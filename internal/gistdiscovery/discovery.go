package gistdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

const (
	defaultAPIBaseURL    = "https://api.github.com"
	defaultPages         = 5
	defaultPerPage       = 100
	defaultMinNodes      = 1
	defaultMaxFileBytes  = 2 * 1024 * 1024
	defaultMaxCandidates = 400
	defaultUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"
)

var ErrBodyTooLarge = errors.New("raw file too large")

// Options controls gist discovery behavior.
type Options struct {
	APIBaseURL    string
	Since         string
	Pages         int
	PerPage       int
	MinNodes      int
	MaxFileBytes  int
	MaxCandidates int
	Token         string
	UserAgent     string
}

// Stats summarizes a discovery run.
type Stats struct {
	GistsScanned     int `json:"gists_scanned"`
	CandidateFiles   int `json:"candidate_files"`
	ValidatedFiles   int `json:"validated_files"`
	ValidURLs        int `json:"valid_urls"`
	DuplicateURLs    int `json:"duplicate_urls"`
	FetchErrors      int `json:"fetch_errors"`
	ParseErrors      int `json:"parse_errors"`
	TooSmallNodeSets int `json:"too_small_node_sets"`
	TooLargeFiles    int `json:"too_large_files"`
}

type gistFile struct {
	Filename string `json:"filename"`
	RawURL   string `json:"raw_url"`
}

type gistItem struct {
	ID    string              `json:"id"`
	Files map[string]gistFile `json:"files"`
}

// IsTargetClashFilename reports whether filename matches desired clash config patterns.
func IsTargetClashFilename(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if n == "all.yaml" || n == "all.yml" || n == "clash.yaml" || n == "clash.yml" {
		return true
	}
	if (strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")) && strings.Contains(n, "clash") {
		return true
	}
	return false
}

func normalizeOptions(opts Options) Options {
	if opts.APIBaseURL == "" {
		opts.APIBaseURL = defaultAPIBaseURL
	}
	opts.APIBaseURL = strings.TrimRight(opts.APIBaseURL, "/")
	if opts.Pages <= 0 {
		opts.Pages = defaultPages
	}
	if opts.PerPage <= 0 || opts.PerPage > 100 {
		opts.PerPage = defaultPerPage
	}
	if opts.MinNodes <= 0 {
		opts.MinNodes = defaultMinNodes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = defaultMaxFileBytes
	}
	if opts.MaxCandidates <= 0 {
		opts.MaxCandidates = defaultMaxCandidates
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}
	return opts
}

// Discover fetches latest public gists, filters target clash YAML files, validates parseability,
// then returns usable raw URLs for subscriptions.
func Discover(ctx context.Context, client *http.Client, opts Options) ([]string, Stats, error) {
	opts = normalizeOptions(opts)
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	candidates, stats, err := collectCandidates(ctx, client, opts)
	if err != nil {
		return nil, stats, err
	}

	valid := make([]string, 0, len(candidates))
	for _, rawURL := range candidates {
		content, err := fetchRawContent(ctx, client, rawURL, opts)
		if err != nil {
			if errors.Is(err, ErrBodyTooLarge) {
				stats.TooLargeFiles++
			} else {
				stats.FetchErrors++
			}
			continue
		}

		stats.ValidatedFiles++
		nodes, err := config.ParseSubscriptionContent(content)
		if err != nil {
			stats.ParseErrors++
			continue
		}
		if len(nodes) < opts.MinNodes {
			stats.TooSmallNodeSets++
			continue
		}

		valid = append(valid, rawURL)
	}

	stats.ValidURLs = len(valid)
	return valid, stats, nil
}

func collectCandidates(ctx context.Context, client *http.Client, opts Options) ([]string, Stats, error) {
	stats := Stats{}
	seen := make(map[string]struct{})
	candidates := make([]string, 0, opts.MaxCandidates)

outer:
	for page := 1; page <= opts.Pages; page++ {
		items, err := listPublicGists(ctx, client, opts, page)
		if err != nil {
			return nil, stats, err
		}
		if len(items) == 0 {
			break
		}

		stats.GistsScanned += len(items)
		for _, item := range items {
			filenames := make([]string, 0, len(item.Files))
			for name := range item.Files {
				filenames = append(filenames, name)
			}
			sort.Strings(filenames)

			for _, name := range filenames {
				file := item.Files[name]
				if !IsTargetClashFilename(file.Filename) {
					continue
				}
				rawURL := strings.TrimSpace(file.RawURL)
				if rawURL == "" {
					continue
				}
				rawURL = canonicalizeRawURL(rawURL, file.Filename)
				u, err := url.Parse(rawURL)
				if err != nil || u.Scheme != "https" {
					continue
				}

				stats.CandidateFiles++
				if _, ok := seen[rawURL]; ok {
					stats.DuplicateURLs++
					continue
				}
				seen[rawURL] = struct{}{}
				candidates = append(candidates, rawURL)
				if len(candidates) >= opts.MaxCandidates {
					break outer
				}
			}
		}
	}

	return candidates, stats, nil
}

// canonicalizeRawURL rewrites gist raw URLs to their latest form:
// https://gist.githubusercontent.com/{user}/{gist}/raw/{filename}
// from hashed variants like:
// https://gist.githubusercontent.com/{user}/{gist}/raw/{hash}/{filename}
func canonicalizeRawURL(rawURL, filename string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return strings.TrimSpace(rawURL)
	}
	if u.Scheme != "https" || u.Host != "gist.githubusercontent.com" {
		return rawURL
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		return rawURL
	}
	// Expected: user/gistid/raw/(hash/)?filename
	if parts[2] != "raw" {
		return rawURL
	}

	finalName := strings.TrimSpace(filename)
	if finalName == "" {
		finalName = parts[len(parts)-1]
	}
	if finalName == "" {
		return rawURL
	}

	u.Path = "/" + parts[0] + "/" + parts[1] + "/raw/" + finalName
	return u.String()
}

func listPublicGists(ctx context.Context, client *http.Client, opts Options, page int) ([]gistItem, error) {
	params := url.Values{}
	params.Set("per_page", fmt.Sprintf("%d", opts.PerPage))
	params.Set("page", fmt.Sprintf("%d", page))
	if strings.TrimSpace(opts.Since) != "" {
		params.Set("since", strings.TrimSpace(opts.Since))
	}
	apiURL := fmt.Sprintf("%s/gists/public?%s", opts.APIBaseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", opts.UserAgent)
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("github api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var items []gistItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	return items, nil
}

func fetchRawContent(ctx context.Context, client *http.Client, rawURL string, opts Options) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", opts.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("raw file status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, int64(opts.MaxFileBytes+1))
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if len(data) > opts.MaxFileBytes {
		return "", ErrBodyTooLarge
	}

	return string(data), nil
}

package gist

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
)

const (
	defaultAPIBaseURL    = "https://api.github.com"
	defaultPages         = 5
	defaultPerPage       = 100
	defaultMaxCandidates = 400

	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"
)

// Options controls public gist scanning.
type Options struct {
	APIBaseURL    string
	Since         string
	Pages         int
	PerPage       int
	MaxCandidates int
	Token         string
	UserAgent     string
}

// Stats summarizes gist candidate collection.
type Stats struct {
	GistsScanned   int `json:"gists_scanned"`
	CandidateFiles int `json:"candidate_files"`
	DuplicateURLs  int `json:"duplicate_urls"`
	FetchErrors    int `json:"fetch_errors"`
}

type gistFile struct {
	Filename string `json:"filename"`
	RawURL   string `json:"raw_url"`
}

type gistItem struct {
	ID    string              `json:"id"`
	Files map[string]gistFile `json:"files"`
}

type githubAPIError struct {
	StatusCode int
	Message    string
}

func (e *githubAPIError) Error() string {
	return fmt.Sprintf("github api status %d: %s", e.StatusCode, e.Message)
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
	if opts.MaxCandidates <= 0 {
		opts.MaxCandidates = defaultMaxCandidates
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	return opts
}

// CollectCandidates fetches public gists and returns candidate raw subscription URLs.
func CollectCandidates(ctx context.Context, client *http.Client, opts Options) ([]string, Stats, error) {
	opts = normalizeOptions(opts)
	if client == nil {
		client = &http.Client{}
	}

	stats := Stats{}
	seen := make(map[string]struct{})
	candidates := make([]string, 0, opts.MaxCandidates)

outer:
	for page := 1; page <= opts.Pages; page++ {
		items, err := listPublicGists(ctx, client, opts, page)
		if err != nil {
			stats.FetchErrors++
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
				rawURL = CanonicalizeRawURL(rawURL, file.Filename)
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

// CanonicalizeRawURL rewrites gist raw URLs to stable latest-form URL.
func CanonicalizeRawURL(rawURL, filename string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return strings.TrimSpace(rawURL)
	}
	if u.Scheme != "https" || u.Host != "gist.githubusercontent.com" {
		return rawURL
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "raw" {
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
	items, err := listPublicGistsOnce(ctx, client, opts, page)
	if err == nil {
		return items, nil
	}

	var apiErr *githubAPIError
	if opts.Token != "" && errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
		retryOpts := opts
		retryOpts.Token = ""
		return listPublicGistsOnce(ctx, client, retryOpts, page)
	}
	return nil, err
}

func listPublicGistsOnce(ctx context.Context, client *http.Client, opts Options, page int) ([]gistItem, error) {
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
		return nil, &githubAPIError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}

	var items []gistItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	return items, nil
}

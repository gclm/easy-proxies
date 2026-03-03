package gist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const (
	defaultAPIBaseURL    = "https://api.github.com"
	defaultSearchBaseURL = "https://gist.github.com/search"
	defaultKeyword       = "clash"
	defaultPages         = 5
	defaultPerPage       = 100
	defaultMaxCandidates = 400

	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"
)

var gistIDFromSearchPattern = regexp.MustCompile(`href="/[A-Za-z0-9][A-Za-z0-9_-]*/([0-9a-f]{32})"`)

// Options controls gist keyword search behavior.
type Options struct {
	APIBaseURL    string
	SearchBaseURL string
	Keyword       string
	Since         string
	Pages         int
	PerPage       int
	MaxCandidates int
	Token         string
	UserAgent     string
}

// Stats summarizes gist candidate collection.
type Stats struct {
	SearchHits     int      `json:"search_hits"`
	GistsScanned   int      `json:"gists_scanned"`
	FilesScanned   int      `json:"files_scanned"`
	NonTargetFiles int      `json:"non_target_files"`
	CandidateFiles int      `json:"candidate_files"`
	InvalidRawURLs int      `json:"invalid_raw_urls"`
	DuplicateURLs  int      `json:"duplicate_urls"`
	FetchErrors    int      `json:"fetch_errors"`
	FetchSamples   []string `json:"fetch_samples,omitempty"`
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
	if opts.SearchBaseURL == "" {
		opts.SearchBaseURL = defaultSearchBaseURL
	}
	opts.SearchBaseURL = strings.TrimRight(opts.SearchBaseURL, "/")
	if strings.TrimSpace(opts.Keyword) == "" {
		opts.Keyword = defaultKeyword
	}
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

// CollectCandidates searches gists by keyword and returns candidate raw subscription URLs.
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
		ids, err := searchGistIDsByKeyword(ctx, client, opts, page)
		if err != nil {
			stats.FetchErrors++
			return nil, stats, err
		}
		if len(ids) == 0 {
			break
		}
		stats.SearchHits += len(ids)

		for _, id := range ids {
			item, err := getGistByID(ctx, client, opts, id)
			if err != nil {
				stats.FetchErrors++
				stats.FetchSamples = appendSample(stats.FetchSamples, fmt.Sprintf("gist=%s (%v)", id, err))
				continue
			}
			stats.GistsScanned++

			filenames := make([]string, 0, len(item.Files))
			for name := range item.Files {
				filenames = append(filenames, name)
			}
			sort.Strings(filenames)

			for _, name := range filenames {
				file := item.Files[name]
				stats.FilesScanned++
				if !IsTargetClashFilename(file.Filename) {
					stats.NonTargetFiles++
					continue
				}
				rawURL := strings.TrimSpace(file.RawURL)
				if rawURL == "" {
					continue
				}
				rawURL = CanonicalizeRawURL(rawURL, file.Filename)
				u, err := url.Parse(rawURL)
				if err != nil || u.Scheme != "https" {
					stats.InvalidRawURLs++
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

func appendSample(samples []string, entry string) []string {
	const maxSamples = 5
	if len(samples) >= maxSamples {
		return samples
	}
	return append(samples, entry)
}

// IsTargetClashFilename accepts files containing clash keyword with yaml/yml/txt suffix.
func IsTargetClashFilename(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if !strings.Contains(n, "clash") {
		return false
	}
	return strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".txt")
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

func searchGistIDsByKeyword(ctx context.Context, client *http.Client, opts Options, page int) ([]string, error) {
	params := url.Values{}
	params.Set("q", strings.TrimSpace(opts.Keyword))
	params.Set("p", fmt.Sprintf("%d", page))
	searchURL := fmt.Sprintf("%s?%s", opts.SearchBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("gist search status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	matches := gistIDFromSearchPattern.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		id := strings.TrimSpace(m[1])
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func getGistByID(ctx context.Context, client *http.Client, opts Options, id string) (gistItem, error) {
	item, err := getGistByIDOnce(ctx, client, opts, id)
	if err == nil {
		return item, nil
	}

	var apiErr *githubAPIError
	if opts.Token != "" && errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
		retry := opts
		retry.Token = ""
		return getGistByIDOnce(ctx, client, retry, id)
	}
	return gistItem{}, err
}

func getGistByIDOnce(ctx context.Context, client *http.Client, opts Options, id string) (gistItem, error) {
	apiURL := fmt.Sprintf("%s/gists/%s", opts.APIBaseURL, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return gistItem{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", opts.UserAgent)
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return gistItem{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return gistItem{}, &githubAPIError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}

	var item gistItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return gistItem{}, err
	}
	return item, nil
}

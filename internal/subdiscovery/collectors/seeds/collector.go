package seeds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitHubAPIBaseURL = "https://api.github.com"
	defaultUserAgent        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"
)

var xPattern = regexp.MustCompile(`\{x\}(\.[a-zA-Z0-9]+)(?:/|$)`) // {x}.yaml

// Options controls seed-based collection and template expansion.
type Options struct {
	SeedsFile        string
	ExtraURLs        []string
	Now              time.Time
	GitHubToken      string
	GitHubAPIBaseURL string
	UserAgent        string
}

// Stats summarizes seed collector processing.
type Stats struct {
	InputLines         int `json:"input_lines"`
	ExpandedCandidates int `json:"expanded_candidates"`
	InvalidLines       int `json:"invalid_lines"`
	GitHubXResolved    int `json:"github_x_resolved"`
	GitHubXErrors      int `json:"github_x_errors"`
	FallbackXExpanded  int `json:"fallback_x_expanded"`
}

// CollectCandidates loads seeds from file + extra URLs, expands templates, and returns candidate URLs.
func CollectCandidates(ctx context.Context, client *http.Client, opts Options) ([]string, Stats, error) {
	if client == nil {
		client = &http.Client{}
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if strings.TrimSpace(opts.GitHubAPIBaseURL) == "" {
		opts.GitHubAPIBaseURL = defaultGitHubAPIBaseURL
	}
	opts.GitHubAPIBaseURL = strings.TrimRight(opts.GitHubAPIBaseURL, "/")
	if strings.TrimSpace(opts.UserAgent) == "" {
		opts.UserAgent = defaultUserAgent
	}

	lines, err := readSeedLines(opts.SeedsFile)
	if err != nil {
		return nil, Stats{}, err
	}
	lines = append(lines, opts.ExtraURLs...)

	stats := Stats{}
	results := make([]string, 0, len(lines))
	for _, line := range lines {
		stats.InputLines++
		expanded, ok := expandSeedLine(ctx, client, line, opts, &stats)
		if !ok {
			stats.InvalidLines++
			continue
		}
		results = append(results, expanded...)
	}
	stats.ExpandedCandidates = len(results)
	return results, stats, nil
}

func readSeedLines(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		raw := strings.TrimSpace(line)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		cleaned = append(cleaned, raw)
	}
	return cleaned, nil
}

func expandSeedLine(ctx context.Context, client *http.Client, raw string, opts Options, stats *Stats) ([]string, bool) {
	seed := normalizeSeedLine(raw)
	if seed == "" {
		return nil, false
	}

	seed = replaceDatetime(seed, opts.Now)
	if strings.Contains(seed, "{x}") {
		if resolved, ok := resolveGitHubXTemplate(ctx, client, seed, opts); ok {
			stats.GitHubXResolved++
			if containsUnresolvedTemplate(resolved) {
				return nil, false
			}
			return []string{resolved}, true
		}
		stats.GitHubXErrors++
		fallback := expandXByDigits(seed)
		stats.FallbackXExpanded += len(fallback)
		if len(fallback) == 0 {
			return nil, false
		}
		return fallback, true
	}

	if containsUnresolvedTemplate(seed) {
		return nil, false
	}
	return []string{seed}, true
}

func normalizeSeedLine(raw string) string {
	seed := strings.TrimSpace(raw)
	seed = strings.Trim(seed, "\",")
	if seed == "" {
		return ""
	}
	if idx := strings.Index(seed, "|"); idx >= 0 {
		seed = strings.TrimSpace(seed[:idx])
	}
	return strings.TrimSpace(seed)
}

func replaceDatetime(seed string, now time.Time) string {
	replacer := strings.NewReplacer(
		"{Y}", now.Format("2006"),
		"{m}", now.Format("01"),
		"{d}", now.Format("02"),
		"{H}", now.Format("15"),
		"{M}", now.Format("04"),
		"{S}", now.Format("05"),
		"{Ymd}", now.Format("20060102"),
		"{Y_m_d}", now.Format("2006_01_02"),
	)
	return replacer.Replace(seed)
}

func containsUnresolvedTemplate(seed string) bool {
	return strings.Contains(seed, "{") || strings.Contains(seed, "}")
}

func expandXByDigits(seed string) []string {
	if !strings.Contains(seed, "{x}") {
		return []string{seed}
	}
	results := make([]string, 0, 10)
	for i := 0; i <= 9; i++ {
		results = append(results, strings.ReplaceAll(seed, "{x}", strconv.Itoa(i)))
	}
	return results
}

func resolveGitHubXTemplate(ctx context.Context, client *http.Client, seed string, opts Options) (string, bool) {
	u, err := url.Parse(seed)
	if err != nil || u.Scheme != "https" || u.Host != "raw.githubusercontent.com" {
		return "", false
	}
	m := xPattern.FindStringSubmatch(seed)
	if len(m) < 2 {
		return "", false
	}
	suffix := m[1]

	owner, repo, branch, dirPath, ok := parseGitHubRaw(seed, suffix)
	if !ok {
		return "", false
	}
	filename, ok := pickGitHubFilename(ctx, client, owner, repo, branch, dirPath, suffix, opts)
	if !ok {
		return "", false
	}

	re := regexp.MustCompile(`\{x\}` + regexp.QuoteMeta(suffix))
	resolved := re.ReplaceAllString(seed, filename)
	return resolved, true
}

func parseGitHubRaw(seed, suffix string) (owner, repo, branch, dirPath string, ok bool) {
	u, err := url.Parse(seed)
	if err != nil {
		return "", "", "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		return "", "", "", "", false
	}
	owner, repo = parts[0], parts[1]
	rest := parts[2:]

	if len(rest) >= 3 && rest[0] == "refs" && rest[1] == "heads" {
		branch = rest[2]
		rest = rest[3:]
	} else {
		branch = rest[0]
		rest = rest[1:]
	}
	if branch == "" || len(rest) == 0 {
		return "", "", "", "", false
	}

	rawPath := strings.Join(rest, "/")
	dirPath = strings.TrimSuffix(rawPath, "{x}"+suffix)
	dirPath = strings.TrimSuffix(dirPath, "/")
	return owner, repo, branch, dirPath, true
}

type githubContentItem struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func pickGitHubFilename(ctx context.Context, client *http.Client, owner, repo, branch, dirPath, suffix string, opts Options) (string, bool) {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents", owner, repo)
	if dirPath != "" {
		apiPath += "/" + escapePath(dirPath)
	}
	apiURL := fmt.Sprintf("%s%s?ref=%s", opts.GitHubAPIBaseURL, apiPath, url.QueryEscape(branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", opts.UserAgent)
	if strings.TrimSpace(opts.GitHubToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.GitHubToken))
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	var items []githubContentItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return "", false
	}

	matches := make([]string, 0, len(items))
	for _, item := range items {
		if item.Type != "file" {
			continue
		}
		if strings.HasSuffix(item.Name, suffix) {
			matches = append(matches, item.Name)
		}
	}
	if len(matches) == 0 {
		return "", false
	}

	today := opts.Now.Format("20060102")
	todayMatches := make([]string, 0, len(matches))
	for _, name := range matches {
		if strings.Contains(name, today) {
			todayMatches = append(todayMatches, name)
		}
	}
	if len(todayMatches) > 0 {
		sort.Strings(todayMatches)
		return todayMatches[len(todayMatches)-1], true
	}
	sort.Strings(matches)
	return matches[len(matches)-1], true
}

func escapePath(raw string) string {
	segments := strings.Split(strings.Trim(raw, "/"), "/")
	escaped := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(seg))
	}
	return path.Join(escaped...)
}

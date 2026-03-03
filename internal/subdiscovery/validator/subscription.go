package validator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"easy_proxies/internal/config"
)

const (
	defaultMinNodes     = 1
	defaultMaxFileBytes = 2 * 1024 * 1024
	defaultConcurrency  = 12
	defaultUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"
)

var ErrBodyTooLarge = errors.New("raw file too large")

// Options controls subscription validation behavior.
type Options struct {
	MinNodes     int
	MaxFileBytes int
	Concurrency  int
	UserAgent    string
}

// Stats summarizes validation outcomes.
type Stats struct {
	ValidatedFiles   int      `json:"validated_files"`
	ValidURLs        int      `json:"valid_urls"`
	InvalidURLs      int      `json:"invalid_urls"`
	FetchErrors      int      `json:"fetch_errors"`
	ParseErrors      int      `json:"parse_errors"`
	TooSmallNodeSets int      `json:"too_small_node_sets"`
	TooLargeFiles    int      `json:"too_large_files"`
	FetchSamples     []string `json:"fetch_samples,omitempty"`
	ParseSamples     []string `json:"parse_samples,omitempty"`
	TooSmallSamples  []string `json:"too_small_samples,omitempty"`
	TooLargeSamples  []string `json:"too_large_samples,omitempty"`
}

func normalizeOptions(opts Options) Options {
	if opts.MinNodes <= 0 {
		opts.MinNodes = defaultMinNodes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = defaultMaxFileBytes
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultConcurrency
	}
	if strings.TrimSpace(opts.UserAgent) == "" {
		opts.UserAgent = defaultUserAgent
	}
	return opts
}

// ValidateSubscriptionURLs validates each URL by fetching and parsing nodes.
func ValidateSubscriptionURLs(ctx context.Context, client *http.Client, urls []string, opts Options) ([]string, Stats) {
	opts = normalizeOptions(opts)
	if client == nil {
		client = &http.Client{}
	}

	stats := Stats{}

	type job struct {
		index int
		url   string
	}
	type result struct {
		index     int
		url       string
		valid     bool
		validated bool
		nodes     int
		err       error
		reason    string // invalid|fetch|parse|toolarge|toosmall
	}

	jobs := make(chan job)
	results := make(chan result, len(urls))
	workers := opts.Concurrency
	if workers > len(urls) {
		workers = len(urls)
	}
	if workers <= 0 {
		workers = 1
	}

	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for j := range jobs {
			rawURL := strings.TrimSpace(j.url)
			u, err := url.Parse(rawURL)
			if err != nil || u.Scheme != "https" {
				results <- result{index: j.index, url: rawURL, reason: "invalid"}
				continue
			}

			content, err := fetchRawContent(ctx, client, rawURL, opts)
			if err != nil {
				if errors.Is(err, ErrBodyTooLarge) {
					results <- result{index: j.index, url: rawURL, reason: "toolarge", err: err}
				} else {
					results <- result{index: j.index, url: rawURL, reason: "fetch", err: err}
				}
				continue
			}

			nodes, err := config.ParseSubscriptionContent(content)
			if err != nil {
				results <- result{index: j.index, url: rawURL, validated: true, reason: "parse", err: err}
				continue
			}
			if len(nodes) < opts.MinNodes {
				results <- result{index: j.index, url: rawURL, validated: true, reason: "toosmall", nodes: len(nodes)}
				continue
			}
			results <- result{index: j.index, url: rawURL, validated: true, valid: true, nodes: len(nodes)}
		}
	}

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}
	go func() {
		for i, u := range urls {
			jobs <- job{index: i, url: u}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	outcomes := make([]result, 0, len(urls))
	for r := range results {
		outcomes = append(outcomes, r)
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].index < outcomes[j].index })

	valid := make([]string, 0, len(urls))
	for _, r := range outcomes {
		if r.validated {
			stats.ValidatedFiles++
		}
		switch r.reason {
		case "invalid":
			stats.InvalidURLs++
		case "toolarge":
			stats.TooLargeFiles++
			stats.TooLargeSamples = appendSample(stats.TooLargeSamples, r.url)
		case "fetch":
			stats.FetchErrors++
			stats.FetchSamples = appendSample(stats.FetchSamples, fmt.Sprintf("%s (%v)", r.url, r.err))
		case "parse":
			stats.ParseErrors++
			stats.ParseSamples = appendSample(stats.ParseSamples, fmt.Sprintf("%s (%v)", r.url, r.err))
		case "toosmall":
			stats.TooSmallNodeSets++
			stats.TooSmallSamples = appendSample(stats.TooSmallSamples, fmt.Sprintf("%s (nodes=%d)", r.url, r.nodes))
		}
		if r.valid {
			valid = append(valid, r.url)
		}
	}

	stats.ValidURLs = len(valid)
	return valid, stats
}

func appendSample(samples []string, entry string) []string {
	const maxSamples = 5
	if len(samples) >= maxSamples {
		return samples
	}
	return append(samples, entry)
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

package validator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

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
	ValidatedFiles   int `json:"validated_files"`
	ValidURLs        int `json:"valid_urls"`
	InvalidURLs      int `json:"invalid_urls"`
	FetchErrors      int `json:"fetch_errors"`
	ParseErrors      int `json:"parse_errors"`
	TooSmallNodeSets int `json:"too_small_node_sets"`
	TooLargeFiles    int `json:"too_large_files"`
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

	valid := make([]string, 0, len(urls))
	stats := Stats{}
	for _, rawURL := range urls {
		rawURL = strings.TrimSpace(rawURL)
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme != "https" {
			stats.InvalidURLs++
			continue
		}

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
	return valid, stats
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

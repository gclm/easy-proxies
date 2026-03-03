package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"easy_proxies/internal/gistdiscovery"
)

type discoveryState struct {
	UsedSince  string              `json:"used_since,omitempty"`
	NextSince  string              `json:"next_since"`
	StartedAt  string              `json:"started_at"`
	FinishedAt string              `json:"finished_at"`
	Overlap    string              `json:"overlap"`
	Stats      gistdiscovery.Stats `json:"stats"`
}

func main() {
	var (
		outPath       string
		statsOutPath  string
		stateOutPath  string
		apiBaseURL    string
		userAgent     string
		since         string
		overlap       time.Duration
		pages         int
		perPage       int
		minNodes      int
		maxFileBytes  int
		maxCandidates int
		allowEmpty    bool
		timeoutSec    int
	)

	flag.StringVar(&outPath, "out", "subscriptions.gist.txt", "output file path")
	flag.StringVar(&statsOutPath, "stats-out", "subscriptions.gist.stats.json", "stats output path")
	flag.StringVar(&stateOutPath, "state-out", "subscriptions.gist.state.json", "state output path")
	flag.StringVar(&apiBaseURL, "api-base", "", "GitHub API base URL (optional)")
	flag.StringVar(&userAgent, "user-agent", "", "HTTP User-Agent for API/raw requests (optional)")
	flag.StringVar(&since, "since", "", "only fetch gists updated since RFC3339 time")
	flag.DurationVar(&overlap, "overlap", 10*time.Minute, "overlap duration when calculating next_since")
	flag.IntVar(&pages, "pages", 5, "number of pages to scan")
	flag.IntVar(&perPage, "per-page", 100, "gists per page")
	flag.IntVar(&minNodes, "min-nodes", 1, "minimum parsed nodes to accept a url")
	flag.IntVar(&maxFileBytes, "max-file-bytes", 2*1024*1024, "max raw file size")
	flag.IntVar(&maxCandidates, "max-candidates", 400, "max candidate raw URLs to validate")
	flag.BoolVar(&allowEmpty, "allow-empty", false, "allow writing empty result")
	flag.IntVar(&timeoutSec, "timeout", 20, "http timeout in seconds")
	flag.Parse()

	since = strings.TrimSpace(since)
	if since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			fmt.Fprintf(os.Stderr, "invalid -since value (RFC3339 required): %v\n", err)
			os.Exit(1)
		}
	}
	if overlap < 0 {
		fmt.Fprintln(os.Stderr, "invalid -overlap value (must be >= 0)")
		os.Exit(1)
	}

	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	startedAt := time.Now().UTC()
	opts := gistdiscovery.Options{
		APIBaseURL:    apiBaseURL,
		Since:         since,
		Pages:         pages,
		PerPage:       perPage,
		MinNodes:      minNodes,
		MaxFileBytes:  maxFileBytes,
		MaxCandidates: maxCandidates,
		Token:         strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		UserAgent:     strings.TrimSpace(userAgent),
	}

	urls, stats, err := gistdiscovery.Discover(context.Background(), client, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover failed: %v\n", err)
		os.Exit(1)
	}
	if len(urls) == 0 && !allowEmpty {
		fmt.Fprintln(os.Stderr, "discover failed: no valid subscription urls found")
		os.Exit(1)
	}

	content := strings.Join(urls, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write output failed: %v\n", err)
		os.Exit(1)
	}

	statsData, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode stats failed: %v\n", err)
		os.Exit(1)
	}
	statsData = append(statsData, '\n')
	if err := os.WriteFile(statsOutPath, statsData, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write stats failed: %v\n", err)
		os.Exit(1)
	}

	finishedAt := time.Now().UTC()
	nextSince := finishedAt.Add(-overlap).Format(time.RFC3339)
	state := discoveryState{
		UsedSince:  since,
		NextSince:  nextSince,
		StartedAt:  startedAt.Format(time.RFC3339),
		FinishedAt: finishedAt.Format(time.RFC3339),
		Overlap:    overlap.String(),
		Stats:      stats,
	}
	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode state failed: %v\n", err)
		os.Exit(1)
	}
	stateData = append(stateData, '\n')
	if err := os.WriteFile(stateOutPath, stateData, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write state failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("discovered=%d candidates=%d gists=%d output=%s\n", stats.ValidURLs, stats.CandidateFiles, stats.GistsScanned, outPath)
}

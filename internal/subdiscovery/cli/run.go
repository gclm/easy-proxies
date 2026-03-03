package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"easy_proxies/internal/subdiscovery"
	"easy_proxies/internal/subdiscovery/collectors/gist"
	seedcollector "easy_proxies/internal/subdiscovery/collectors/seeds"
	"easy_proxies/internal/subdiscovery/validator"
)

// Main runs CLI discovery using process args/stdout/stderr.
func Main() int {
	return Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
}

// Run executes discovery from CLI args.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscription_discovery", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		outPath           string
		nodesOutPath      string
		statsOutPath      string
		stateOutPath      string
		apiBaseURL        string
		userAgent         string
		since             string
		overlap           time.Duration
		pages             int
		perPage           int
		minNodes          int
		maxFileBytes      int
		maxCandidates     int
		extraURLs         string
		seedsFile         string
		disableProxyShare bool
		disableGist       bool
		disableSeeds      bool
		allowEmpty        bool
		allowEmptyNodes   bool
		quiet             bool
		timeoutSec        int
	)

	fs.StringVar(&outPath, "out", "subscriptions.txt", "subscription output file path")
	fs.StringVar(&nodesOutPath, "nodes-out", "nodes.txt", "proxy_share node output file path")
	fs.StringVar(&statsOutPath, "stats-out", "subscriptions.stats.json", "stats output path")
	fs.StringVar(&stateOutPath, "state-out", "subscriptions.state.json", "state output path")
	fs.StringVar(&apiBaseURL, "api-base", "", "GitHub API base URL (optional)")
	fs.StringVar(&userAgent, "user-agent", "", "HTTP User-Agent for API/raw requests (optional)")
	fs.StringVar(&since, "since", "", "only fetch gists updated since RFC3339 time")
	fs.DurationVar(&overlap, "overlap", 10*time.Minute, "overlap duration when calculating next_since")
	fs.IntVar(&pages, "pages", 5, "number of pages to scan")
	fs.IntVar(&perPage, "per-page", 100, "gists per page")
	fs.IntVar(&minNodes, "min-nodes", 1, "minimum parsed nodes to accept a url")
	fs.IntVar(&maxFileBytes, "max-file-bytes", 2*1024*1024, "max raw file size")
	fs.IntVar(&maxCandidates, "max-candidates", 400, "max candidate raw URLs to validate")
	fs.StringVar(&seedsFile, "seeds-file", "", "path to seed subscription list file (one URL per line)")
	fs.StringVar(&extraURLs, "extra-urls", "", "comma-separated extra subscription URLs to validate and include")
	fs.BoolVar(&disableProxyShare, "disable-proxy-share", false, "disable proxy_share node discovery")
	fs.BoolVar(&disableGist, "disable-gist", false, "disable gist collector")
	fs.BoolVar(&disableSeeds, "disable-seeds", false, "disable seeds collector")
	fs.BoolVar(&allowEmpty, "allow-empty", false, "allow writing empty subscription result")
	fs.BoolVar(&allowEmptyNodes, "allow-empty-nodes", true, "allow writing empty proxy_share node result")
	fs.BoolVar(&quiet, "quiet", false, "disable progress logs")
	fs.IntVar(&timeoutSec, "timeout", 20, "http timeout in seconds")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	since = strings.TrimSpace(since)
	if since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			fmt.Fprintf(stderr, "invalid -since value (RFC3339 required): %v\n", err)
			return 1
		}
	}
	if overlap < 0 {
		fmt.Fprintln(stderr, "invalid -overlap value (must be >= 0)")
		return 1
	}

	startedAt := time.Now().UTC()
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	token := strings.TrimSpace(os.Getenv("GIST_DISCOVERY_TOKEN"))
	userAgent = strings.TrimSpace(userAgent)
	logger := func(format string, args ...any) {
		if quiet {
			return
		}
		fmt.Fprintf(stderr, "[subscription_discovery] "+format+"\n", args...)
	}
	logger("config since=%q pages=%d per_page=%d seeds_file=%t extra_urls=%d timeout=%ds", since, pages, perPage, strings.TrimSpace(seedsFile) != "", len(splitCSV(extraURLs)), timeoutSec)

	runOpts := subdiscovery.Options{
		StartedAt:       startedAt,
		Since:           since,
		Overlap:         overlap,
		Logf:            logger,
		AllowEmpty:      allowEmpty,
		AllowEmptyNodes: allowEmptyNodes,
		DisableGist:     disableGist,
		DisableSeeds:    disableSeeds,
		DisableProxy:    disableProxyShare,
		Gist: gist.Options{
			APIBaseURL:    apiBaseURL,
			Since:         since,
			Pages:         pages,
			PerPage:       perPage,
			MaxCandidates: maxCandidates,
			Token:         token,
			UserAgent:     userAgent,
		},
		Seeds: seedcollector.Options{
			SeedsFile:        seedsFile,
			ExtraURLs:        splitCSV(extraURLs),
			Now:              startedAt,
			GitHubToken:      token,
			GitHubAPIBaseURL: apiBaseURL,
			UserAgent:        userAgent,
		},
		Validation: validator.Options{
			MinNodes:     minNodes,
			MaxFileBytes: maxFileBytes,
			UserAgent:    userAgent,
		},
	}

	result, err := subdiscovery.Run(ctx, client, runOpts)
	if err != nil {
		fmt.Fprintf(stderr, "discover failed: %v\n", err)
		return 1
	}

	if err := writeLineFile(outPath, result.Subscriptions); err != nil {
		fmt.Fprintf(stderr, "write output failed: %v\n", err)
		return 1
	}
	if err := writeLineFile(nodesOutPath, result.Nodes); err != nil {
		fmt.Fprintf(stderr, "write nodes output failed: %v\n", err)
		return 1
	}
	if err := writeJSONFile(statsOutPath, result.Stats); err != nil {
		fmt.Fprintf(stderr, "write stats failed: %v\n", err)
		return 1
	}
	if err := writeJSONFile(stateOutPath, result.State); err != nil {
		fmt.Fprintf(stderr, "write state failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "%s out=%s nodes_out=%s\n", subdiscovery.SummaryLine(result), outPath, nodesOutPath)
	return 0
}

func writeLineFile(path string, lines []string) error {
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, item := range parts {
		v := strings.TrimSpace(item)
		if v != "" {
			result = append(result, v)
		}
	}
	return result
}

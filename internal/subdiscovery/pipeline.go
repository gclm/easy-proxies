package subdiscovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"easy_proxies/internal/subdiscovery/collectors/gist"
	"easy_proxies/internal/subdiscovery/collectors/proxyshare"
	"easy_proxies/internal/subdiscovery/collectors/seeds"
	"easy_proxies/internal/subdiscovery/validator"
)

// Run executes the discovery pipeline and returns subscriptions, nodes, stats, and state.
func Run(ctx context.Context, client *http.Client, opts Options) (Result, error) {
	opts = normalizeOptions(opts)
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	stats := Stats{CollectorErrors: make(map[string]string)}
	seedCandidates := make([]string, 0)
	gistCandidates := make([]string, 0)

	if !opts.DisableSeeds {
		seedOpts := opts.Seeds
		if seedOpts.Now.IsZero() {
			seedOpts.Now = opts.StartedAt
		}
		seedOpts.UserAgent = firstNonEmpty(seedOpts.UserAgent, opts.Gist.UserAgent)
		if seedOpts.GitHubToken == "" {
			seedOpts.GitHubToken = opts.Gist.Token
		}
		seedOpts.GitHubAPIBaseURL = firstNonEmpty(seedOpts.GitHubAPIBaseURL, opts.Gist.APIBaseURL)

		urls, seedStats, err := seeds.CollectCandidates(ctx, client, seedOpts)
		stats.Seeds = seedStats
		if err != nil {
			stats.CollectorErrors["seeds"] = err.Error()
		} else {
			seedCandidates = urls
		}
	}

	if !opts.DisableGist {
		urls, gistStats, err := gist.CollectCandidates(ctx, client, opts.Gist)
		stats.Gist = gistStats
		if err != nil {
			stats.CollectorErrors["gist"] = err.Error()
		} else {
			gistCandidates = urls
		}
	}

	merged := mergeCandidates(seedCandidates, gistCandidates, &stats)
	valid, vstats := validator.ValidateSubscriptionURLs(ctx, client, merged, opts.Validation)
	stats.Validation = vstats
	stats.ValidSubscriptions = len(valid)

	if len(valid) == 0 && !opts.AllowEmpty {
		return Result{}, errors.New("discover failed: no valid subscription urls found")
	}

	nodes := make([]string, 0)
	if !opts.DisableProxy {
		ns, proxyStats, err := proxyshare.DiscoverNodes(ctx, client, opts.ProxyShare)
		stats.ProxyShare = proxyStats
		if err != nil {
			stats.CollectorErrors["proxy_share"] = err.Error()
		}
		nodes = ns
		if len(nodes) == 0 && !opts.AllowEmptyNodes {
			return Result{}, errors.New("proxy_share discovery failed: no nodes found")
		}
	}
	stats.TotalNodes = len(nodes)
	if len(stats.CollectorErrors) == 0 {
		stats.CollectorErrors = nil
	}

	finishedAt := time.Now().UTC()
	nextSince := finishedAt.Add(-opts.Overlap).Format(time.RFC3339)
	state := State{
		UsedSince:  strings.TrimSpace(opts.Since),
		NextSince:  nextSince,
		StartedAt:  opts.StartedAt.Format(time.RFC3339),
		FinishedAt: finishedAt.Format(time.RFC3339),
		Overlap:    opts.Overlap.String(),
		Stats:      stats,
	}

	return Result{
		Subscriptions: valid,
		Nodes:         nodes,
		Stats:         stats,
		State:         state,
	}, nil
}

func normalizeOptions(opts Options) Options {
	if opts.StartedAt.IsZero() {
		opts.StartedAt = time.Now().UTC()
	}
	if opts.Overlap <= 0 {
		opts.Overlap = 10 * time.Minute
	}
	if strings.TrimSpace(opts.Gist.UserAgent) == "" {
		opts.Gist.UserAgent = gist.DefaultUserAgent
	}
	if strings.TrimSpace(opts.Seeds.UserAgent) == "" {
		opts.Seeds.UserAgent = opts.Gist.UserAgent
	}
	if strings.TrimSpace(opts.ProxyShare.UserAgent) == "" {
		opts.ProxyShare.UserAgent = opts.Gist.UserAgent
	}
	return opts
}

func mergeCandidates(seedCandidates, gistCandidates []string, stats *Stats) []string {
	stats.CandidateTotal = len(seedCandidates) + len(gistCandidates)
	if stats.CandidateTotal == 0 {
		return nil
	}

	seen := make(map[string]struct{}, stats.CandidateTotal)
	merged := make([]string, 0, stats.CandidateTotal)
	appendUnique := func(urls []string) {
		for _, raw := range urls {
			u := strings.TrimSpace(raw)
			if u == "" {
				continue
			}
			if _, ok := seen[u]; ok {
				stats.DuplicateCandidates++
				continue
			}
			seen[u] = struct{}{}
			merged = append(merged, u)
		}
	}

	appendUnique(seedCandidates)
	appendUnique(gistCandidates)
	stats.CandidateUnique = len(merged)
	return merged
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// SummaryLine returns a compact one-line summary for command output.
func SummaryLine(result Result) string {
	return fmt.Sprintf(
		"subscriptions=%d nodes=%d candidates=%d gists=%d",
		result.Stats.ValidSubscriptions,
		result.Stats.TotalNodes,
		result.Stats.CandidateUnique,
		result.Stats.Gist.GistsScanned,
	)
}

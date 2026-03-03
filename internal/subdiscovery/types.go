package subdiscovery

import (
	"time"

	"easy_proxies/internal/subdiscovery/collectors/gist"
	"easy_proxies/internal/subdiscovery/collectors/proxyshare"
	"easy_proxies/internal/subdiscovery/collectors/seeds"
	"easy_proxies/internal/subdiscovery/validator"
)

// Options controls the full discovery pipeline.
type Options struct {
	StartedAt time.Time
	Since     string
	Overlap   time.Duration

	AllowEmpty      bool
	AllowEmptyNodes bool
	DisableGist     bool
	DisableSeeds    bool
	DisableProxy    bool

	Gist       gist.Options
	Seeds      seeds.Options
	Validation validator.Options
	ProxyShare proxyshare.Options
}

// Stats summarizes collector, validation, and output counts.
type Stats struct {
	CandidateTotal      int               `json:"candidate_total"`
	CandidateUnique     int               `json:"candidate_unique"`
	DuplicateCandidates int               `json:"duplicate_candidates"`
	ValidSubscriptions  int               `json:"valid_subscriptions"`
	TotalNodes          int               `json:"total_nodes"`
	CollectorErrors     map[string]string `json:"collector_errors,omitempty"`

	Gist       gist.Stats       `json:"gist"`
	Seeds      seeds.Stats      `json:"seeds"`
	Validation validator.Stats  `json:"validation"`
	ProxyShare proxyshare.Stats `json:"proxy_share"`
}

// State is persisted between workflow runs.
type State struct {
	UsedSince  string `json:"used_since,omitempty"`
	NextSince  string `json:"next_since"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Overlap    string `json:"overlap"`
	Stats      Stats  `json:"stats"`
}

// Result contains complete pipeline output.
type Result struct {
	Subscriptions []string
	Nodes         []string
	Stats         Stats
	State         State
}

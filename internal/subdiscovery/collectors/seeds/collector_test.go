package seeds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCollectCandidates_DateTemplate(t *testing.T) {
	now := time.Date(2026, 3, 3, 12, 34, 56, 0, time.UTC)
	urls, stats, err := CollectCandidates(context.Background(), nil, Options{
		Now: now,
		ExtraURLs: []string{
			`"https://www.freeclashnode.com/uploads/{Y}/{m}/0-{Ymd}.yaml",`,
			"https://www.freeclashnode.com/uploads/{Y}/{m}/1-{Ymd}.yaml",
		},
	})
	if err != nil {
		t.Fatalf("CollectCandidates error: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("len(urls)=%d, want 2", len(urls))
	}
	if urls[0] != "https://www.freeclashnode.com/uploads/2026/03/0-20260303.yaml" {
		t.Fatalf("urls[0]=%q", urls[0])
	}
	if urls[1] != "https://www.freeclashnode.com/uploads/2026/03/1-20260303.yaml" {
		t.Fatalf("urls[1]=%q", urls[1])
	}
	if stats.FallbackXExpanded != 0 {
		t.Fatalf("fallback_x_expanded=%d, want 0", stats.FallbackXExpanded)
	}
}

func TestCollectCandidates_GitHubXTemplate(t *testing.T) {
	now := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	mux.HandleFunc("/repos/owner/repo/contents/data/2026_03_03", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("ref"); got != "main" {
			t.Fatalf("ref=%q, want main", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"name": "20260303-0.yaml", "type": "file"},
			{"name": "20260303-9.yaml", "type": "file"},
			{"name": "notes.txt", "type": "file"},
		})
	})

	urls, stats, err := CollectCandidates(context.Background(), ts.Client(), Options{
		Now:              now,
		GitHubAPIBaseURL: ts.URL,
		ExtraURLs: []string{
			"https://raw.githubusercontent.com/owner/repo/refs/heads/main/data/{Y_m_d}/{x}.yaml",
		},
	})
	if err != nil {
		t.Fatalf("CollectCandidates error: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("len(urls)=%d, want 1", len(urls))
	}
	want := "https://raw.githubusercontent.com/owner/repo/refs/heads/main/data/2026_03_03/20260303-9.yaml"
	if urls[0] != want {
		t.Fatalf("urls[0]=%q, want %q", urls[0], want)
	}
	if stats.GitHubXResolved != 1 {
		t.Fatalf("github_x_resolved=%d, want 1", stats.GitHubXResolved)
	}
	if stats.FallbackXExpanded != 0 {
		t.Fatalf("fallback_x_expanded=%d, want 0", stats.FallbackXExpanded)
	}
}

func TestCollectCandidates_GitHubXFallbackDigits(t *testing.T) {
	now := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	mux.HandleFunc("/repos/owner/repo/contents/data/2026_03_03", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})

	urls, stats, err := CollectCandidates(context.Background(), ts.Client(), Options{
		Now:              now,
		GitHubAPIBaseURL: ts.URL,
		ExtraURLs: []string{
			"https://raw.githubusercontent.com/owner/repo/refs/heads/main/data/{Y_m_d}/{x}.yaml",
		},
	})
	if err != nil {
		t.Fatalf("CollectCandidates error: %v", err)
	}
	if len(urls) != 10 {
		t.Fatalf("len(urls)=%d, want 10", len(urls))
	}
	if !strings.HasSuffix(urls[0], "/0.yaml") || !strings.HasSuffix(urls[9], "/9.yaml") {
		t.Fatalf("unexpected fallback urls: first=%q last=%q", urls[0], urls[9])
	}
	if stats.GitHubXErrors != 1 {
		t.Fatalf("github_x_errors=%d, want 1", stats.GitHubXErrors)
	}
	if stats.FallbackXExpanded != 10 {
		t.Fatalf("fallback_x_expanded=%d, want 10", stats.FallbackXExpanded)
	}
}

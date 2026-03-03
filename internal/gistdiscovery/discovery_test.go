package gistdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIsTargetClashFilename(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "all.yaml", want: true},
		{name: "ALL.YAML", want: true},
		{name: "clash.yaml", want: true},
		{name: "my-clash.yaml", want: true},
		{name: "my_clash.yml", want: true},
		{name: "nodes.txt", want: false},
		{name: "clash.txt", want: false},
		{name: "", want: false},
	}

	for _, tc := range cases {
		if got := IsTargetClashFilename(tc.name); got != tc.want {
			t.Fatalf("IsTargetClashFilename(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDiscoverWithMockServer(t *testing.T) {
	mux := http.NewServeMux()
	ts := httptest.NewTLSServer(mux)
	defer ts.Close()

	mux.HandleFunc("/gists/public", func(w http.ResponseWriter, r *http.Request) {
		resp := []map[string]any{
			{
				"id": "g1",
				"files": map[string]any{
					"all.yaml": map[string]any{
						"filename": "all.yaml",
						"raw_url":  ts.URL + "/raw/valid-1",
					},
					"demo-clash.yaml": map[string]any{
						"filename": "demo-clash.yaml",
						"raw_url":  ts.URL + "/raw/valid-2",
					},
					"clash.yaml": map[string]any{
						"filename": "clash.yaml",
						"raw_url":  ts.URL + "/raw/invalid",
					},
				},
			},
			{
				"id": "g2",
				"files": map[string]any{
					"duplicate-clash.yaml": map[string]any{
						"filename": "duplicate-clash.yaml",
						"raw_url":  ts.URL + "/raw/valid-1",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/raw/valid-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("http://user:pass@1.1.1.1:8080#node-1\n"))
	})
	mux.HandleFunc("/raw/valid-2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`proxies:
  - name: ss1
    type: ss
    server: 1.1.1.1
    port: 8388
    cipher: aes-128-gcm
    password: pass
`))
	})
	mux.HandleFunc("/raw/invalid", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a subscription"))
	})

	urls, stats, err := Discover(context.Background(), ts.Client(), Options{
		APIBaseURL:    ts.URL,
		Pages:         1,
		PerPage:       100,
		MinNodes:      1,
		MaxCandidates: 20,
		UserAgent:     "test",
	})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("expected 2 valid urls, got %d (%v)", len(urls), urls)
	}
	if stats.DuplicateURLs != 1 {
		t.Fatalf("duplicate urls = %d, want 1", stats.DuplicateURLs)
	}
	if stats.ParseErrors+stats.TooSmallNodeSets == 0 {
		t.Fatalf("expected parse or too-small rejections > 0")
	}
}

func TestFetchRawTooLarge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/raw/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 20)))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, err := fetchRawContent(context.Background(), ts.Client(), ts.URL+"/raw/big", Options{MaxFileBytes: 8, UserAgent: "test"})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("error = %v, want %v", err, ErrBodyTooLarge)
	}
}

func TestCanonicalizeRawURL(t *testing.T) {
	in := "https://gist.githubusercontent.com/xuefei666/ba24b4e7ecda0a57bfbd65604c63774e/raw/b2177a6a519221f3315e140076f8d06ea28f192d/all.yaml"
	want := "https://gist.githubusercontent.com/xuefei666/ba24b4e7ecda0a57bfbd65604c63774e/raw/all.yaml"

	got := canonicalizeRawURL(in, "all.yaml")
	if got != want {
		t.Fatalf("canonicalizeRawURL() = %q, want %q", got, want)
	}
}

func TestListPublicGistsIncludeSinceQuery(t *testing.T) {
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wantSince := "2026-03-01T00:00:00Z"
	mux.HandleFunc("/gists/public", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			t.Fatalf("parse query failed: %v", err)
		}
		if got := q.Get("since"); got != wantSince {
			t.Fatalf("since query = %q, want %q", got, wantSince)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	})

	_, err := listPublicGists(context.Background(), ts.Client(), Options{
		APIBaseURL: ts.URL,
		PerPage:    10,
		Since:      wantSince,
		UserAgent:  "test",
	}, 1)
	if err != nil {
		t.Fatalf("listPublicGists returned error: %v", err)
	}
}

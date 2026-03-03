package gist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsTargetClashFilename(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "all.yaml", want: true},
		{name: "clash.yaml", want: true},
		{name: "my-clash.yaml", want: true},
		{name: "nodes.txt", want: false},
	}
	for _, tc := range cases {
		if got := IsTargetClashFilename(tc.name); got != tc.want {
			t.Fatalf("IsTargetClashFilename(%q)=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCanonicalizeRawURL(t *testing.T) {
	in := "https://gist.githubusercontent.com/xuefei666/ba24b4e7ecda0a57bfbd65604c63774e/raw/b2177a6a519221f3315e140076f8d06ea28f192d/all.yaml"
	want := "https://gist.githubusercontent.com/xuefei666/ba24b4e7ecda0a57bfbd65604c63774e/raw/all.yaml"
	if got := CanonicalizeRawURL(in, "all.yaml"); got != want {
		t.Fatalf("CanonicalizeRawURL=%q, want %q", got, want)
	}
}

func TestCollectCandidatesFallbackToAnonymous(t *testing.T) {
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	requestCount := 0
	mux.HandleFunc("/gists/public", func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			if r.Header.Get("Authorization") == "" {
				t.Fatalf("first request should include auth")
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("retry should be anonymous")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "g1",
				"files": map[string]any{
					"all.yaml": map[string]any{
						"filename": "all.yaml",
						"raw_url":  "https://gist.githubusercontent.com/u/g/raw/all.yaml",
					},
				},
			},
		})
	})

	urls, stats, err := CollectCandidates(context.Background(), ts.Client(), Options{
		APIBaseURL: ts.URL,
		PerPage:    20,
		Pages:      1,
		Token:      "integration-token",
		UserAgent:  "test",
	})
	if err != nil {
		t.Fatalf("CollectCandidates error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("requestCount=%d, want 2", requestCount)
	}
	if len(urls) != 1 {
		t.Fatalf("len(urls)=%d, want 1", len(urls))
	}
	if stats.CandidateFiles != 1 {
		t.Fatalf("candidate_files=%d, want 1", stats.CandidateFiles)
	}
}

package gist

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsTargetClashFilename(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "all.yaml", want: false},
		{name: "clash.yaml", want: true},
		{name: "my-clash.yaml", want: true},
		{name: "clash_lite.txt", want: true},
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

func TestCollectCandidatesKeywordSearchWithAnonymousRetry(t *testing.T) {
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	gistID := "1c0d9c1679c8df4a35986c21233c1c2a"
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "clash" {
			t.Fatalf("search keyword=%q, want clash", got)
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`<a href="/viertagius/%s">match</a>`, gistID)))
	})

	requestCount := 0
	mux.HandleFunc("/gists/"+gistID, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			if r.Header.Get("Authorization") == "" {
				t.Fatalf("first request should include auth header")
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("retry should be anonymous")
		}
		_, _ = w.Write([]byte(`{
  "id":"` + gistID + `",
  "files":{
    "clash_lite.txt":{
      "filename":"clash_lite.txt",
      "raw_url":"https://gist.githubusercontent.com/viertagius/` + gistID + `/raw/abc/clash_lite.txt"
    }
  }
}`))
	})

	urls, stats, err := CollectCandidates(context.Background(), ts.Client(), Options{
		APIBaseURL:    ts.URL,
		SearchBaseURL: ts.URL + "/search",
		Keyword:       "clash",
		Pages:         1,
		Token:         "integration-token",
		UserAgent:     "test",
	})
	if err != nil {
		t.Fatalf("CollectCandidates error: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("len(urls)=%d, want 1", len(urls))
	}
	want := "https://gist.githubusercontent.com/viertagius/" + gistID + "/raw/clash_lite.txt"
	if urls[0] != want {
		t.Fatalf("url=%q, want %q", urls[0], want)
	}
	if stats.SearchHits != 1 {
		t.Fatalf("search_hits=%d, want 1", stats.SearchHits)
	}
}

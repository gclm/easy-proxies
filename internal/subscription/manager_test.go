package subscription

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestFetchAllSubscriptions_PrefixAndOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sub1":
			_, _ = w.Write([]byte("vmess://node-a#Alpha\n"))
			_, _ = w.Write([]byte("vless://node-b#Beta%20Node\n"))
			_, _ = w.Write([]byte("ss://node-c\n"))
		case "/sub2":
			_, _ = w.Write([]byte("trojan://node-d#Gamma\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Subscriptions: []string{
			server.URL + "/sub1",
			server.URL + "/sub2",
		},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{Timeout: time.Second},
	}

	m := New(cfg, nil)
	nodes, err := m.fetchAllSubscriptions()
	if err != nil {
		t.Fatalf("fetchAllSubscriptions returned error: %v", err)
	}

	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}

	wantNames := []string{
		"[1] Alpha",
		"[1] Beta Node",
		"[1] node-2",
		"[2] Gamma",
	}
	wantURIs := []string{
		"vmess://node-a#Alpha",
		"vless://node-b#Beta%20Node",
		"ss://node-c",
		"trojan://node-d#Gamma",
	}

	for i, node := range nodes {
		if node.Name != wantNames[i] {
			t.Fatalf("node %d name mismatch: want %q, got %q", i, wantNames[i], node.Name)
		}
		if node.URI != wantURIs[i] {
			t.Fatalf("node %d uri mismatch: want %q, got %q", i, wantURIs[i], node.URI)
		}
	}
}

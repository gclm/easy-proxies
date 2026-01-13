package config

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseNodesFromContent_PreservesOrder(t *testing.T) {
	content := "# comment\n" +
		"vmess://first\n" +
		"\n" +
		"invalid://skip\n" +
		"vless://second\n" +
		"  ss://third  \n"

	nodes, err := parseNodesFromContent(content)
	if err != nil {
		t.Fatalf("parseNodesFromContent returned error: %v", err)
	}

	got := make([]string, 0, len(nodes))
	for _, node := range nodes {
		got = append(got, node.URI)
	}
	want := []string{"vmess://first", "vless://second", "ss://third"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed node order mismatch\nwant: %v\n got: %v", want, got)
	}
}

func TestNormalize_SubscriptionPrefixAndFragment(t *testing.T) {
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

	cfg := &Config{
		Subscriptions: []string{
			server.URL + "/sub1",
			server.URL + "/sub2",
		},
		SubscriptionRefresh: SubscriptionRefreshConfig{Timeout: time.Second},
	}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "config.yaml"))

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}

	if len(cfg.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(cfg.Nodes))
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

	for i, node := range cfg.Nodes {
		if node.Name != wantNames[i] {
			t.Fatalf("node %d name mismatch: want %q, got %q", i, wantNames[i], node.Name)
		}
		if node.URI != wantURIs[i] {
			t.Fatalf("node %d uri mismatch: want %q, got %q", i, wantURIs[i], node.URI)
		}
	}
}

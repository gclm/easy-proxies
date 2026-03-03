package config

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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

func TestSaveSubscriptions_PreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := "" +
		"mode: pool\n" +
		"listener:\n" +
		"  address: 127.0.0.1\n" +
		"  port: 2323\n" +
		"nodes_file: nodes.txt\n" +
		"subscriptions:\n" +
		"  - https://old.example.com/sub\n" +
		"external_ip: \"1.2.3.4\"\n"

	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}

	cfg := &Config{Subscriptions: []string{"https://new.example.com/sub1", "https://new.example.com/sub2"}}
	cfg.SetFilePath(path)

	if err := cfg.SaveSubscriptions(); err != nil {
		t.Fatalf("SaveSubscriptions returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	if got["mode"] != "pool" {
		t.Fatalf("mode changed: got %v", got["mode"])
	}
	if got["nodes_file"] != "nodes.txt" {
		t.Fatalf("nodes_file changed: got %v", got["nodes_file"])
	}
	if got["external_ip"] != "1.2.3.4" {
		t.Fatalf("external_ip changed: got %v", got["external_ip"])
	}

	listener, ok := got["listener"].(map[string]any)
	if !ok {
		// yaml.v3 may decode to map[string]interface{} depending on Go version; handle both.
		if m, ok2 := got["listener"].(map[string]interface{}); ok2 {
			listener = make(map[string]any, len(m))
			for k, v := range m {
				listener[k] = v
			}
			ok = true
		}
	}
	if !ok {
		t.Fatalf("listener type unexpected: %T", got["listener"])
	}
	if listener["address"] != "127.0.0.1" {
		t.Fatalf("listener.address changed: got %v", listener["address"])
	}
	if listener["port"] != 2323 {
		t.Fatalf("listener.port changed: got %v", listener["port"])
	}

	subsRaw, ok := got["subscriptions"].([]any)
	if !ok {
		if s2, ok2 := got["subscriptions"].([]interface{}); ok2 {
			subsRaw = make([]any, len(s2))
			for i := range s2 {
				subsRaw[i] = s2[i]
			}
			ok = true
		}
	}
	if !ok {
		t.Fatalf("subscriptions type unexpected: %T", got["subscriptions"])
	}
	subs := make([]string, 0, len(subsRaw))
	for _, v := range subsRaw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("subscription item type unexpected: %T", v)
		}
		subs = append(subs, s)
	}
	want := []string{"https://new.example.com/sub1", "https://new.example.com/sub2"}
	if !reflect.DeepEqual(subs, want) {
		t.Fatalf("subscriptions mismatch\nwant: %v\n got: %v", want, subs)
	}
}

func TestSubscriptionCRUD_ValidatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Minimal but valid mapping config.
	if err := os.WriteFile(path, []byte("mode: pool\n"), 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}

	cfg := &Config{}
	cfg.SetFilePath(path)

	if _, err := cfg.AddSubscription("not-a-url"); err == nil {
		t.Fatalf("expected invalid url error")
	} else if !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("expected ErrInvalidSubscription, got %v", err)
	}

	idx, err := cfg.AddSubscription("https://example.com/sub")
	if err != nil {
		t.Fatalf("AddSubscription returned error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("unexpected index: %d", idx)
	}

	if _, err := cfg.AddSubscription("https://example.com/sub"); err == nil {
		t.Fatalf("expected duplicate error")
	} else if !errors.Is(err, ErrSubscriptionDuplicate) {
		t.Fatalf("expected ErrSubscriptionDuplicate, got %v", err)
	}

	if err := cfg.UpdateSubscription(1, "https://example.com/other"); err == nil {
		t.Fatalf("expected not found error")
	} else if !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("expected ErrSubscriptionNotFound, got %v", err)
	}

	if err := cfg.UpdateSubscription(0, "https://example.com/other"); err != nil {
		t.Fatalf("UpdateSubscription returned error: %v", err)
	}

	if err := cfg.DeleteSubscription(0); err != nil {
		t.Fatalf("DeleteSubscription returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	if got["mode"] != "pool" {
		t.Fatalf("mode changed: got %v", got["mode"])
	}
	if subs, ok := got["subscriptions"]; ok && subs != nil {
		// After deletion we still expect an empty list (not a scalar).
		if v, ok := subs.([]interface{}); ok && len(v) != 0 {
			t.Fatalf("expected empty subscriptions, got %v", subs)
		}
	}
}

func TestParseSubscriptionContent_ClashYAMLProxiesAfter200Bytes(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "preface_key_%d: value_%d\n", i, i)
	}
	b.WriteString("" +
		"proxies:\n" +
		"  - name: test-ss\n" +
		"    type: ss\n" +
		"    server: 1.1.1.1\n" +
		"    port: 8388\n" +
		"    cipher: aes-128-gcm\n" +
		"    password: pass\n")

	nodes, err := ParseSubscriptionContent(b.String())
	if err != nil {
		t.Fatalf("ParseSubscriptionContent returned error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
}

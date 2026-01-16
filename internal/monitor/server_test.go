package monitor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleExport_SurgeKeepsSubscriptionPrefixAndOrder(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	// Register out of order to ensure SnapshotFiltered's Index sorting is respected.
	mgr.Register(NodeInfo{Tag: "gamma", Name: "[2] Gamma", ListenAddress: "1.2.3.4", Port: 29002, Index: 2})
	mgr.Register(NodeInfo{Tag: "beta", Name: "[1] Beta", ListenAddress: "1.2.3.4", Port: 29001, Index: 1})
	mgr.Register(NodeInfo{Tag: "alpha", Name: "[1] Alpha", ListenAddress: "1.2.3.4", Port: 29000, Index: 0})

	s := &Server{
		cfg: Config{
			InboundType:   "http",
			ProxyUsername: "user",
			ProxyPassword: "pass",
		},
		mgr: mgr,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/export?format=surge", nil)
	w := httptest.NewRecorder()
	s.handleExport(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: want %d, got %d", http.StatusOK, resp.StatusCode)
	}

	body := strings.TrimSpace(w.Body.String())
	lines := strings.Split(body, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), body)
	}

	wantOrder := []string{"[1] Alpha", "[1] Beta", "[2] Gamma"}
	for i := range wantOrder {
		if !strings.Contains(lines[i], wantOrder[i]) {
			t.Fatalf("line %d should contain node name %q, got %q", i, wantOrder[i], lines[i])
		}
	}
}

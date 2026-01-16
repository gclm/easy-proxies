package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

// Logger defines logging interface.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// Option configures the Manager.
type Option func(*Manager)

// WithLogger sets a custom logger.
func WithLogger(l Logger) Option {
	return func(m *Manager) { m.logger = l }
}

// Manager handles periodic subscription refresh.
type Manager struct {
	mu sync.RWMutex

	baseCfg    *config.Config
	boxMgr     *boxmgr.Manager
	logger     Logger
	httpClient *http.Client // Custom HTTP client with connection pooling

	status        monitor.SubscriptionStatus
	ctx           context.Context
	cancel        context.CancelFunc
	refreshMu     sync.Mutex // prevents concurrent refreshes
	manualRefresh chan struct{}

	// Track nodes.txt content hash to detect modifications
	lastSubHash      string    // Hash of nodes.txt content after last subscription refresh
	lastNodesModTime time.Time // Last known modification time of nodes.txt

	// Cached active nodes by subscription index (0-based). This represents the currently applied
	// subscription nodes set, used to support per-subscription refresh without re-fetching others.
	subNodesByIndex [][]config.NodeConfig
	subURLsSnapshot []string
}

// New creates a SubscriptionManager.
func New(cfg *config.Config, boxMgr *boxmgr.Manager, opts ...Option) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	// Create optimized HTTP client with connection pooling
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second, // Overall timeout
	}

	m := &Manager{
		baseCfg:       cfg,
		boxMgr:        boxMgr,
		ctx:           ctx,
		cancel:        cancel,
		manualRefresh: make(chan struct{}, 1),
		httpClient:    httpClient,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.logger == nil {
		m.logger = defaultLogger{}
	}
	return m
}

// Start begins the periodic refresh loop.
func (m *Manager) Start() {
	if !m.baseCfg.SubscriptionRefresh.Enabled {
		m.logger.Infof("subscription refresh disabled")
		return
	}
	if len(m.baseCfg.Subscriptions) == 0 {
		m.logger.Infof("no subscriptions configured, refresh disabled")
		return
	}

	interval := m.baseCfg.SubscriptionRefresh.Interval
	m.logger.Infof("starting subscription refresh, interval: %s", interval)

	go m.refreshLoop(interval)
}

// Stop stops the periodic refresh.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	// Close idle connections
	if m.httpClient != nil {
		m.httpClient.CloseIdleConnections()
	}
}

// RefreshNow triggers an immediate refresh.
func (m *Manager) RefreshNow() error {
	select {
	case m.manualRefresh <- struct{}{}:
	default:
		// Already a refresh pending
	}

	// Wait for refresh to complete or timeout
	timeout := m.baseCfg.SubscriptionRefresh.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(m.ctx, timeout+m.baseCfg.SubscriptionRefresh.HealthCheckTimeout)
	defer cancel()

	// Poll status until refresh completes
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	startCount := m.Status().RefreshCount
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("refresh timeout")
		case <-ticker.C:
			status := m.Status()
			if status.RefreshCount > startCount {
				if status.LastError != "" {
					return fmt.Errorf("refresh failed: %s", status.LastError)
				}
				return nil
			}
		}
	}
}

// Status returns the current refresh status.
func (m *Manager) Status() monitor.SubscriptionStatus {
	m.mu.RLock()
	status := m.status
	m.mu.RUnlock()

	// Check if nodes have been modified since last refresh
	status.NodesModified = m.CheckNodesModified()
	return status
}

// refreshLoop runs the periodic refresh.
func (m *Manager) refreshLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Update next refresh time
	m.mu.Lock()
	m.status.NextRefresh = time.Now().Add(interval)
	m.mu.Unlock()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.doRefresh()
			m.mu.Lock()
			m.status.NextRefresh = time.Now().Add(interval)
			m.mu.Unlock()
		case <-m.manualRefresh:
			m.doRefresh()
			// Reset ticker after manual refresh
			ticker.Reset(interval)
			m.mu.Lock()
			m.status.NextRefresh = time.Now().Add(interval)
			m.mu.Unlock()
		}
	}
}

// doRefresh performs a single refresh operation.
func (m *Manager) doRefresh() {
	// Prevent concurrent refreshes
	if !m.refreshMu.TryLock() {
		m.logger.Warnf("refresh already in progress, skipping")
		return
	}
	defer m.refreshMu.Unlock()

	m.mu.Lock()
	m.status.IsRefreshing = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.status.IsRefreshing = false
		m.status.RefreshCount++
		m.mu.Unlock()
	}()

	m.logger.Infof("starting subscription refresh")

	// Fetch nodes from all subscriptions
	nodes, err := m.fetchAllSubscriptions()
	if err != nil {
		m.logger.Errorf("fetch subscriptions failed: %v", err)
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return
	}

	if len(nodes) == 0 {
		m.logger.Warnf("no nodes fetched from subscriptions")
		m.mu.Lock()
		m.status.LastError = "no nodes fetched"
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return
	}

	m.logger.Infof("fetched %d nodes from subscriptions", len(nodes))

	// Write subscription nodes to nodes.txt
	nodesFilePath := m.getNodesFilePath()
	if err := m.writeNodesToFile(nodesFilePath, nodes); err != nil {
		m.logger.Errorf("failed to write nodes.txt: %v", err)
		m.mu.Lock()
		m.status.LastError = fmt.Sprintf("write nodes.txt: %v", err)
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return
	}
	m.logger.Infof("written %d nodes to %s", len(nodes), nodesFilePath)

	// Update hash and mod time after writing
	newHash := m.computeNodesHash(nodes)
	m.mu.Lock()
	m.lastSubHash = newHash
	if info, err := os.Stat(nodesFilePath); err == nil {
		m.lastNodesModTime = info.ModTime()
	} else {
		m.lastNodesModTime = time.Now()
	}
	m.status.NodesModified = false
	m.mu.Unlock()

	// Get current port mapping to preserve existing node ports
	portMap := m.boxMgr.CurrentPortMap()

	// Create new config with updated nodes
	newCfg := m.createNewConfig(nodes)

	// Trigger BoxManager reload with port preservation
	if err := m.boxMgr.ReloadWithPortMap(newCfg, portMap); err != nil {
		m.logger.Errorf("reload failed: %v", err)
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	m.status.LastRefresh = time.Now()
	m.status.NodeCount = len(nodes)
	m.status.LastError = ""
	m.mu.Unlock()

	m.logger.Infof("subscription refresh completed, %d nodes active", len(nodes))
}

// FetchSubscriptionNodes returns the currently cached (active) nodes for a single subscription index.
// If the subscription has never been refreshed (and cannot be inferred from startup config),
// it returns an empty slice.
func (m *Manager) FetchSubscriptionNodes(index int) ([]config.NodeConfig, error) {
	subs := m.getSubscriptions()
	if index < 0 || index >= len(subs) {
		return nil, fmt.Errorf("subscription index out of range")
	}

	m.mu.Lock()
	m.ensureSubscriptionCacheLocked(subs)
	nodes := cloneNodeSlice(m.subNodesByIndex[index])
	m.mu.Unlock()
	return nodes, nil
}

// RefreshSubscription refreshes a single subscription by index, updates nodes.txt, and reloads sing-box
// while preserving existing port assignments. It returns the number of nodes fetched from that subscription.
func (m *Manager) RefreshSubscription(index int) (int, error) {
	if m.boxMgr == nil {
		return 0, fmt.Errorf("box manager unavailable")
	}

	// Prevent concurrent refreshes (full or per-sub).
	if !m.refreshMu.TryLock() {
		return 0, fmt.Errorf("refresh already in progress")
	}
	defer m.refreshMu.Unlock()

	subs := m.getSubscriptions()
	if index < 0 || index >= len(subs) {
		return 0, fmt.Errorf("subscription index out of range")
	}

	m.mu.Lock()
	m.status.IsRefreshing = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.status.IsRefreshing = false
		m.status.RefreshCount++
		m.mu.Unlock()
	}()

	timeout := m.baseCfg.SubscriptionRefresh.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	subURL := subs[index]
	nodes, err := m.fetchSubscription(subURL, timeout)
	if err != nil {
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return 0, err
	}

	// Apply name extraction + subscription prefix so node keys stay stable for port preservation.
	prefix := fmt.Sprintf("[%d] ", index+1)
	for j := range nodes {
		if nodes[j].Name == "" {
			if parsed, err := url.Parse(nodes[j].URI); err == nil && parsed.Fragment != "" {
				if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
					nodes[j].Name = decoded
				} else {
					nodes[j].Name = parsed.Fragment
				}
			}
		}
		if nodes[j].Name == "" {
			nodes[j].Name = fmt.Sprintf("node-%d", j)
		}
		nodes[j].Name = prefix + nodes[j].Name
	}

	// Update cached active nodes for this subscription, then merge all cached nodes in order.
	m.mu.Lock()
	m.ensureSubscriptionCacheLocked(subs)
	m.subNodesByIndex[index] = cloneNodeSlice(nodes)
	merged := m.mergeCachedSubscriptionNodesLocked()
	m.mu.Unlock()

	if len(merged) == 0 {
		err := fmt.Errorf("no nodes available after refresh")
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return 0, err
	}

	// Write subscription nodes to nodes.txt
	nodesFilePath := m.getNodesFilePath()
	if err := m.writeNodesToFile(nodesFilePath, merged); err != nil {
		m.mu.Lock()
		m.status.LastError = fmt.Sprintf("write nodes.txt: %v", err)
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return 0, err
	}

	// Update hash and mod time after writing
	newHash := m.computeNodesHash(merged)
	m.mu.Lock()
	m.lastSubHash = newHash
	if info, err := os.Stat(nodesFilePath); err == nil {
		m.lastNodesModTime = info.ModTime()
	} else {
		m.lastNodesModTime = time.Now()
	}
	m.status.NodesModified = false
	m.mu.Unlock()

	// Preserve existing port mapping.
	portMap := m.boxMgr.CurrentPortMap()
	newCfg := m.createNewConfig(merged)
	if err := m.boxMgr.ReloadWithPortMap(newCfg, portMap); err != nil {
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return 0, err
	}

	m.mu.Lock()
	m.status.LastRefresh = time.Now()
	m.status.NodeCount = len(merged)
	m.status.LastError = ""
	m.mu.Unlock()

	return len(nodes), nil
}

// getNodesFilePath returns the path to nodes.txt.
func (m *Manager) getNodesFilePath() string {
	if m.baseCfg.NodesFile != "" {
		return m.baseCfg.NodesFile
	}
	return filepath.Join(filepath.Dir(m.baseCfg.FilePath()), "nodes.txt")
}

// writeNodesToFile writes nodes to a file (one URI per line).
func (m *Manager) writeNodesToFile(path string, nodes []config.NodeConfig) error {
	var lines []string
	for _, node := range nodes {
		lines = append(lines, node.URI)
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// computeNodesHash computes a hash of node URIs for change detection.
func (m *Manager) computeNodesHash(nodes []config.NodeConfig) string {
	var uris []string
	for _, node := range nodes {
		uris = append(uris, node.URI)
	}
	content := strings.Join(uris, "\n")
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

// CheckNodesModified checks if nodes.txt has been modified since last refresh.
// Uses file modification time as a fast path to avoid unnecessary file reads.
func (m *Manager) CheckNodesModified() bool {
	m.mu.RLock()
	lastHash := m.lastSubHash
	lastMod := m.lastNodesModTime
	m.mu.RUnlock()

	if lastHash == "" {
		return false // No previous refresh, can't determine modification
	}

	nodesFilePath := m.getNodesFilePath()

	// Fast path: check modification time first
	info, err := os.Stat(nodesFilePath)
	if err != nil {
		return false // File doesn't exist or can't stat
	}
	modTime := info.ModTime()
	if !modTime.After(lastMod) {
		return false // File hasn't been modified
	}

	// Slow path: file was modified, compute hash
	data, err := os.ReadFile(nodesFilePath)
	if err != nil {
		return false // File doesn't exist or can't read
	}

	// Parse nodes from file content
	var nodes []config.NodeConfig
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if config.IsProxyURI(line) {
			nodes = append(nodes, config.NodeConfig{URI: line})
		}
	}

	currentHash := m.computeNodesHash(nodes)
	changed := currentHash != lastHash

	// Update cached mod time
	m.mu.Lock()
	m.lastNodesModTime = modTime
	m.mu.Unlock()

	return changed
}

// MarkNodesModified updates the modification status.
func (m *Manager) MarkNodesModified() {
	m.mu.Lock()
	m.status.NodesModified = true
	m.mu.Unlock()
}

func (m *Manager) getSubscriptions() []string {
	// Copy slice to avoid external mutation during iteration.
	if m.baseCfg == nil || len(m.baseCfg.Subscriptions) == 0 {
		return nil
	}
	return append([]string(nil), m.baseCfg.Subscriptions...)
}

func cloneNodeSlice(in []config.NodeConfig) []config.NodeConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.NodeConfig, len(in))
	copy(out, in)
	return out
}

func parseSubscriptionPrefixIndex(name string) (int, bool) {
	// Expected format: "[<1-based-index>] ..."
	if !strings.HasPrefix(name, "[") {
		return -1, false
	}
	end := strings.Index(name, "]")
	if end <= 1 {
		return -1, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(name[1:end]))
	if err != nil || n <= 0 {
		return -1, false
	}
	return n - 1, true
}

// ensureSubscriptionCacheLocked initializes and reconciles the per-subscription active nodes cache.
// m.mu must be held for writing.
func (m *Manager) ensureSubscriptionCacheLocked(subs []string) {
	if m.subNodesByIndex == nil {
		m.subNodesByIndex = make([][]config.NodeConfig, len(subs))
		m.subURLsSnapshot = append([]string(nil), subs...)

		// Best-effort initialization from startup config nodes.
		for _, node := range m.baseCfg.Nodes {
			idx, ok := parseSubscriptionPrefixIndex(node.Name)
			if !ok || idx < 0 || idx >= len(subs) {
				continue
			}
			m.subNodesByIndex[idx] = append(m.subNodesByIndex[idx], node)
		}
		return
	}

	oldSubs := m.subURLsSnapshot
	oldNodes := m.subNodesByIndex

	newNodes := make([][]config.NodeConfig, len(subs))
	usedOld := make([]bool, len(oldSubs))
	oldIndexByURL := make(map[string]int, len(oldSubs))
	for i, u := range oldSubs {
		oldIndexByURL[u] = i
	}

	// First pass: preserve nodes by matching URLs (handles deletes/reorders).
	for i, u := range subs {
		if j, ok := oldIndexByURL[u]; ok && j >= 0 && j < len(oldNodes) {
			newNodes[i] = oldNodes[j]
			usedOld[j] = true
		}
	}

	// Second pass: for remaining unmatched slots, preserve by index when possible (handles updates).
	for i := range subs {
		if newNodes[i] != nil {
			continue
		}
		if i < len(oldNodes) && !usedOld[i] {
			newNodes[i] = oldNodes[i]
			usedOld[i] = true
		}
	}

	m.subNodesByIndex = newNodes
	m.subURLsSnapshot = append([]string(nil), subs...)
}

// mergeCachedSubscriptionNodesLocked merges cached nodes from all subscriptions in order.
// m.mu must be held for writing.
func (m *Manager) mergeCachedSubscriptionNodesLocked() []config.NodeConfig {
	var merged []config.NodeConfig
	for i := range m.subNodesByIndex {
		if len(m.subNodesByIndex[i]) == 0 {
			continue
		}
		merged = append(merged, m.subNodesByIndex[i]...)
	}
	return merged
}

// fetchAllSubscriptions fetches nodes from all configured subscription URLs.
func (m *Manager) fetchAllSubscriptions() ([]config.NodeConfig, error) {
	var allNodes []config.NodeConfig
	var lastErr error

	timeout := m.baseCfg.SubscriptionRefresh.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	subs := m.getSubscriptions()
	for i, subURL := range subs {
		nodes, err := m.fetchSubscription(subURL, timeout)
		if err != nil {
			m.logger.Warnf("failed to fetch %s: %v", subURL, err)
			lastErr = err
			// Keep cache consistent with what will be applied: drop nodes for this subscription on failure.
			m.mu.Lock()
			m.ensureSubscriptionCacheLocked(subs)
			if i >= 0 && i < len(m.subNodesByIndex) {
				m.subNodesByIndex[i] = nil
			}
			m.mu.Unlock()
			continue
		}
		// 先从 URI fragment 提取名称，再添加订阅前缀
		prefix := fmt.Sprintf("[%d] ", i+1)
		for j := range nodes {
			// 从 URI fragment 提取名称
			if nodes[j].Name == "" {
				if parsed, err := url.Parse(nodes[j].URI); err == nil && parsed.Fragment != "" {
					if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
						nodes[j].Name = decoded
					} else {
						nodes[j].Name = parsed.Fragment
					}
				}
			}
			if nodes[j].Name == "" {
				nodes[j].Name = fmt.Sprintf("node-%d", j)
			}
			nodes[j].Name = prefix + nodes[j].Name
		}
		m.logger.Infof("fetched %d nodes from subscription %d", len(nodes), i+1)
		allNodes = append(allNodes, nodes...)

		// Update cache for per-subscription refresh support.
		m.mu.Lock()
		m.ensureSubscriptionCacheLocked(subs)
		if i >= 0 && i < len(m.subNodesByIndex) {
			m.subNodesByIndex[i] = cloneNodeSlice(nodes)
		}
		m.mu.Unlock()
	}

	if len(allNodes) == 0 && lastErr != nil {
		return nil, lastErr
	}

	return allNodes, nil
}

// fetchSubscription fetches and parses a single subscription URL.
func (m *Manager) fetchSubscription(subURL string, timeout time.Duration) ([]config.NodeConfig, error) {
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", subURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "*/*")

	// Use custom HTTP client with connection pooling
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	// Limit read size to prevent memory exhaustion
	const maxBodySize = 10 * 1024 * 1024 // 10MB
	limitedReader := io.LimitReader(resp.Body, maxBodySize)

	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return config.ParseSubscriptionContent(string(body))
}

// createNewConfig creates a new config with updated nodes while preserving other settings.
func (m *Manager) createNewConfig(nodes []config.NodeConfig) *config.Config {
	// Deep copy base config
	newCfg := *m.baseCfg

	// Assign port numbers to nodes in multi-port mode
	if newCfg.Mode == "multi-port" {
		portCursor := newCfg.MultiPort.BasePort
		for i := range nodes {
			nodes[i].Port = portCursor
			portCursor++
			// Apply default credentials
			if nodes[i].Username == "" {
				nodes[i].Username = newCfg.MultiPort.Username
				nodes[i].Password = newCfg.MultiPort.Password
			}
		}
	}

	// Process node names
	for i := range nodes {
		nodes[i].Name = strings.TrimSpace(nodes[i].Name)
		nodes[i].URI = strings.TrimSpace(nodes[i].URI)

		// Extract name from URI fragment if not provided
		if nodes[i].Name == "" {
			if parsed, err := url.Parse(nodes[i].URI); err == nil && parsed.Fragment != "" {
				if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
					nodes[i].Name = decoded
				} else {
					nodes[i].Name = parsed.Fragment
				}
			}
		}
		if nodes[i].Name == "" {
			nodes[i].Name = fmt.Sprintf("node-%d", i)
		}
	}

	newCfg.Nodes = nodes
	return &newCfg
}

type defaultLogger struct{}

func (defaultLogger) Infof(format string, args ...any) {
	log.Printf("[subscription] "+format, args...)
}

func (defaultLogger) Warnf(format string, args ...any) {
	log.Printf("[subscription] WARN: "+format, args...)
}

func (defaultLogger) Errorf(format string, args ...any) {
	log.Printf("[subscription] ERROR: "+format, args...)
}

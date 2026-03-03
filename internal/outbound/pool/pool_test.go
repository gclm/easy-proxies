package pool

import (
	"reflect"
	"testing"
	"time"

	"easy_proxies/internal/monitor"
)

func TestAvailableMembersLockedSkipsUnavailableWhenHealthCheckEnabled(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("create monitor manager: %v", err)
	}

	healthy := mgr.Register(monitor.NodeInfo{Tag: "healthy"})
	healthy.MarkInitialCheckDone(true)

	failed := mgr.Register(monitor.NodeInfo{Tag: "failed"})
	failed.MarkInitialCheckDone(false)

	unknown := mgr.Register(monitor.NodeInfo{Tag: "unknown"})

	p := &poolOutbound{
		options: Options{DisableHealthCheck: false},
		members: []*memberState{
			{tag: "healthy", entry: healthy, shared: acquireSharedState("healthy")},
			{tag: "failed", entry: failed, shared: acquireSharedState("failed")},
			{tag: "unknown", entry: unknown, shared: acquireSharedState("unknown")},
		},
	}

	result := p.availableMembersLocked(time.Now(), "", make([]*memberState, 0, 3))
	gotTags := make([]string, 0, len(result))
	for _, member := range result {
		gotTags = append(gotTags, member.tag)
	}

	wantTags := []string{"healthy", "unknown"}
	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Fatalf("available tags = %v, want %v", gotTags, wantTags)
	}
}

func TestAvailableMembersLockedKeepsUnavailableWhenHealthCheckDisabled(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("create monitor manager: %v", err)
	}

	healthy := mgr.Register(monitor.NodeInfo{Tag: "healthy"})
	healthy.MarkInitialCheckDone(true)

	failed := mgr.Register(monitor.NodeInfo{Tag: "failed"})
	failed.MarkInitialCheckDone(false)

	p := &poolOutbound{
		options: Options{DisableHealthCheck: true},
		members: []*memberState{
			{tag: "healthy", entry: healthy, shared: acquireSharedState("healthy")},
			{tag: "failed", entry: failed, shared: acquireSharedState("failed")},
		},
	}

	result := p.availableMembersLocked(time.Now(), "", make([]*memberState, 0, 2))
	gotTags := make([]string, 0, len(result))
	for _, member := range result {
		gotTags = append(gotTags, member.tag)
	}

	wantTags := []string{"healthy", "failed"}
	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Fatalf("available tags = %v, want %v", gotTags, wantTags)
	}
}

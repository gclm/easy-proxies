package builder

import (
	"reflect"
	"testing"
)

func TestRegionSelectorUsernames(t *testing.T) {
	got := regionSelectorUsernames("proxy", "US")
	want := []string{"proxy-us", "proxy_us"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regionSelectorUsernames() = %v, want %v", got, want)
	}
}

func TestRegionSelectorUsernamesEmptyInput(t *testing.T) {
	if got := regionSelectorUsernames("", "us"); got != nil {
		t.Fatalf("expected nil for empty username, got %v", got)
	}
	if got := regionSelectorUsernames("proxy", ""); got != nil {
		t.Fatalf("expected nil for empty region, got %v", got)
	}
}

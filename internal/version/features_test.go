package version

import (
	"reflect"
	"testing"
)

func TestFeatureList(t *testing.T) {
	tests := []struct {
		name  string
		flags string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "feat-a", []string{"feat-a"}},
		{"multiple", "feat-a,feat-b, feat-c", []string{"feat-a", "feat-b", "feat-c"}},
		{"with empty parts", ",feat-a,,", []string{"feat-a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			FeatureFlags = tt.flags
			got := FeatureList()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FeatureList() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasFeature(t *testing.T) {
	FeatureFlags = "rework-deferred-throttle,hooked-polecats-working"
	if !HasFeature("rework-deferred-throttle") {
		t.Error("expected HasFeature(rework-deferred-throttle) = true")
	}
	if HasFeature("missing") {
		t.Error("expected HasFeature(missing) = false")
	}
}

func TestIsPinnedRuntime(t *testing.T) {
	PinnedRuntimeLine = "1.2.0"
	if !IsPinnedRuntime("1.2.0") {
		t.Error("expected IsPinnedRuntime(1.2.0) = true")
	}
	if IsPinnedRuntime("1.2.1") {
		t.Error("expected IsPinnedRuntime(1.2.1) = false")
	}
	PinnedRuntimeLine = ""
	if IsPinnedRuntime("1.2.0") {
		t.Error("expected IsPinnedRuntime with empty line = false")
	}
}

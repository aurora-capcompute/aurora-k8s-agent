package main

import "testing"

func TestSourceKinds(t *testing.T) {
	cases := []struct {
		name    string
		sources string
		channel string
		want    []string
	}{
		{"default telegram", "", "", []string{"telegram"}},
		{"explicit list", "telegram,slack", "", []string{"telegram", "slack"}},
		{"none disables chat", "none", "", nil},
		{"dedupe and trim", "slack, slack ,telegram", "", []string{"slack", "telegram"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AURORA_SOURCES", tc.sources)
			t.Setenv("AURORA_CHANNEL", tc.channel)
			got := sourceKinds()
			if len(got) != len(tc.want) {
				t.Fatalf("sourceKinds() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sourceKinds() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestControlPlaneKind(t *testing.T) {
	t.Run("default none", func(t *testing.T) {
		t.Setenv("AURORA_CONTROL_PLANE", "")
		t.Setenv("AURORA_CONTROLLER", "")
		if got := controlPlaneKind(); got != "none" {
			t.Fatalf("controlPlaneKind() = %q, want none", got)
		}
	})
	t.Run("explicit fs", func(t *testing.T) {
		t.Setenv("AURORA_CONTROL_PLANE", "fs")
		if got := controlPlaneKind(); got != "fs" {
			t.Fatalf("controlPlaneKind() = %q, want fs", got)
		}
	})
	t.Run("legacy controller flag", func(t *testing.T) {
		t.Setenv("AURORA_CONTROL_PLANE", "")
		t.Setenv("AURORA_CONTROLLER", "true")
		if got := controlPlaneKind(); got != "k8s" {
			t.Fatalf("controlPlaneKind() = %q, want k8s", got)
		}
	})
}

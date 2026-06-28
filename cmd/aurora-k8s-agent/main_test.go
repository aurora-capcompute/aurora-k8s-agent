package main

import "testing"

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

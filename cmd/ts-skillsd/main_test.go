package main

import "testing"

func TestDevModeFromEnv(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true} {
		t.Setenv("TS_SKILLSD_DEV", value)
		enabled, err := devModeFromEnv()
		if err != nil {
			t.Fatalf("devModeFromEnv(%q): %v", value, err)
		}
		if enabled != want {
			t.Errorf("devModeFromEnv(%q) = %v, want %v", value, enabled, want)
		}
	}
	t.Setenv("TS_SKILLSD_DEV", "maybe")
	if _, err := devModeFromEnv(); err == nil {
		t.Error("devModeFromEnv accepted a non-boolean value")
	}
}

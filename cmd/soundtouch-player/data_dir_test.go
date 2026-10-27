package main

import (
	"path/filepath"
	"testing"
)

func TestPlayerDataDirectoryIsExplicitOrUserScoped(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	got, err := playerDataDirectory("")
	if err != nil || got != filepath.Join(base, "aftertouch-player") {
		t.Fatalf("default=%q %v", got, err)
	}
	explicit := filepath.Join(base, "separate-instance")
	got, err = playerDataDirectory(explicit)
	if err != nil || got != explicit {
		t.Fatalf("explicit=%q %v", got, err)
	}
}

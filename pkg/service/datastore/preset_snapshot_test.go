package datastore

import (
	"path/filepath"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
)

func TestReadPresetSnapshotStates(t *testing.T) {
	account := "1234567"
	device := "DEVICE01"

	tests := []struct {
		name  string
		write []byte
		want  PresetSnapshotState
	}{
		{name: "missing", want: PresetSnapshotMissing},
		{name: "empty", write: []byte(" \n"), want: PresetSnapshotEmpty},
		{name: "malformed", write: []byte("<presets>"), want: PresetSnapshotMalformed},
		{name: "valid empty", write: []byte("<presets></presets>"), want: PresetSnapshotValid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := NewDataStore(t.TempDir())
			if tt.write != nil {
				path := filepath.Join(ds.AccountDeviceDir(account, device), constants.PresetsFile)
				if err := ds.MkdirAllUnderBase(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAllUnderBase: %v", err)
				}
				if err := ds.WriteFileUnderBase(path, tt.write, 0o644); err != nil {
					t.Fatalf("WriteFileUnderBase: %v", err)
				}
			}

			snapshot, err := ds.ReadPresetSnapshot(account, device)
			if err != nil {
				t.Fatalf("ReadPresetSnapshot: %v", err)
			}
			if snapshot.State != tt.want {
				t.Fatalf("state = %q, want %q", snapshot.State, tt.want)
			}
			if tt.want == PresetSnapshotValid && len(snapshot.Presets) != 0 {
				t.Fatalf("valid empty snapshot returned %d presets", len(snapshot.Presets))
			}
		})
	}
}

func TestReadPresetSnapshotReturnsPersistedPresets(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	account := "1234567"
	device := "DEVICE01"
	want := models.ServicePreset{
		ServiceContentItem: models.ServiceContentItem{Name: "Radio", Location: "http://radio.example/stream"},
		ID:                 "1",
		ButtonNumber:       "1",
	}

	if err := ds.SavePresets(account, device, []models.ServicePreset{want}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}

	snapshot, err := ds.ReadPresetSnapshot(account, device)
	if err != nil {
		t.Fatalf("ReadPresetSnapshot: %v", err)
	}
	if snapshot.State != PresetSnapshotValid {
		t.Fatalf("state = %q, want %q", snapshot.State, PresetSnapshotValid)
	}
	if len(snapshot.Presets) != 1 || snapshot.Presets[0].Name != want.Name || snapshot.Presets[0].Location != want.Location {
		t.Fatalf("presets = %+v, want Radio at %s", snapshot.Presets, want.Location)
	}
}

package setup

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
)

const (
	readinessAccount = "1234567"
	readinessDevice  = "DEVICE01"
)

func newMigrationReadinessFixture(t *testing.T, livePresetsXML string) (*Manager, *datastore.DataStore, string) {
	t.Helper()

	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			_, _ = fmt.Fprintf(w, `<info deviceID="%s"><name>Test Speaker</name><margeAccountUUID>%s</margeAccountUUID></info>`, readinessDevice, readinessAccount)
		case "/presets":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(livePresetsXML))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(speaker.Close)

	ds := datastore.NewDataStore(t.TempDir())
	deviceIP := strings.TrimPrefix(speaker.URL, "http://")
	if err := ds.SaveDeviceInfo(readinessAccount, readinessDevice, &models.ServiceDeviceInfo{
		DeviceID:  readinessDevice,
		AccountID: readinessAccount,
		IPAddress: deviceIP,
		Name:      "Test Speaker",
	}); err != nil {
		t.Fatalf("SaveDeviceInfo: %v", err)
	}

	return NewManager("http://aftertouch.example:8000", ds, nil), ds, deviceIP
}

func readinessPreset(slot, name, location string) models.ServicePreset {
	return models.ServicePreset{
		ServiceContentItem: models.ServiceContentItem{
			Name:            name,
			Source:          "LOCAL_INTERNET_RADIO",
			Type:            "stationurl",
			ContentItemType: "stationurl",
			Location:        location,
			SourceID:        "10003",
			IsPresetable:    "true",
		},
		ID:           slot,
		ButtonNumber: slot,
	}
}

func livePresetsXML(presets ...models.ServicePreset) string {
	var xml strings.Builder
	xml.WriteString(`<presets deviceID="` + readinessDevice + `">`)
	for _, preset := range presets {
		fmt.Fprintf(&xml, `<preset id="%s"><ContentItem source="%s" type="%s" location="%s" isPresetable="true"><itemName>%s</itemName></ContentItem></preset>`,
			preset.ButtonNumber, preset.Source, preset.Type, preset.Location, preset.Name)
	}
	xml.WriteString(`</presets>`)

	return xml.String()
}

func requireMigrationNotReady(t *testing.T, err error) *MigrationDataNotReadyError {
	t.Helper()
	if err == nil {
		t.Fatal("expected migration data readiness error")
	}

	var notReady *MigrationDataNotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("error type = %T, want *MigrationDataNotReadyError: %v", err, err)
	}
	if notReady.Action == "" && !strings.Contains(err.Error(), "Data Sync") {
		t.Fatalf("default error is not actionable: %v", err)
	}
	if notReady.Action != "" && !strings.Contains(err.Error(), notReady.Action) {
		t.Fatalf("custom action is missing from error: %v", err)
	}

	return notReady
}

func TestMigrateSpeakerMissingSnapshotBlocksBeforeTelnet(t *testing.T) {
	m, _, deviceIP := newMigrationReadinessFixture(t, `<presets/>`)
	telnetCalls := 0
	m.NewTelnet = func(string) TelnetClient {
		telnetCalls++
		return &fakeTelnet{}
	}

	_, err := m.MigrateSpeaker(deviceIP, "", "", nil, MigrationMethodTelnet)
	notReady := requireMigrationNotReady(t, err)
	if !strings.Contains(notReady.Reason, "missing") {
		t.Fatalf("reason = %q, want missing snapshot", notReady.Reason)
	}
	if telnetCalls != 0 {
		t.Fatalf("telnet factory called %d times, want zero", telnetCalls)
	}
}

func TestMigrationDataReadinessBlocksPartialAndFilteredPresets(t *testing.T) {
	t.Run("partial live list", func(t *testing.T) {
		one := readinessPreset("1", "One", "http://radio.example/one")
		two := readinessPreset("2", "Two", "http://radio.example/two")
		m, ds, deviceIP := newMigrationReadinessFixture(t, livePresetsXML(one))
		if err := ds.SavePresets(readinessAccount, readinessDevice, []models.ServicePreset{one, two}); err != nil {
			t.Fatalf("SavePresets: %v", err)
		}

		requireMigrationNotReady(t, m.checkMigrationDataReady(deviceIP))
	})

	t.Run("preset filtered from full", func(t *testing.T) {
		filtered := readinessPreset("1", "Spotify", "spotify:track:missing")
		filtered.Source = "SPOTIFY"
		filtered.SourceID = "missing-spotify-source"
		m, ds, deviceIP := newMigrationReadinessFixture(t, livePresetsXML(filtered))
		if err := ds.SavePresets(readinessAccount, readinessDevice, []models.ServicePreset{filtered}); err != nil {
			t.Fatalf("SavePresets: %v", err)
		}

		notReady := requireMigrationNotReady(t, m.checkMigrationDataReady(deviceIP))
		if !strings.Contains(notReady.Reason, "rendered /full") {
			t.Fatalf("reason = %q, want rendered /full mismatch", notReady.Reason)
		}
	})

	t.Run("shared account", func(t *testing.T) {
		m, ds, deviceIP := newMigrationReadinessFixture(t, `<presets/>`)
		if err := ds.SavePresets(readinessAccount, readinessDevice, nil); err != nil {
			t.Fatalf("SavePresets: %v", err)
		}

		if err := ds.SaveDeviceInfo(readinessAccount, "SIBLING01", &models.ServiceDeviceInfo{
			DeviceID:  "SIBLING01",
			AccountID: readinessAccount,
			Name:      "Sibling Speaker",
		}); err != nil {
			t.Fatalf("SaveDeviceInfo sibling: %v", err)
		}

		notReady := requireMigrationNotReady(t, m.checkMigrationDataReady(deviceIP))
		if !strings.Contains(notReady.Reason, "2 devices") {
			t.Fatalf("reason = %q, want shared-account device count", notReady.Reason)
		}
		if !strings.Contains(notReady.Action, "dedicated account") {
			t.Fatalf("action = %q, want dedicated-account guidance", notReady.Action)
		}
		if !strings.Contains(notReady.Action, "Data Sync") {
			t.Fatalf("action = %q, want Data Sync guidance", notReady.Action)
		}
	})
}

func TestMigrationDataReadinessAllowsValidEmptyAndSyncedPresets(t *testing.T) {
	t.Run("valid empty", func(t *testing.T) {
		m, ds, deviceIP := newMigrationReadinessFixture(t, `<presets/>`)
		if err := ds.SavePresets(readinessAccount, readinessDevice, nil); err != nil {
			t.Fatalf("SavePresets: %v", err)
		}

		if err := m.checkMigrationDataReady(deviceIP); err != nil {
			t.Fatalf("checkMigrationDataReady: %v", err)
		}
	})

	t.Run("fully synced", func(t *testing.T) {
		preset := readinessPreset("1", "Radio", "http://radio.example/stream")
		m, ds, deviceIP := newMigrationReadinessFixture(t, livePresetsXML(preset))
		if err := ds.SavePresets(readinessAccount, readinessDevice, []models.ServicePreset{preset}); err != nil {
			t.Fatalf("SavePresets: %v", err)
		}

		if err := m.checkMigrationDataReady(deviceIP); err != nil {
			t.Fatalf("checkMigrationDataReady: %v", err)
		}
	})
}

func TestMigrationDataReadinessDoesNotRewritePresetSnapshots(t *testing.T) {
	t.Run("ready single device", func(t *testing.T) {
		preset := readinessPreset("1", "Target Radio", "http://radio.example/target")
		m, ds, deviceIP := newMigrationReadinessFixture(t, livePresetsXML(preset))
		if err := ds.SavePresets(readinessAccount, readinessDevice, []models.ServicePreset{preset}); err != nil {
			t.Fatalf("SavePresets target: %v", err)
		}
		targetPath := filepath.Join(ds.AccountDeviceDir(readinessAccount, readinessDevice), constants.PresetsFile)
		before, err := os.ReadFile(targetPath)
		if err != nil {
			t.Fatalf("read target snapshot: %v", err)
		}

		if err = m.checkMigrationDataReady(deviceIP); err != nil {
			t.Fatalf("checkMigrationDataReady: %v", err)
		}
		assertPresetSnapshotUnchanged(t, targetPath, before)
	})

	t.Run("rejected shared account", func(t *testing.T) {
		m, ds, deviceIP := newMigrationReadinessFixture(t, `<presets/>`)
		if err := ds.SavePresets(readinessAccount, readinessDevice, nil); err != nil {
			t.Fatalf("SavePresets target: %v", err)
		}

		const siblingDevice = "SIBLING01"
		if err := ds.SaveDeviceInfo(readinessAccount, siblingDevice, &models.ServiceDeviceInfo{
			DeviceID:  siblingDevice,
			AccountID: readinessAccount,
			Name:      "Sibling Speaker",
		}); err != nil {
			t.Fatalf("SaveDeviceInfo sibling: %v", err)
		}

		legacy := []byte(`<presets><preset id="1"><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="http://radio.example/sibling"><itemName>Sibling Radio</itemName></ContentItem></preset></presets>`)
		siblingPath := filepath.Join(ds.AccountDeviceDir(readinessAccount, siblingDevice), constants.PresetsFile)
		if err := ds.WriteFileUnderBase(siblingPath, legacy, 0o644); err != nil {
			t.Fatalf("write legacy sibling snapshot: %v", err)
		}

		requireMigrationNotReady(t, m.checkMigrationDataReady(deviceIP))
		assertPresetSnapshotUnchanged(t, siblingPath, legacy)
	})
}

func assertPresetSnapshotUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preset snapshot: %v", err)
	}
	if !bytes.Equal(after, want) {
		t.Fatalf("readiness preflight rewrote Presets.xml\n got: %s\nwant: %s", after, want)
	}
}

// TestMigrationDataReadinessAllowsUnpairedSpeaker: a factory-reset speaker has
// no account data to preserve, and the admin UI migrates before it pairs, so
// refusing here would make onboarding impossible. Data Sync could not unblock
// it either: an account-less device is filed under "default", which never
// matches an empty live account.
func TestMigrationDataReadinessAllowsUnpairedSpeaker(t *testing.T) {
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			_, _ = fmt.Fprintf(w, `<info deviceID="%s"><name>Fresh Speaker</name><margeAccountUUID></margeAccountUUID></info>`, readinessDevice)

			return
		}

		http.NotFound(w, r)
	}))
	defer speaker.Close()

	m := NewManager("http://aftertouch.example:8000", datastore.NewDataStore(t.TempDir()), nil)

	if err := m.checkMigrationDataReady(strings.TrimPrefix(speaker.URL, "http://")); err != nil {
		t.Fatalf("unpaired speaker was refused migration: %v", err)
	}
}

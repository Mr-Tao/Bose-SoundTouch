package webtypes

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestNowPlayingRejectsPollOlderThanEvent(t *testing.T) {
	conn := NewDeviceConnection(nil, nil)
	pollRevision := conn.NextFieldRevision()
	eventRevision := conn.NextFieldRevision()
	eventNowPlaying := &models.NowPlaying{Track: "new event"}

	conn.UpdateStatus(func(status *DeviceStatus) {
		if !status.MergeNowPlaying(eventNowPlaying, eventRevision) {
			t.Fatal("newer NowPlaying event was rejected")
		}
	})
	conn.UpdateStatus(func(status *DeviceStatus) {
		if status.MergeNowPlaying(&models.NowPlaying{Track: "old poll"}, pollRevision) {
			t.Fatal("older NowPlaying poll was accepted after the event")
		}
	})

	if got := conn.Status().NowPlaying; got != eventNowPlaying {
		t.Fatalf("older poll replaced NowPlaying event: %+v", got)
	}
}

func TestDeviceStatusRevisionAdvancesForEveryProjection(t *testing.T) {
	conn := NewDeviceConnection(nil, nil)
	if got := conn.Status().Revision; got != 0 {
		t.Fatalf("initial revision = %d, want 0", got)
	}

	conn.UpdateStatus(func(*DeviceStatus) {})
	if got := conn.Status().Revision; got != 1 {
		t.Fatalf("revision after UpdateStatus = %d, want 1", got)
	}

	conn.SetStatus(&DeviceStatus{Revision: 99})
	if got := conn.Status().Revision; got != 2 {
		t.Fatalf("revision after SetStatus = %d, want 2", got)
	}
}

func TestUnrelatedProjectionDoesNotAdvanceNowPlayingRevision(t *testing.T) {
	conn := NewDeviceConnection(nil, nil)
	conn.SetStatus(&DeviceStatus{NowPlaying: &models.NowPlaying{Source: "SPOTIFY"}})
	baseline := conn.Status()

	volumeRevision := conn.NextFieldRevision()
	conn.UpdateStatus(func(status *DeviceStatus) {
		status.MergeVolume(&models.Volume{ActualVolume: 35}, volumeRevision)
	})
	updated := conn.Status()

	if updated.Revision <= baseline.Revision {
		t.Fatalf("aggregate revision = %d, want newer than %d", updated.Revision, baseline.Revision)
	}
	if updated.NowPlayingRevision != baseline.NowPlayingRevision {
		t.Fatalf("now-playing revision = %d, want unchanged %d after volume update",
			updated.NowPlayingRevision, baseline.NowPlayingRevision)
	}
}

func TestDeviceStatusRevisionIsMonotonicWithConcurrentProjections(t *testing.T) {
	conn := NewDeviceConnection(nil, nil)
	const projections = 64

	var wg sync.WaitGroup
	wg.Add(projections)
	for i := range projections {
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				conn.UpdateStatus(func(*DeviceStatus) {})
				return
			}
			conn.SetStatus(&DeviceStatus{})
		}()
	}
	wg.Wait()

	if got := conn.Status().Revision; got != projections {
		t.Fatalf("final revision = %d, want %d", got, projections)
	}
}

func TestDeviceStatusJSONExposesPublicRevisionsOnly(t *testing.T) {
	conn := NewDeviceConnection(nil, nil)
	revision := conn.NextFieldRevision()
	conn.UpdateStatus(func(status *DeviceStatus) {
		status.MergeNowPlaying(&models.NowPlaying{Track: "test"}, revision)
	})

	encoded, err := json.Marshal(conn.Status())
	if err != nil {
		t.Fatalf("marshal DeviceStatus: %v", err)
	}
	jsonStatus := string(encoded)
	if !strings.Contains(jsonStatus, `"revision":1`) {
		t.Fatalf("public revision missing from JSON: %s", jsonStatus)
	}
	if !strings.Contains(jsonStatus, `"nowPlayingRevision":1`) {
		t.Fatalf("now-playing revision missing from JSON: %s", jsonStatus)
	}
	if strings.Contains(jsonStatus, "fieldRevision") {
		t.Fatalf("internal field revision leaked into JSON: %s", jsonStatus)
	}
}

package soundtouchweb

import (
	"strings"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

func tuneInNowPlaying(source string) *models.NowPlaying {
	return &models.NowPlaying{
		Source: source,
		ContentItem: &models.ContentItem{
			Source:   "TUNEIN",
			Type:     "stationurl",
			Location: "/v1/playback/station/s119025",
			ItemName: "Arabella Lovesongs",
		},
	}
}

func TestAutoResumeState_HealthyRemembersContentItemAndDoesNotResume(t *testing.T) {
	s := &autoResumeState{}

	item, attempt, shouldResume := s.observe("", tuneInNowPlaying("TUNEIN"))
	if shouldResume {
		t.Fatalf("shouldResume = true on a healthy source, want false")
	}

	if item != nil || attempt != 0 {
		t.Errorf("item/attempt = %v/%d, want nil/0", item, attempt)
	}

	if s.lastGoodContentItem == nil {
		t.Fatal("lastGoodContentItem was not recorded from a healthy now_playing")
	}
}

func TestAutoResumeState_FreshErrorAfterHealthyTriggersResume(t *testing.T) {
	s := &autoResumeState{}

	// Prime with a healthy TUNEIN event, matching the WS handler calling
	// observe once per event with the source seen on the previous call.
	s.observe("", tuneInNowPlaying("TUNEIN"))

	item, attempt, shouldResume := s.observe("TUNEIN", tuneInNowPlaying("INVALID_SOURCE"))
	if !shouldResume {
		t.Fatal("shouldResume = false on a fresh error transition, want true")
	}

	if attempt != 1 {
		t.Errorf("attempt = %d, want 1", attempt)
	}

	if item == nil || item.Location != "/v1/playback/station/s119025" {
		t.Errorf("item = %+v, want the last healthy ContentItem", item)
	}
}

func TestAutoResumeState_DoesNotResumeWithoutAPriorGoodContentItem(t *testing.T) {
	s := &autoResumeState{}

	// No healthy event was ever observed, so there's nothing to restore.
	_, _, shouldResume := s.observe("", tuneInNowPlaying("INVALID_SOURCE"))
	if shouldResume {
		t.Fatal("shouldResume = true with no prior good ContentItem, want false")
	}
}

func TestAutoResumeState_DoesNotResumeOnRepeatedErrorEvents(t *testing.T) {
	s := &autoResumeState{}

	s.observe("", tuneInNowPlaying("TUNEIN"))
	s.observe("TUNEIN", tuneInNowPlaying("INVALID_SOURCE")) // first resume, attempt 1

	// A second consecutive error event (wasError=true this time) must not
	// fire another resume — one attempt per drop, not per event.
	_, _, shouldResume := s.observe("INVALID_SOURCE", tuneInNowPlaying("INVALID_SOURCE"))
	if shouldResume {
		t.Fatal("shouldResume = true on a repeated error event, want false")
	}
}

func TestAutoResumeState_KeepsResumingIndefinitelyAcrossRepeatedDrops(t *testing.T) {
	s := &autoResumeState{}

	s.observe("", tuneInNowPlaying("TUNEIN"))

	// The reported #622 pattern: the same station drops and (once resumed)
	// recovers repeatedly, indefinitely, on a fixed cycle. Each fresh drop
	// after a genuine recovery must keep resuming — there is no cap.
	const cycles = 20

	for i := 1; i <= cycles; i++ {
		_, attempt, shouldResume := s.observe("TUNEIN", tuneInNowPlaying("INVALID_SOURCE"))
		if !shouldResume {
			t.Fatalf("cycle %d: shouldResume = false, want true", i)
		}

		if attempt != i {
			t.Errorf("cycle %d: attempt label = %d, want %d", i, attempt, i)
		}

		s.observe("INVALID_SOURCE", tuneInNowPlaying("TUNEIN")) // the resume worked
	}
}

func TestAutoResumeState_StopsRetryingAfterAFailedResume(t *testing.T) {
	s := &autoResumeState{}

	s.observe("", tuneInNowPlaying("TUNEIN"))

	_, _, shouldResume := s.observe("TUNEIN", tuneInNowPlaying("INVALID_SOURCE"))
	if !shouldResume {
		t.Fatal("shouldResume = false on the first drop, want true")
	}

	// The resume attempt itself failed (or the station is genuinely gone):
	// the speaker keeps reporting the same error source on further events.
	// wasError is now true, so nothing should fire again without a genuine
	// recovery in between — this is what keeps a truly dead station from
	// being retried forever.
	for i := 0; i < 5; i++ {
		_, _, shouldResume := s.observe("INVALID_SOURCE", tuneInNowPlaying("INVALID_SOURCE"))
		if shouldResume {
			t.Fatalf("iteration %d: shouldResume = true on a persisting error, want false", i)
		}
	}
}

func TestAutoResumePlaybackAfter_ReselectsContentItem(t *testing.T) {
	speaker, captured := setupSpeakerMock(t, nil)
	defer speaker.Close()

	c := client.NewClient(&client.Config{Host: speaker.URL})
	conn := webtypes.NewDeviceConnection(c, &models.DeviceInfo{DeviceID: "DEVICEID01"})

	item := &models.ContentItem{Source: "TUNEIN", Type: "stationurl", Location: "/v1/playback/station/s119025", ItemName: "Arabella Lovesongs"}

	done := make(chan struct{})
	go func() {
		autoResumePlaybackAfter(conn, "DEVICEID01", item, 1, 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("autoResumePlaybackAfter did not return in time")
	}

	body, ok := captured["/select"]
	if !ok {
		t.Fatalf("no /select request captured; requests: %v", captured)
	}

	if !strings.Contains(body, `source="TUNEIN"`) || !strings.Contains(body, "/v1/playback/station/s119025") {
		t.Errorf("/select body = %q, want it to carry the TUNEIN content item", body)
	}
}

func TestAutoResumePlaybackAfter_StopsWhenConnectionClosed(t *testing.T) {
	speaker, captured := setupSpeakerMock(t, nil)
	defer speaker.Close()

	c := client.NewClient(&client.Config{Host: speaker.URL})
	conn := webtypes.NewDeviceConnection(c, &models.DeviceInfo{DeviceID: "DEVICEID01"})
	conn.Close()

	item := &models.ContentItem{Source: "TUNEIN", Type: "stationurl", Location: "/v1/playback/station/s119025"}

	done := make(chan struct{})
	go func() {
		autoResumePlaybackAfter(conn, "DEVICEID01", item, 1, time.Hour)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("autoResumePlaybackAfter did not return promptly after conn.Close()")
	}

	if _, ok := captured["/select"]; ok {
		t.Error("/select was called after the connection was closed, want no request")
	}
}

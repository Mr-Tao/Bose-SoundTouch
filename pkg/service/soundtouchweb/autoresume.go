package soundtouchweb

import (
	"log"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

// autoResumeBackoff is the delay before re-issuing a dropped content item,
// giving a transient upstream hiccup a moment to clear before retrying.
const autoResumeBackoff = 2 * time.Second

// autoResumeState tracks what ConnectDeviceWebSocket needs to decide whether
// a now_playing transition should trigger an auto-resume. Split out from the
// WebSocket goroutine so the decision can be unit tested without a live
// connection.
//
// resumeAttempts only labels log lines — it is never used to cap retries.
// A resume is gated on wasError being false (see observe), which already
// means at most one attempt ever fires per drop: if the attempt fails and
// the source stays in error, every following event has wasError=true and
// nothing fires again until a genuine recovery is observed. A station that
// keeps recovering and re-dropping (the reported #622 pattern — a TuneIn
// stream disconnecting the speaker on a fixed cycle, indefinitely, while
// otherwise healthy) is exactly the case this should keep resuming forever.
type autoResumeState struct {
	lastGoodContentItem *models.ContentItem
	resumeAttempts      int
}

// observe updates the state for a new now_playing event and reports whether
// the caller should fire an auto-resume for item, plus a label for the log
// line. prevSource is the source seen on the previous event.
//
// #622: some TuneIn stations disconnect the speaker's audio pipeline on
// their own (errorUpdate 1041 SOURCE_DISCONNECTED, observed ~5m35s into
// playback on one reporter's setup) even though the SoundTouch WebSocket
// control channel stays healthy throughout. The firmware does not recover
// on its own, so a fresh transition into an error source right after a
// healthy one — the speaker dropping a source it didn't choose to leave, as
// opposed to the user picking a new one — re-issues the last content item,
// exactly what pressing the physical preset button again does.
func (s *autoResumeState) observe(prevSource string, np *models.NowPlaying) (item *models.ContentItem, attempt int, shouldResume bool) {
	wasError := isErrorSource(prevSource)
	nowError := isErrorSource(np.Source)

	if !nowError {
		if np.ContentItem != nil {
			s.lastGoodContentItem = np.ContentItem
		}

		return nil, 0, false
	}

	if wasError || s.lastGoodContentItem == nil {
		return nil, 0, false
	}

	s.resumeAttempts++

	return s.lastGoodContentItem, s.resumeAttempts, true
}

// autoResumePlayback re-selects item on conn's device after autoResumeBackoff.
// It runs in its own goroutine (never on the WebSocket read loop) so a slow
// or hanging /select call can't stall processing of further device events.
func autoResumePlayback(conn *webtypes.DeviceConnection, deviceID string, item *models.ContentItem, attempt int) {
	autoResumePlaybackAfter(conn, deviceID, item, attempt, autoResumeBackoff)
}

// autoResumePlaybackAfter is autoResumePlayback with an injectable delay so
// tests don't have to wait out the real backoff.
func autoResumePlaybackAfter(conn *webtypes.DeviceConnection, deviceID string, item *models.ContentItem, attempt int, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-conn.Done():
		return
	}

	if conn.Client == nil {
		return
	}

	if err := conn.Client.SelectContentItem(item); err != nil {
		log.Printf("[play] device=%q auto-resume attempt %d failed: %v",
			sanitizeLog(deviceID), attempt, err)

		return
	}

	log.Printf("[play] device=%q auto-resume attempt %d re-selected source=%q location=%q",
		sanitizeLog(deviceID), attempt, sanitizeLog(item.Source), sanitizeLog(item.Location))
}

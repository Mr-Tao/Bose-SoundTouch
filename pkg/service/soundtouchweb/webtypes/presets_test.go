package webtypes

import (
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestPresetPublicationFenceCoversPausedCASMutator(t *testing.T) {
	conn := NewDeviceConnection(nil, nil)
	conn.UpdateStatus(func(s *DeviceStatus) { s.Volume = &models.Volume{ActualVolume: 17} })
	generation := conn.BeginFieldPoll(FieldPresets)
	entered, release, oldDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	old := &models.Presets{Preset: []models.Preset{{ID: 1}}}
	newer := &models.Presets{Preset: []models.Preset{{ID: 3}}}
	go func() {
		defer close(oldDone)
		conn.completePresetsPoll(generation, func(s *DeviceStatus) {
			// This must be held through the actual CAS mutation, not only
			// during its generation check. TryLock makes the assertion
			// deterministic without timing how quickly another goroutine runs.
			if conn.presetFieldMu.TryLock() {
				conn.presetFieldMu.Unlock()
				t.Error("preset publication fence released before status mutation")
			}
			close(entered)
			<-release
			s.Presets = old
		})
	}()
	<-entered
	eventStarted, eventDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(eventDone)
		close(eventStarted)
		conn.ApplyPresetEvent(newer)
	}()
	<-eventStarted
	close(release)
	<-oldDone
	<-eventDone
	if conn.Status().Presets != newer || conn.Status().Volume.ActualVolume != 17 {
		t.Fatal("newer preset event lost or unrelated volume changed")
	}
	if accepted, _ := conn.ApplyPolledPresets(generation, old); accepted {
		t.Fatal("old generation accepted after the event")
	}
}

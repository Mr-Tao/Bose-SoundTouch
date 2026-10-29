package webtypes

import (
	"reflect"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

// ApplyPolledPresets keeps the generation decision and publication ordered
// together. A CAS retry must not replay an older preset bank over a push event.
// The network read happens before entering this short, preset-only section.
func (c *DeviceConnection) ApplyPolledPresets(generation uint64, presets *models.Presets) (applied, changed bool) {
	applied = c.completePresetsPoll(generation, func(status *DeviceStatus) {
		changed = !reflect.DeepEqual(status.Presets, presets)
		status.Presets = presets
		status.LastActivity = time.Now()
	})
	return applied, changed
}

func (c *DeviceConnection) completePresetsPoll(generation uint64, mutation func(*DeviceStatus)) bool {
	c.presetFieldMu.Lock()
	defer c.presetFieldMu.Unlock()
	return c.CompleteFieldPoll(FieldPresets, generation, mutation)
}

// ApplyPresetEvent publishes the whole bank, including a slot emptied when
// firmware moves its content to another button.
func (c *DeviceConnection) ApplyPresetEvent(presets *models.Presets) (changed bool) {
	c.presetFieldMu.Lock()
	defer c.presetFieldMu.Unlock()

	c.ApplyFieldEvent(FieldPresets, func(status *DeviceStatus) {
		changed = !reflect.DeepEqual(status.Presets, presets)
		status.Presets = presets
		status.LastActivity = time.Now()
	})
	return changed
}

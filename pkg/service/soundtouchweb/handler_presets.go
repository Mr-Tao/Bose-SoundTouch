package soundtouchweb

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
)

const presetTargetHeader = "X-AfterTouch-Preset-Target"

// validatePresetTarget leaves the published primitive compatible with older
// clients. The player supplies its preflight identity to checked reads/writes.
func (app *WebApp) validatePresetTarget(w http.ResponseWriter, r *http.Request, device *webtypes.DeviceConnection) bool {
	expected := strings.TrimSpace(r.Header.Get(presetTargetHeader))
	if expected == "" {
		return true
	}
	if settingsTargetIdentity(device) != expected {
		app.sendError(w, "Preset target changed; reopen the device", http.StatusConflict)
		return false
	}
	if err := liveSettingsTargetError(device, expected); err != nil {
		app.sendError(w, "Could not verify the preset target; reopen the device", http.StatusConflict)
		return false
	}
	if current, exists := app.GetDevice(chi.URLParam(r, "id")); !exists || current != device {
		app.sendError(w, "Preset target changed; reopen the device", http.StatusConflict)
		return false
	}
	return true
}

func (app *WebApp) readDevicePresets(device *webtypes.DeviceConnection) error {
	if device.Client == nil {
		return errors.New("device client unavailable")
	}
	generation := device.BeginFieldPoll(webtypes.FieldPresets)
	presets, err := device.Client.GetPresets()
	if err != nil {
		return err
	}
	select {
	case <-device.Done():
		return errors.New("device no longer registered")
	default:
	}
	_, changed := device.ApplyPolledPresets(generation, presets)
	app.queueBroadcastIfChanged(changed)
	return nil
}

// HandleDevicePresets returns the accepted projection after a fresh read, not
// an earlier cache entry presented as successful verification. If a newer push
// won the read race, its bank is returned instead of the stale HTTP answer.
func (app *WebApp) HandleDevicePresets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	deviceID := chi.URLParam(r, "id")
	device, exists := app.GetDevice(deviceID)
	if !exists {
		app.sendError(w, "Device not found", http.StatusNotFound)
		return
	}
	if device.Client == nil {
		app.sendError(w, "Device client not available", http.StatusServiceUnavailable)
		return
	}
	if !app.validatePresetTarget(w, r, device) {
		return
	}
	if err := app.readDevicePresets(device); err != nil {
		app.sendError(w, "Could not read the speaker's presets", http.StatusBadGateway)
		return
	}
	view, visible := app.deviceViewForID(deviceID)
	current, exists := app.GetDevice(deviceID)
	if !visible || !exists || current != device {
		app.sendError(w, "Preset target changed; reopen the device", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(webtypes.APIResponse{Success: true, Data: view}); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

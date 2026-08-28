package soundtouchweb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
)

type zoneVolumeMemberResult struct {
	DeviceID  string `json:"deviceId"`
	ControlID string `json:"controlId,omitempty"`
	Name      string `json:"name,omitempty"`
	Before    *int   `json:"before,omitempty"`
	Target    *int   `json:"target,omitempty"`
	Actual    *int   `json:"actual,omitempty"`
	Error     string `json:"error,omitempty"`
}

type zoneVolumeResult struct {
	MasterDeviceID string                   `json:"masterDeviceId"`
	Requested      int                      `json:"requested"`
	Baseline       int                      `json:"baseline"`
	Delta          int                      `json:"delta"`
	Partial        bool                     `json:"partial"`
	Members        []zoneVolumeMemberResult `json:"members"`
}

type zoneVolumeTarget struct {
	index  int
	conn   *webtypes.DeviceConnection
	before int
}

// HandleZoneVolume applies one proportional volume move to a logical zone.
// The scalar is the highest current logical-member volume; every reachable
// member moves by the same delta, preserving audible offsets except at 0/100
// clamps. Each write is followed by a device readback and partial failures are
// returned instead of rolling successful members back.
func (app *WebApp) HandleZoneVolume(w http.ResponseWriter, r *http.Request) {
	controlID := chi.URLParam(r, "id")

	requested, err := strconv.Atoi(chi.URLParam(r, "volume"))
	if err != nil || !models.ValidateVolumeLevel(requested) {
		app.sendError(w, "Invalid volume level (0-100)", http.StatusBadRequest)
		return
	}

	view, ok := app.deviceViewSnapshot()[controlID]
	if !ok {
		app.sendError(w, "Device not found", http.StatusNotFound)
		return
	}

	if view.Zone == nil {
		app.sendError(w, "Device is not a logical zone master", http.StatusConflict)
		return
	}

	masterDeviceID := strings.TrimSpace(view.Zone.MasterDeviceID)
	lock := app.zoneVolumeLock(masterDeviceID)
	lock.Lock()
	defer lock.Unlock()

	zone, err := app.revalidateZone(masterDeviceID)
	if err != nil {
		app.sendError(w, err.Error(), http.StatusConflict)
		return
	}

	result, ok := app.applyZoneVolume(zone, requested)
	if !ok {
		app.sendError(w, "No zone member volume could be read", http.StatusBadGateway)
		return
	}

	result.MasterDeviceID = masterDeviceID

	// A full projected inventory lets every browser reconcile zone-level volume
	// and any topology change observed during revalidation.
	app.BroadcastDeviceList()

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(webtypes.APIResponse{Success: true, Data: result}); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func (app *WebApp) zoneVolumeLock(masterDeviceID string) *sync.Mutex {
	value, _ := app.zoneVolumeLocks.LoadOrStore(masterDeviceID, &sync.Mutex{})

	lock, ok := value.(*sync.Mutex)
	if !ok {
		panic("zone volume lock has an invalid type")
	}

	return lock
}

func (app *WebApp) revalidateZone(masterDeviceID string) (*models.ZoneInfo, error) {
	masterIP := app.findIPByHwID(masterDeviceID)

	master, ok := app.GetDevice(masterIP)
	if !ok || master.Client == nil {
		return nil, fmt.Errorf("zone master is unavailable")
	}

	generation := master.BeginZoneRefresh()

	zone, err := master.Client.GetZone()
	if err != nil {
		return nil, fmt.Errorf("refresh zone master: %w", err)
	}

	if !master.ApplyPolledZone(generation, masterDeviceID, zone) {
		return nil, fmt.Errorf("zone topology changed during refresh")
	}

	if zone.IsStandalone() {
		app.BroadcastDeviceList()
		return nil, fmt.Errorf("zone has been dissolved")
	}

	return zone, nil
}

func (app *WebApp) applyZoneVolume(zone *models.ZoneInfo, requested int) (zoneVolumeResult, bool) {
	projected, ok := projectZoneInfo(zone, captureDeviceProjectionEntries(app.DeviceSnapshot()))
	if !ok {
		return zoneVolumeResult{Requested: requested}, false
	}

	members := projected.Members
	result := zoneVolumeResult{
		Requested: requested,
		Members:   make([]zoneVolumeMemberResult, len(members)),
	}
	targets := make([]zoneVolumeTarget, 0, len(members))

	var targetsMu sync.Mutex

	var readWG sync.WaitGroup
	readWG.Add(len(members))

	for index := range members {
		go func() {
			defer readWG.Done()

			projectedMember := &members[index]
			member := zoneVolumeResultForMember(projectedMember)

			conn, ok := app.GetDevice(projectedMember.ControlID)
			if !ok || conn.Client == nil {
				member.Error = "speaker unavailable"
				result.Members[index] = member

				return
			}

			if !authoritativeVolumeControl(conn) {
				member.Error = "stereo pair master unavailable"
				result.Members[index] = member

				return
			}

			if info := conn.Info(); info != nil {
				member.Name = info.Name
			}

			volume, err := conn.Client.GetVolume()
			if err != nil {
				member.Error = fmt.Sprintf("read volume: %v", err)
				result.Members[index] = member

				return
			}

			before := volume.ActualVolume
			member.Before = &before
			result.Members[index] = member

			targetsMu.Lock()

			targets = append(targets, zoneVolumeTarget{index: index, conn: conn, before: before})
			targetsMu.Unlock()
		}()
	}

	readWG.Wait()

	if len(targets) == 0 {
		return result, false
	}

	baseline := 0
	for _, target := range targets {
		if target.before > baseline {
			baseline = target.before
		}
	}

	result.Baseline = baseline
	result.Delta = requested - baseline

	var partial atomic.Bool
	partial.Store(len(targets) != len(members))

	var writeWG sync.WaitGroup
	writeWG.Add(len(targets))

	for _, target := range targets {
		go func() {
			defer writeWG.Done()

			member := &result.Members[target.index]
			level := models.ClampVolumeLevel(target.before + result.Delta)
			member.Target = &level

			if !app.applyVolumeTarget(member, target.conn, level) {
				partial.Store(true)
			}
		}()
	}

	writeWG.Wait()

	result.Partial = partial.Load()

	return result, true
}

func zoneVolumeResultForMember(member *zoneMemberView) zoneVolumeMemberResult {
	result := zoneVolumeMemberResult{
		DeviceID:  member.HardwareID,
		ControlID: member.ControlID,
		Name:      member.Name,
		Before:    member.ActualVolume,
	}
	if result.DeviceID == "" && len(member.DeviceIDs) != 0 {
		result.DeviceID = member.DeviceIDs[0]
	}

	return result
}

// A registered stereo slave is not an independent volume target. If its
// firmware master is unavailable, fail closed until the logical pair can be
// projected through that master again.
func authoritativeVolumeControl(conn *webtypes.DeviceConnection) bool {
	if conn == nil {
		return false
	}

	info := conn.Info()
	status := conn.Status()

	if info == nil {
		return false
	}

	if status == nil || status.Group == nil {
		return true
	}

	masterDeviceID := strings.TrimSpace(status.Group.MasterDeviceID)

	return masterDeviceID == "" || masterDeviceID == strings.TrimSpace(info.DeviceID)
}

func (app *WebApp) applyVolumeTarget(
	member *zoneVolumeMemberResult,
	conn *webtypes.DeviceConnection,
	level int,
) bool {
	writeErr := conn.Client.SetVolume(level)
	volumeGeneration := conn.BeginVolumeRefresh()
	healthGeneration := conn.BeginHTTPPoll()

	volume, readErr := conn.Client.GetVolume()
	if readErr != nil {
		conn.CompleteHTTPPoll(healthGeneration, false, time.Now(), nil)

		if writeErr != nil {
			appendZoneVolumeError(member, fmt.Sprintf("set volume: %v", writeErr))
		}

		appendZoneVolumeError(member, fmt.Sprintf("readback volume: %v", readErr))

		return false
	}

	actual := volume.ActualVolume
	member.Actual = intPointer(actual)

	if !conn.ApplyPolledVolume(volumeGeneration, volume) {
		if current := conn.Status().Volume; current != nil {
			actual = current.ActualVolume
			member.Actual = intPointer(actual)
		}
	}

	conn.CompleteHTTPPoll(healthGeneration, true, time.Now(), nil)

	if actual == level {
		return true
	}

	if writeErr != nil {
		appendZoneVolumeError(member, fmt.Sprintf("set volume: %v", writeErr))
	}

	appendZoneVolumeError(member, fmt.Sprintf("readback volume %d does not match target %d", actual, level))

	return false
}

func intPointer(value int) *int {
	return &value
}

func appendZoneVolumeError(member *zoneVolumeMemberResult, message string) {
	if member.Error != "" {
		member.Error += "; "
	}

	member.Error += message
}

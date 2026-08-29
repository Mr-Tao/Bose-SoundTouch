package soundtouchweb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
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
	index         int
	controlID     string
	conn          *webtypes.DeviceConnection
	groupTopology webtypes.GroupTopology
	before        int
}

type zoneVolumeTopology struct {
	controlID string
	conn      *webtypes.DeviceConnection
	snapshot  webtypes.ZoneTopology
}

const (
	zoneVolumeReadbackAttempts   = 3
	zoneVolumeReadbackRetryDelay = 50 * time.Millisecond
)

// HandleZoneVolume moves every current logical member by one shared delta.
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

	zoneTopology, err := app.revalidateZone(masterDeviceID)
	if err != nil {
		app.sendError(w, err.Error(), http.StatusConflict)
		return
	}

	result, readable := app.applyZoneVolume(zoneTopology.snapshot.Zone, &zoneTopology, requested)
	if !readable {
		app.sendError(w, "No zone member volume could be read", http.StatusBadGateway)
		return
	}

	result.MasterDeviceID = masterDeviceID
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

func (app *WebApp) revalidateZone(masterDeviceID string) (zoneVolumeTopology, error) {
	masterControlID := app.findIPByHwID(masterDeviceID)
	master, ok := app.GetDevice(masterControlID)
	if !ok || master.Client == nil {
		return zoneVolumeTopology{}, fmt.Errorf("zone master is unavailable")
	}

	generation := master.BeginZoneRefresh()
	zone, err := master.Client.GetZone()
	if err != nil {
		return zoneVolumeTopology{}, fmt.Errorf("refresh zone master: %w", err)
	}
	changed := master.ApplyPolledZone(generation, masterDeviceID, zone)
	snapshot, current := master.SnapshotZoneTopology()
	if !current {
		return zoneVolumeTopology{}, fmt.Errorf("zone topology changed during refresh")
	}
	if zone == nil {
		return zoneVolumeTopology{}, fmt.Errorf("zone master returned no topology")
	}
	if zone.IsStandalone() {
		if changed {
			app.BroadcastDeviceList()
		}
		return zoneVolumeTopology{}, fmt.Errorf("zone has been dissolved")
	}

	if !reflect.DeepEqual(snapshot.Zone, zone) {
		return zoneVolumeTopology{}, fmt.Errorf("zone topology changed after refresh")
	}

	return zoneVolumeTopology{controlID: masterControlID, conn: master, snapshot: snapshot}, nil
}

func (app *WebApp) applyZoneVolume(
	zone *models.ZoneInfo,
	zoneTopology *zoneVolumeTopology,
	requested int,
) (zoneVolumeResult, bool) {
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
			member.Before = nil

			conn, ok := app.GetDevice(projectedMember.ControlID)
			if !ok || conn.Client == nil {
				member.Error = "speaker unavailable"
				result.Members[index] = member
				return
			}

			groupTopology, current := conn.SnapshotGroupTopology()
			if !current || !authoritativeVolumeTopology(conn, groupTopology) {
				member.Error = "stereo pair master unavailable"
				result.Members[index] = member
				return
			}

			volumeGeneration := conn.BeginVolumeRefresh()
			volume, err := conn.Client.GetVolume()
			if err != nil || volume == nil {
				if err != nil {
					member.Error = fmt.Sprintf("read volume: %v", err)
				} else {
					member.Error = "read volume: empty response"
				}
				result.Members[index] = member
				return
			}

			if !app.applyCurrentVolumeReadback(
				projectedMember.ControlID,
				conn,
				groupTopology,
				zoneTopology,
				volumeGeneration,
				volume,
			) {
				member.Error = "speaker topology or volume state changed while reading volume"
				result.Members[index] = member
				return
			}

			before := volume.ActualVolume
			member.Before = intPointer(before)
			result.Members[index] = member

			targetsMu.Lock()
			targets = append(targets, zoneVolumeTarget{
				index:         index,
				controlID:     projectedMember.ControlID,
				conn:          conn,
				groupTopology: groupTopology,
				before:        before,
			})
			targetsMu.Unlock()
		}()
	}

	readWG.Wait()
	if len(targets) == 0 {
		return result, false
	}

	for _, target := range targets {
		if target.before > result.Baseline {
			result.Baseline = target.before
		}
	}
	result.Delta = requested - result.Baseline

	var partial atomic.Bool
	partial.Store(len(targets) != len(members))
	var writeWG sync.WaitGroup
	writeWG.Add(len(targets))

	for _, target := range targets {
		go func() {
			defer writeWG.Done()

			member := &result.Members[target.index]
			level := models.ClampVolumeLevel(target.before + result.Delta)
			member.Target = intPointer(level)
			atTarget, _ := app.applyVolumeTarget(
				member,
				target.controlID,
				target.conn,
				target.groupTopology,
				zoneTopology,
				level,
			)
			if !atTarget {
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

func authoritativeVolumeTopology(conn *webtypes.DeviceConnection, topology webtypes.GroupTopology) bool {
	if conn == nil || conn.DeviceInfo == nil {
		return false
	}
	if topology.Group == nil {
		return true
	}

	masterDeviceID := strings.TrimSpace(topology.Group.MasterDeviceID)

	return masterDeviceID != "" && masterDeviceID == strings.TrimSpace(conn.DeviceInfo.DeviceID)
}

func (app *WebApp) applyVolumeTarget(
	member *zoneVolumeMemberResult,
	controlID string,
	conn *webtypes.DeviceConnection,
	groupTopology webtypes.GroupTopology,
	zoneTopology *zoneVolumeTopology,
	level int,
) (bool, bool) {
	atTarget := false
	confirmed := false

	conn.WithVolumeOperation(func() {
		var writeErr error
		var volumeGeneration uint64
		if !app.withCurrentVolumeWrite(controlID, conn, groupTopology, zoneTopology, func() {
			volumeGeneration = conn.BeginVolumeRefresh()
			writeErr = conn.Client.SetVolume(level)
		}) {
			appendZoneVolumeError(member, "speaker topology changed before volume update")
			return
		}

		var volume *models.Volume
		var readErr error
		for attempt := 0; attempt < zoneVolumeReadbackAttempts; attempt++ {
			if attempt > 0 {
				app.waitForVolumeReadbackRetry()
				if !app.volumeTargetCurrent(controlID, conn, groupTopology, zoneTopology) {
					confirmed = false
					break
				}
				volumeGeneration = conn.BeginVolumeRefresh()
			}

			volume, readErr = conn.Client.GetVolume()
			if readErr != nil || volume == nil {
				break
			}

			confirmed = app.applyCurrentVolumeReadback(
				controlID,
				conn,
				groupTopology,
				zoneTopology,
				volumeGeneration,
				volume,
			)
			if confirmed && volume.TargetVolume == level && volume.ActualVolume == level {
				atTarget = true
				break
			}
			if !app.volumeTargetCurrent(controlID, conn, groupTopology, zoneTopology) {
				break
			}
		}

		if readErr != nil || volume == nil {
			if writeErr != nil {
				appendZoneVolumeError(member, fmt.Sprintf("set volume: %v", writeErr))
			}
			if readErr != nil {
				appendZoneVolumeError(member, fmt.Sprintf("readback volume: %v", readErr))
			} else {
				appendZoneVolumeError(member, "readback volume: empty response")
			}
			return
		}
		if !confirmed {
			appendZoneVolumeError(member, "speaker topology or volume state changed during readback")
			return
		}

		member.Actual = intPointer(volume.ActualVolume)
		if atTarget {
			return
		}
		if writeErr != nil {
			appendZoneVolumeError(member, fmt.Sprintf("set volume: %v", writeErr))
		}
		appendZoneVolumeError(member, fmt.Sprintf(
			"readback target %d actual %d does not both match requested %d",
			volume.TargetVolume,
			volume.ActualVolume,
			level,
		))
	})

	return atTarget, confirmed
}

func (app *WebApp) waitForVolumeReadbackRetry() {
	if app.volumeReadbackRetryWait != nil {
		app.volumeReadbackRetryWait(zoneVolumeReadbackRetryDelay)
		return
	}

	time.Sleep(zoneVolumeReadbackRetryDelay)
}

func (app *WebApp) volumeTargetCurrent(
	controlID string,
	conn *webtypes.DeviceConnection,
	topology webtypes.GroupTopology,
	zoneTopology *zoneVolumeTopology,
) bool {
	app.devicesMu.RLock()
	defer app.devicesMu.RUnlock()

	return app.devices[controlID] == conn && conn.GroupTopologyCurrent(topology) &&
		authoritativeVolumeTopology(conn, topology) && app.zoneTopologyCurrentLocked(zoneTopology)
}

func (app *WebApp) zoneTopologyCurrentLocked(topology *zoneVolumeTopology) bool {
	return topology == nil ||
		(app.devices[topology.controlID] == topology.conn && topology.conn.ZoneTopologyCurrent(topology.snapshot))
}

func (app *WebApp) withCurrentVolumeWrite(
	controlID string,
	conn *webtypes.DeviceConnection,
	topology webtypes.GroupTopology,
	zoneTopology *zoneVolumeTopology,
	operation func(),
) bool {
	current := false
	withZoneVolumeFence(zoneTopology, func() {
		conn.WithGroupWriteFence(func() {
			current = app.volumeTargetCurrent(controlID, conn, topology, zoneTopology)
			if current {
				operation()
			}
		})
	})

	return current
}

func (app *WebApp) applyCurrentVolumeReadback(
	controlID string,
	conn *webtypes.DeviceConnection,
	topology webtypes.GroupTopology,
	zoneTopology *zoneVolumeTopology,
	volumeGeneration uint64,
	volume *models.Volume,
) bool {
	applied := false
	withZoneVolumeFence(zoneTopology, func() {
		conn.WithGroupWriteFence(func() {
			if app.volumeTargetCurrent(controlID, conn, topology, zoneTopology) {
				applied = conn.ApplyPolledVolume(volumeGeneration, volume)
			}
		})
	})

	return applied
}

func withZoneVolumeFence(topology *zoneVolumeTopology, operation func()) {
	if topology == nil {
		operation()
		return
	}

	topology.conn.WithZoneWriteFence(operation)
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

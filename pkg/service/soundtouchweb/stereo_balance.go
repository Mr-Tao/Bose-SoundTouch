package soundtouchweb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
)

type stereoBalanceResult struct {
	Requested int  `json:"requested"`
	Target    *int `json:"target"`
	Actual    *int `json:"actual"`
	AtTarget  bool `json:"atTarget"`
}

// HandleStereoBalance sets and immediately verifies balance on the confirmed
// LEFT/master represented by one logical stereo-pair card.
func (app *WebApp) HandleStereoBalance(w http.ResponseWriter, r *http.Request) {
	controlID := strings.TrimSpace(chi.URLParam(r, "id"))
	requested, err := strconv.Atoi(chi.URLParam(r, "level"))
	if err != nil {
		app.sendError(w, "Invalid balance level", http.StatusBadRequest)
		return
	}

	conn, exists := app.GetDevice(controlID)
	if !exists {
		app.sendError(w, "Device not found", http.StatusNotFound)
		return
	}

	result := stereoBalanceResult{Requested: requested}
	status := http.StatusOK
	response := webtypes.APIResponse{Success: true, Data: result}
	broadcast := false

	conn.WithBalanceOperation(func() {
		if !app.confirmedStereoBalanceTarget(controlID, conn) {
			status = http.StatusConflict
			response.Success = false
			response.Error = "Device is not the LEFT/master of a confirmed stereo pair"
			return
		}

		if conn.Client == nil {
			status = http.StatusBadGateway
			response.Success = false
			response.Error = "Stereo balance device client is unavailable"
			return
		}

		refresh, ok := conn.BeginBalanceRefresh()
		if !ok || !confirmedStereoBalanceMaster(conn.Info().DeviceID, refresh.Group) {
			status = http.StatusConflict
			response.Success = false
			response.Error = "Stereo pair topology changed"
			return
		}

		available, minLevel, maxLevel, _, capabilityKnown := refresh.Balance.Capability()
		if !capabilityKnown || !available {
			status = http.StatusConflict
			response.Success = false
			response.Error = "Stereo balance capability is unknown or unavailable"
			return
		}
		if !models.ValidateBalanceLevelForRange(requested, minLevel, maxLevel) {
			status = http.StatusBadRequest
			response.Success = false
			response.Error = fmt.Sprintf("Invalid balance level (must be between %d and %d)", minLevel, maxLevel)
			return
		}
		var writeErr error
		if !app.withCurrentBalanceWrite(controlID, conn, refresh, func() {
			writeErr = conn.Client.SetBalanceForRange(requested, minLevel, maxLevel)
		}) {
			status = http.StatusConflict
			response.Success = false
			response.Error = "Stereo pair topology changed"
			return
		}

		balance, readErr := conn.Client.GetBalance()

		if !app.balanceRefreshCurrent(controlID, conn, refresh) || !app.confirmedStereoBalanceTarget(controlID, conn) {
			status = http.StatusConflict
			response.Success = false
			response.Error = "Stereo pair topology changed during balance update"
			return
		}
		if readErr != nil || balance == nil {
			if !app.applyBalanceReadback(controlID, conn, refresh, nil) {
				status = http.StatusConflict
				response.Success = false
				response.Error = "Stereo pair topology changed during balance update"
				return
			}
			broadcast = true
			status = http.StatusBadGateway
			response.Success = false
			switch {
			case writeErr != nil && readErr != nil:
				response.Error = fmt.Sprintf("Stereo balance write and readback are unverified: write: %v; read: %v", writeErr, readErr)
			case readErr != nil:
				response.Error = fmt.Sprintf("Stereo balance readback is unverified: %v", readErr)
			default:
				response.Error = "Stereo balance readback is unverified"
			}
			return
		}
		readAvailable, _, _, _, readCapabilityKnown := balance.Capability()
		if !app.applyBalanceReadback(controlID, conn, refresh, balance) {
			status = http.StatusConflict
			response.Success = false
			response.Error = "Stereo pair topology changed during balance update"
			return
		}
		broadcast = true
		if !readCapabilityKnown {
			status = http.StatusBadGateway
			response.Success = false
			response.Error = "Stereo balance readback capability is unverified"
			return
		}
		if !readAvailable {
			status = http.StatusConflict
			response.Success = false
			response.Error = "Stereo balance is no longer available"
			return
		}

		target := balance.TargetBalance
		actual := balance.ActualBalance
		result.Target = &target
		result.Actual = &actual
		result.AtTarget = target == requested && actual == requested
		response.Data = result
	})

	if broadcast {
		app.BroadcastDeviceList()
	}

	response.Data = result
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode stereo balance response", http.StatusInternalServerError)
	}
}

// withCurrentBalanceWrite holds the per-connection write fence from the final
// topology and registry check through SetBalance. Group invalidation and
// registry removal share this fence, while GET readback remains interruptible.
func (app *WebApp) withCurrentBalanceWrite(
	controlID string,
	conn *webtypes.DeviceConnection,
	refresh webtypes.BalanceRefresh,
	operation func(),
) bool {
	current := false
	conn.WithBalanceWriteFence(func() {
		current = app.balanceRefreshCurrent(controlID, conn, refresh) &&
			app.confirmedStereoBalanceTarget(controlID, conn)
		if current {
			operation()
		}
	})

	return current
}

func (app *WebApp) confirmedStereoBalanceTarget(controlID string, conn *webtypes.DeviceConnection) bool {
	if conn == nil || !stereoPairCapable(conn.Info()) {
		return false
	}

	registered, exists := app.GetDevice(controlID)
	if !exists || registered != conn {
		return false
	}
	info := conn.Info()
	status := conn.Status()
	if info == nil || status == nil || !confirmedStereoBalanceMaster(info.DeviceID, status.Group) {
		return false
	}

	view, exists := app.deviceViewSnapshot()[controlID]
	if !exists || view.StereoPair == nil || view.Info == nil ||
		strings.TrimSpace(view.StereoPair.MasterDeviceID) != strings.TrimSpace(view.Info.DeviceID) {
		return false
	}

	for _, member := range view.StereoPair.Members {
		if strings.EqualFold(strings.TrimSpace(member.Role), "LEFT") &&
			strings.TrimSpace(member.DeviceID) == strings.TrimSpace(view.StereoPair.MasterDeviceID) {
			return true
		}
	}

	return false
}

func confirmedStereoBalanceMaster(deviceID string, group *models.Group) bool {
	if !validMasterGroup(deviceID, group) {
		return false
	}

	masterID := strings.TrimSpace(group.MasterDeviceID)
	for _, member := range group.Roles.Roles {
		if strings.TrimSpace(member.DeviceID) == masterID && strings.EqualFold(strings.TrimSpace(member.Role), "LEFT") {
			return true
		}
	}

	return false
}

func (app *WebApp) balanceRefreshCurrent(
	controlID string,
	conn *webtypes.DeviceConnection,
	refresh webtypes.BalanceRefresh,
) bool {
	app.devicesMu.RLock()
	defer app.devicesMu.RUnlock()

	return app.devices[controlID] == conn && conn.BalanceRefreshCurrent(refresh)
}

func (app *WebApp) applyBalanceReadback(
	controlID string,
	conn *webtypes.DeviceConnection,
	refresh webtypes.BalanceRefresh,
	balance *models.Balance,
) bool {
	app.devicesMu.RLock()
	defer app.devicesMu.RUnlock()

	return app.devices[controlID] == conn && conn.ApplyBalanceReadback(refresh, balance)
}

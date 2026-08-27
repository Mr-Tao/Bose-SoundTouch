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

type stereoBalanceOperation struct {
	result    stereoBalanceResult
	response  webtypes.APIResponse
	status    int
	broadcast bool
}

func newStereoBalanceOperation(requested int) *stereoBalanceOperation {
	result := stereoBalanceResult{Requested: requested}

	return &stereoBalanceOperation{
		result:   result,
		response: webtypes.APIResponse{Success: true, Data: result},
		status:   http.StatusOK,
	}
}

func (operation *stereoBalanceOperation) fail(status int, message string) {
	operation.status = status
	operation.response.Success = false
	operation.response.Error = message
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

	operation := newStereoBalanceOperation(requested)

	conn.WithBalanceOperation(func() {
		app.runStereoBalanceOperation(controlID, conn, requested, operation)
	})

	if operation.broadcast {
		app.BroadcastDeviceList()
	}

	operation.response.Data = operation.result

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(operation.status)

	if err := json.NewEncoder(w).Encode(operation.response); err != nil {
		http.Error(w, "Failed to encode stereo balance response", http.StatusInternalServerError)
	}
}

func (app *WebApp) runStereoBalanceOperation(
	controlID string,
	conn *webtypes.DeviceConnection,
	requested int,
	operation *stereoBalanceOperation,
) {
	if !app.confirmedStereoBalanceTarget(controlID, conn) {
		operation.fail(http.StatusConflict, "Device is not the LEFT/master of a confirmed stereo pair")

		return
	}

	if conn.Client == nil {
		operation.fail(http.StatusBadGateway, "Stereo balance device client is unavailable")

		return
	}

	refresh, minLevel, maxLevel, ok := prepareStereoBalanceWrite(conn, requested, operation)
	if !ok {
		return
	}

	var writeErr error

	if !app.withCurrentBalanceWrite(controlID, conn, refresh, func() {
		writeErr = conn.Client.SetBalanceForRange(requested, minLevel, maxLevel)
	}) {
		operation.fail(http.StatusConflict, "Stereo pair topology changed")

		return
	}

	balance, readErr := conn.Client.GetBalance()
	if !app.balanceRefreshCurrent(controlID, conn, refresh) || !app.confirmedStereoBalanceTarget(controlID, conn) {
		operation.fail(http.StatusConflict, "Stereo pair topology changed during balance update")

		return
	}

	app.finishStereoBalanceReadback(controlID, conn, refresh, balance, writeErr, readErr, operation)
}

func prepareStereoBalanceWrite(
	conn *webtypes.DeviceConnection,
	requested int,
	operation *stereoBalanceOperation,
) (webtypes.BalanceRefresh, int, int, bool) {
	refresh, ok := conn.BeginBalanceRefresh()
	if !ok || !confirmedStereoBalanceMaster(conn.Info().DeviceID, refresh.Group) {
		operation.fail(http.StatusConflict, "Stereo pair topology changed")

		return refresh, 0, 0, false
	}

	available, minLevel, maxLevel, _, capabilityKnown := refresh.Balance.Capability()
	if !capabilityKnown || !available {
		operation.fail(http.StatusConflict, "Stereo balance capability is unknown or unavailable")

		return refresh, 0, 0, false
	}

	if !models.ValidateBalanceLevelForRange(requested, minLevel, maxLevel) {
		operation.fail(
			http.StatusBadRequest,
			fmt.Sprintf("Invalid balance level (must be between %d and %d)", minLevel, maxLevel),
		)

		return refresh, 0, 0, false
	}

	return refresh, minLevel, maxLevel, true
}

func (app *WebApp) finishStereoBalanceReadback(
	controlID string,
	conn *webtypes.DeviceConnection,
	refresh webtypes.BalanceRefresh,
	balance *models.Balance,
	writeErr error,
	readErr error,
	operation *stereoBalanceOperation,
) {
	if readErr != nil || balance == nil {
		if !app.applyBalanceReadback(controlID, conn, refresh, nil) {
			operation.fail(http.StatusConflict, "Stereo pair topology changed during balance update")

			return
		}

		operation.broadcast = true
		operation.fail(http.StatusBadGateway, stereoBalanceReadbackError(writeErr, readErr))

		return
	}

	readAvailable, _, _, _, readCapabilityKnown := balance.Capability()
	if !app.applyBalanceReadback(controlID, conn, refresh, balance) {
		operation.fail(http.StatusConflict, "Stereo pair topology changed during balance update")

		return
	}

	operation.broadcast = true
	if !readCapabilityKnown {
		operation.fail(http.StatusBadGateway, "Stereo balance readback capability is unverified")

		return
	}

	if !readAvailable {
		operation.fail(http.StatusConflict, "Stereo balance is no longer available")

		return
	}

	target := balance.TargetBalance
	actual := balance.ActualBalance
	operation.result.Target = &target
	operation.result.Actual = &actual
	operation.result.AtTarget = target == operation.result.Requested && actual == operation.result.Requested
}

func stereoBalanceReadbackError(writeErr, readErr error) string {
	switch {
	case writeErr != nil && readErr != nil:
		return fmt.Sprintf("Stereo balance write and readback are unverified: write: %v; read: %v", writeErr, readErr)
	case readErr != nil:
		return fmt.Sprintf("Stereo balance readback is unverified: %v", readErr)
	default:
		return "Stereo balance readback is unverified"
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

package soundtouchweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
)

type settingsSupport struct {
	ClockDisplay   bool `json:"clockDisplay"`
	ClockTime      bool `json:"clockTime"`
	SystemTimeout  bool `json:"systemTimeout"`
	Language       bool `json:"language"`
	Bluetooth      bool `json:"bluetooth"`
	BluetoothPair  bool `json:"bluetoothPair"`
	BluetoothClear bool `json:"bluetoothClear"`
	Sync           bool `json:"sync"`
	SourceNaming   bool `json:"sourceNaming"`
	WiFiOnboarding bool `json:"wifiOnboarding"`
}

type settingsClockDisplay struct {
	Enabled    bool   `json:"enabled"`
	Format     string `json:"format,omitempty"`
	Brightness int    `json:"brightness,omitempty"`
	TimeZone   string `json:"timeZone,omitempty"`
}

type settingsClockTime struct {
	UTC   int64  `json:"utc,omitempty"`
	Value string `json:"value,omitempty"`
}

type settingsSystemTimeout struct {
	Enabled bool `json:"enabled"`
}

type settingsLanguageOption struct {
	Code int    `json:"code"`
	Name string `json:"name"`
}

type settingsLanguage struct {
	Code    int                      `json:"code"`
	Options []settingsLanguageOption `json:"options"`
}

type settingsSync struct {
	Mode string `json:"mode"`
}

type settingsBluetooth struct {
	MACAddress       string `json:"macAddress,omitempty"`
	ConnectionStatus string `json:"connectionStatus,omitempty"`
	DeviceName       string `json:"deviceName,omitempty"`
}

type settingsSource struct {
	Source        string `json:"source"`
	SourceAccount string `json:"sourceAccount,omitempty"`
	DisplayName   string `json:"displayName"`
}

type settingsNetworkInterface struct {
	Type                         string `json:"type"`
	Name                         string `json:"name,omitempty"`
	MACAddress                   string `json:"macAddress,omitempty"`
	IPAddress                    string `json:"ipAddress,omitempty"`
	SSID                         string `json:"ssid,omitempty"`
	Band                         string `json:"band,omitempty"`
	State                        string `json:"state,omitempty"`
	FirmwareNetworkQuality       string `json:"firmwareNetworkQuality,omitempty"`
	FirmwareNetworkQualitySource string `json:"firmwareNetworkQualitySource,omitempty"`
	FirmwareNetworkQualityState  string `json:"firmwareNetworkQualityState,omitempty"`
	NetworkInfoFirmwareQuality   string `json:"networkInfoFirmwareQuality,omitempty"`
}

type settingsNetwork struct {
	Interfaces []settingsNetworkInterface `json:"interfaces"`
}

type deviceSettingsSnapshot struct {
	Support       settingsSupport        `json:"support"`
	ClockDisplay  *settingsClockDisplay  `json:"clockDisplay,omitempty"`
	ClockTime     *settingsClockTime     `json:"clockTime,omitempty"`
	SystemTimeout *settingsSystemTimeout `json:"systemTimeout,omitempty"`
	Language      *settingsLanguage      `json:"language,omitempty"`
	Sync          *settingsSync          `json:"sync,omitempty"`
	Bluetooth     *settingsBluetooth     `json:"bluetooth,omitempty"`
	Sources       []settingsSource       `json:"sources,omitempty"`
	Network       *settingsNetwork       `json:"network,omitempty"`
	OnboardingURL string                 `json:"onboardingUrl,omitempty"`
	Errors        map[string]string      `json:"errors,omitempty"`
}

var systemLanguageCodes = []models.LanguageCode{
	models.LanguageDanish, models.LanguageGerman, models.LanguageEnglish,
	models.LanguageSpanish, models.LanguageFrench, models.LanguageItalian,
	models.LanguageDutch, models.LanguageSwedish, models.LanguageJapanese,
	models.LanguageSimplifiedChinese, models.LanguageTraditionalChinese,
	models.LanguageKorean, models.LanguageThai, models.LanguageCzech,
	models.LanguageFinnish, models.LanguageGreek, models.LanguageNorwegian,
	models.LanguagePolish, models.LanguagePortuguese, models.LanguageRomanian,
	models.LanguageRussian, models.LanguageSlovenian, models.LanguageTurkish,
	models.LanguageHungarian,
}

func supportedSystemLanguageOptions(current models.LanguageCode) []settingsLanguageOption {
	options := make([]settingsLanguageOption, 0, len(systemLanguageCodes)+1)
	currentKnown := false

	for _, code := range systemLanguageCodes {
		options = append(options, settingsLanguageOption{Code: int(code), Name: models.SystemLanguageNames[code]})
		currentKnown = currentKnown || code == current
	}

	if !currentKnown {
		options = append(options, settingsLanguageOption{
			Code: int(current),
			Name: fmt.Sprintf("Unknown (%d)", current),
		})
	}

	return options
}

func (app *WebApp) settingsDevice(w http.ResponseWriter, r *http.Request) (*webtypes.DeviceConnection, bool) {
	deviceID := chi.URLParam(r, "id")
	if deviceID == "" {
		app.sendError(w, "Device ID required", http.StatusBadRequest)

		return nil, false
	}

	device, exists := app.GetDevice(deviceID)
	if !exists {
		app.sendError(w, "Device not found", http.StatusNotFound)

		return nil, false
	}

	if device.Client == nil {
		app.sendError(w, "Device client not available", http.StatusServiceUnavailable)

		return nil, false
	}

	return device, true
}

func hasSource(sources *models.Sources, name string) bool {
	if sources == nil {
		return false
	}

	for _, source := range sources.SourceItem {
		if source.Source == name {
			return true
		}
	}

	return false
}

func settingsSourceList(sources *models.Sources) []settingsSource {
	if sources == nil {
		return nil
	}

	result := make([]settingsSource, 0)

	for _, source := range sources.SourceItem {
		if source.Source != "AUX" {
			continue
		}

		result = append(result, settingsSource{
			Source:        source.Source,
			SourceAccount: source.SourceAccount,
			DisplayName:   source.GetDisplayName(),
		})
	}

	return result
}

func settingsConnectionStatus(nowPlaying *models.NowPlaying) (string, string) {
	if nowPlaying == nil || nowPlaying.ConnectionStatusInfo == nil {
		return "", ""
	}

	return nowPlaying.ConnectionStatusInfo.Status, nowPlaying.ConnectionStatusInfo.DeviceName
}

func canonicalFirmwareNetworkQuality(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.TrimSuffix(normalized, "_signal")

	switch normalized {
	case "excellent":
		return "Excellent", true
	case "good":
		return "Good", true
	case "fair":
		return "Fair", true
	case "marginal":
		return "Marginal", true
	case "poor":
		return "Poor", true
	default:
		return "", false
	}
}

func readBasicDeviceSettings(device *webtypes.DeviceConnection, snapshot *deviceSettingsSnapshot) {
	if snapshot.Support.ClockDisplay {
		value, readErr := device.Client.GetClockDisplay()
		if readErr != nil {
			snapshot.Errors["clockDisplay"] = readErr.Error()
		} else {
			snapshot.ClockDisplay = &settingsClockDisplay{
				Enabled:    value.Enabled,
				Format:     value.Format,
				Brightness: value.Brightness,
				TimeZone:   value.TimeZone,
			}
		}
	}

	if snapshot.Support.ClockTime {
		value, readErr := device.Client.GetClockTime()
		if readErr != nil {
			snapshot.Errors["clockTime"] = readErr.Error()
		} else {
			snapshot.ClockTime = &settingsClockTime{UTC: value.GetUTC(), Value: value.GetTimeString()}
		}
	}

	if snapshot.Support.SystemTimeout {
		value, readErr := device.Client.GetSystemTimeout()
		if readErr != nil {
			snapshot.Errors["systemTimeout"] = readErr.Error()
		} else {
			snapshot.SystemTimeout = &settingsSystemTimeout{Enabled: value.PowerSavingEnabled}
		}
	}

	if snapshot.Support.Language {
		value, readErr := device.Client.GetLanguage()
		if readErr != nil {
			snapshot.Errors["language"] = readErr.Error()
		} else {
			snapshot.Language = &settingsLanguage{
				Code:    int(value.Code),
				Options: supportedSystemLanguageOptions(value.Code),
			}
		}
	}
}

func readSyncDeviceSetting(device *webtypes.DeviceConnection, snapshot *deviceSettingsSnapshot) {
	if !snapshot.Support.Sync {
		return
	}

	value, readErr := device.Client.GetRebroadcastLatencyMode()
	switch {
	case readErr != nil:
		snapshot.Errors["sync"] = readErr.Error()
	case !value.Controllable:
		snapshot.Support.Sync = false
	default:
		snapshot.Sync = &settingsSync{Mode: string(value.Mode)}
	}
}

func readBluetoothDeviceSetting(device *webtypes.DeviceConnection, snapshot *deviceSettingsSnapshot) {
	if !snapshot.Support.Bluetooth {
		return
	}

	value, readErr := device.Client.GetBluetoothInfo()
	if readErr != nil {
		snapshot.Errors["bluetooth"] = readErr.Error()

		return
	}

	nowPlaying, nowPlayingErr := device.Client.GetNowPlaying()
	if nowPlayingErr != nil {
		snapshot.Errors["bluetooth"] = nowPlayingErr.Error()
	}

	status, name := settingsConnectionStatus(nowPlaying)
	snapshot.Bluetooth = &settingsBluetooth{
		MACAddress:       value.BluetoothMACAddress,
		ConnectionStatus: status,
		DeviceName:       name,
	}
}

func readNetworkDeviceSetting(
	device *webtypes.DeviceConnection,
	supportedURLs *models.SupportedURLsResponse,
	snapshot *deviceSettingsSnapshot,
) {
	if !supportedURLs.HasURL("/networkInfo") {
		return
	}

	value, readErr := device.Client.GetNetworkInfo()
	if readErr != nil {
		snapshot.Errors["network"] = readErr.Error()

		return
	}

	interfaces := value.GetInterfaces()
	network := &settingsNetwork{Interfaces: make([]settingsNetworkInterface, 0, len(interfaces))}
	connectedWiFiIndexes := make([]int, 0, 1)

	for index := range interfaces {
		iface := &interfaces[index]

		projected := settingsNetworkInterface{
			Type:       iface.Type,
			Name:       iface.Name,
			MACAddress: iface.MacAddress,
			IPAddress:  iface.IPAddress,
			SSID:       iface.SSID,
			Band:       iface.GetFrequencyBand(),
			State:      iface.State,
		}
		if iface.IsWiFi() && iface.IsConnected() {
			connectedWiFiIndexes = append(connectedWiFiIndexes, index)

			quality, valid := canonicalFirmwareNetworkQuality(iface.Signal)
			if valid {
				projected.FirmwareNetworkQuality = quality
				projected.FirmwareNetworkQualitySource = "networkInfo"
				projected.FirmwareNetworkQualityState = "reported"
			} else {
				projected.FirmwareNetworkQualityState = "unavailable"
			}
		}

		network.Interfaces = append(network.Interfaces, projected)
	}

	snapshot.Network = network

	if len(connectedWiFiIndexes) == 0 || !supportedURLs.HasURL("/netStats") {
		return
	}

	stats, statsErr := device.Client.GetNetworkStats()

	for _, index := range connectedWiFiIndexes {
		projected := &network.Interfaces[index]
		if statsErr != nil {
			if projected.FirmwareNetworkQuality != "" {
				projected.FirmwareNetworkQualityState = "fallback"
			}

			continue
		}

		connectedWiFi := &interfaces[index]

		activeWireless := stats.FindRunningWireless(connectedWiFi.IPAddress, connectedWiFi.SSID)
		if activeWireless == nil {
			if projected.FirmwareNetworkQuality != "" {
				projected.FirmwareNetworkQualityState = "fallback"
			}

			continue
		}

		quality, valid := activeWireless.CanonicalRSSI()
		if !valid {
			if projected.FirmwareNetworkQuality != "" {
				projected.FirmwareNetworkQualityState = "fallback"
			}

			continue
		}

		if projected.FirmwareNetworkQuality != "" && projected.FirmwareNetworkQuality != quality {
			projected.NetworkInfoFirmwareQuality = projected.FirmwareNetworkQuality
			projected.FirmwareNetworkQualityState = "conflict"
		} else {
			projected.FirmwareNetworkQualityState = "reported"
		}

		projected.FirmwareNetworkQuality = quality
		projected.FirmwareNetworkQualitySource = "netStats"
	}
}

func (app *WebApp) readDeviceSettings(device *webtypes.DeviceConnection) (*deviceSettingsSnapshot, error) {
	capabilities, err := device.Client.GetCapabilities()
	if err != nil {
		return nil, fmt.Errorf("read device capabilities: %w", err)
	}

	supportedURLs, err := device.Client.GetSupportedURLs()
	if err != nil {
		return nil, fmt.Errorf("read supported device endpoints: %w", err)
	}

	snapshot := &deviceSettingsSnapshot{Errors: make(map[string]string)}

	sources, sourcesErr := device.Client.GetSources()
	if sourcesErr != nil {
		snapshot.Errors["sources"] = sourcesErr.Error()
	}

	hasBluetooth := hasSource(sources, "BLUETOOTH")
	renameableSources := settingsSourceList(sources)
	snapshot.Sources = renameableSources

	snapshot.Support = settingsSupport{
		ClockDisplay:   capabilities.HasClockDisplay() && supportedURLs.HasURL("/clockDisplay"),
		ClockTime:      capabilities.HasClockDisplay() && supportedURLs.HasURL("/clockTime"),
		SystemTimeout:  capabilities.HasCapability("systemtimeout") && supportedURLs.HasURL("/systemtimeout"),
		Language:       supportedURLs.HasURL("/language"),
		Bluetooth:      hasBluetooth && supportedURLs.HasURL("/bluetoothInfo"),
		BluetoothPair:  hasBluetooth && supportedURLs.HasURL("/enterPairingMode"),
		BluetoothClear: hasBluetooth && supportedURLs.HasURL("/clearPairedList"),
		Sync:           capabilities.HasCapability("rebroadcastlatencymode") && supportedURLs.HasURL("/rebroadcastlatencymode"),
		SourceNaming:   len(renameableSources) > 0 && supportedURLs.HasURL("/nameSource"),
		WiFiOnboarding: capabilities.HasHostedWifiConfig(),
	}

	readBasicDeviceSettings(device, snapshot)
	readSyncDeviceSetting(device, snapshot)
	readBluetoothDeviceSetting(device, snapshot)
	readNetworkDeviceSetting(device, supportedURLs, snapshot)

	if snapshot.Support.WiFiOnboarding && app.OnboardingURL != "" {
		snapshot.OnboardingURL = app.OnboardingURL
	}

	if len(snapshot.Errors) == 0 {
		snapshot.Errors = nil
	}

	return snapshot, nil
}

func (app *WebApp) writeDeviceSettings(w http.ResponseWriter, snapshot *deviceSettingsSnapshot) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(webtypes.APIResponse{Success: true, Data: snapshot}); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func (app *WebApp) refreshDeviceSettings(w http.ResponseWriter, device *webtypes.DeviceConnection) {
	snapshot, err := app.readDeviceSettings(device)
	if err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	app.writeDeviceSettings(w, snapshot)
}

// HandleGetDeviceSettings returns capability-gated settings with fresh readback.
func (app *WebApp) HandleGetDeviceSettings(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	app.refreshDeviceSettings(w, device)
}

func decodeSettingsRequest(r *http.Request, target interface{}) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return err
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain one JSON object")
		}

		return err
	}

	return nil
}

func (app *WebApp) requireSetting(
	w http.ResponseWriter,
	device *webtypes.DeviceConnection,
	predicate func(settingsSupport) bool,
	name string,
) bool {
	snapshot, err := app.readDeviceSettings(device)
	if err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return false
	}

	if !predicate(snapshot.Support) {
		app.sendError(w, name+" is not supported by this device", http.StatusConflict)

		return false
	}

	return true
}

type clockDisplaySettingsRequest struct {
	Enabled  *bool  `json:"enabled"`
	Format   string `json:"format"`
	TimeZone string `json:"timeZone"`
}

func (app *WebApp) requireClockDisplayReadback(
	w http.ResponseWriter,
	device *webtypes.DeviceConnection,
	request clockDisplaySettingsRequest,
) bool {
	snapshot, err := app.readDeviceSettings(device)
	if err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return false
	}

	if !snapshot.Support.ClockDisplay {
		app.sendError(w, "Clock display is not supported by this device", http.StatusConflict)

		return false
	}

	if snapshot.ClockDisplay == nil {
		message := "Clock display readback is unavailable"
		if readErr := snapshot.Errors["clockDisplay"]; readErr != "" {
			message += ": " + readErr
		}

		app.sendError(w, message, http.StatusBadGateway)

		return false
	}

	if request.Format != "" && snapshot.ClockDisplay.Format == "" {
		app.sendError(w, "Time format is not reported as supported by this device", http.StatusConflict)

		return false
	}

	if request.TimeZone != "" && strings.TrimSpace(snapshot.ClockDisplay.TimeZone) == "" {
		app.sendError(w, "Timezone is not reported as supported by this device", http.StatusConflict)

		return false
	}

	return true
}

// HandleSetClockDisplay updates supported display fields and verifies them.
func (app *WebApp) HandleSetClockDisplay(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	var body clockDisplaySettingsRequest
	if err := decodeSettingsRequest(r, &body); err != nil {
		app.sendError(w, "Invalid clock display request: "+err.Error(), http.StatusBadRequest)

		return
	}

	request := models.NewClockDisplayRequest()
	if body.Enabled != nil {
		request.SetEnabled(*body.Enabled)
	}

	if body.Format != "" {
		request.SetFormat(models.ClockFormat(body.Format))
	}

	if body.TimeZone != "" {
		request.SetTimeZone(strings.TrimSpace(body.TimeZone))
	}

	if !request.HasChanges() {
		app.sendError(w, "No clock display changes specified", http.StatusBadRequest)

		return
	}

	if !app.requireClockDisplayReadback(w, device, body) {
		return
	}

	if err := device.Client.SetClockDisplay(request); err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	readback, err := device.Client.GetClockDisplay()
	if err != nil || !clockDisplayMatches(readback, body) {
		app.sendError(w, "Speaker did not confirm the clock display change", http.StatusBadGateway)

		return
	}

	app.refreshDeviceSettings(w, device)
}

func clockDisplayMatches(value *models.ClockDisplay, request clockDisplaySettingsRequest) bool {
	if value == nil {
		return false
	}

	if request.Enabled != nil && value.Enabled != *request.Enabled {
		return false
	}

	if request.Format != "" && value.Format != request.Format {
		return false
	}

	return request.TimeZone == "" || value.TimeZone == strings.TrimSpace(request.TimeZone)
}

// HandleSetClockTime synchronizes the speaker clock and verifies its timestamp.
func (app *WebApp) HandleSetClockTime(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	snapshot, err := app.readDeviceSettings(device)
	if err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	if !snapshot.Support.ClockTime {
		app.sendError(w, "Clock synchronization is not supported by this device", http.StatusConflict)

		return
	}

	if snapshot.ClockTime == nil {
		message := "Clock time readback is unavailable"
		if readErr := snapshot.Errors["clockTime"]; readErr != "" {
			message += ": " + readErr
		}

		app.sendError(w, message, http.StatusBadGateway)

		return
	}

	if snapshot.ClockTime.UTC == 0 && strings.TrimSpace(snapshot.ClockTime.Value) == "" {
		app.sendError(w, "Current time is not reported as supported by this device", http.StatusConflict)

		return
	}

	started := time.Now()

	if setErr := device.Client.SetClockTimeNow(); setErr != nil {
		app.sendError(w, setErr.Error(), http.StatusBadGateway)

		return
	}

	readback, err := device.Client.GetClockTime()
	if err != nil || readback.GetUTC() == 0 || math.Abs(float64(readback.GetUTC()-started.Unix())) > 60 {
		app.sendError(w, "Speaker did not confirm the clock synchronization", http.StatusBadGateway)

		return
	}

	app.refreshDeviceSettings(w, device)
}

type toggleSettingsRequest struct {
	Enabled *bool `json:"enabled"`
}

// HandleSetSystemTimeout updates automatic standby and verifies readback.
func (app *WebApp) HandleSetSystemTimeout(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	var body toggleSettingsRequest
	if err := decodeSettingsRequest(r, &body); err != nil || body.Enabled == nil {
		app.sendError(w, "Automatic standby requires an enabled boolean", http.StatusBadRequest)

		return
	}

	if !app.requireSetting(w, device, func(s settingsSupport) bool { return s.SystemTimeout }, "Automatic standby") {
		return
	}

	if err := device.Client.SetSystemTimeout(&models.SystemTimeout{PowerSavingEnabled: *body.Enabled}); err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	readback, err := device.Client.GetSystemTimeout()
	if err != nil || readback.PowerSavingEnabled != *body.Enabled {
		app.sendError(w, "Speaker did not confirm the automatic standby change", http.StatusBadGateway)

		return
	}

	app.refreshDeviceSettings(w, device)
}

type languageSettingsRequest struct {
	Code int `json:"code"`
}

// HandleSetSystemLanguage updates a validated firmware language code.
func (app *WebApp) HandleSetSystemLanguage(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	var body languageSettingsRequest
	if err := decodeSettingsRequest(r, &body); err != nil {
		app.sendError(w, "Invalid language request: "+err.Error(), http.StatusBadRequest)

		return
	}

	if err := models.LanguageCode(body.Code).Validate(); err != nil {
		app.sendError(w, err.Error(), http.StatusBadRequest)

		return
	}

	if !app.requireSetting(w, device, func(s settingsSupport) bool { return s.Language }, "Language control") {
		return
	}

	if err := device.Client.SetLanguage(models.LanguageCode(body.Code)); err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	readback, err := device.Client.GetLanguage()
	if err != nil || int(readback.Code) != body.Code {
		app.sendError(w, "Speaker did not confirm the language change", http.StatusBadGateway)

		return
	}

	app.refreshDeviceSettings(w, device)
}

type syncSettingsRequest struct {
	Mode string `json:"mode"`
}

// HandleSetRebroadcastLatencyMode updates and verifies the sync priority.
func (app *WebApp) HandleSetRebroadcastLatencyMode(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	var body syncSettingsRequest
	if err := decodeSettingsRequest(r, &body); err != nil {
		app.sendError(w, "Invalid sync request: "+err.Error(), http.StatusBadRequest)

		return
	}

	if err := models.RebroadcastLatencyModeValue(body.Mode).Validate(); err != nil {
		app.sendError(w, err.Error(), http.StatusBadRequest)

		return
	}

	if !app.requireSetting(w, device, func(s settingsSupport) bool { return s.Sync }, "Sync priority") {
		return
	}

	if err := device.Client.SetRebroadcastLatencyMode(models.RebroadcastLatencyModeValue(body.Mode)); err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	readback, err := device.Client.GetRebroadcastLatencyMode()
	if err != nil || string(readback.Mode) != body.Mode {
		app.sendError(w, "Speaker did not confirm the sync-priority change", http.StatusBadGateway)

		return
	}

	app.refreshDeviceSettings(w, device)
}

// HandleEnterBluetoothPairing requests and verifies discoverable mode.
func (app *WebApp) HandleEnterBluetoothPairing(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	if !app.requireSetting(w, device, func(s settingsSupport) bool { return s.BluetoothPair }, "Bluetooth pairing") {
		return
	}

	if err := device.Client.EnterPairingMode(); err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	nowPlaying, err := device.Client.GetNowPlaying()
	if err != nil || nowPlaying.ConnectionStatusInfo == nil || nowPlaying.ConnectionStatusInfo.Status != "DISCOVERABLE" {
		app.sendError(w, "Speaker did not confirm Bluetooth pairing mode", http.StatusBadGateway)

		return
	}

	device.UpdateStatus(func(status *webtypes.DeviceStatus) { status.NowPlaying = nowPlaying })
	app.refreshDeviceSettings(w, device)
}

// HandleClearBluetoothPairings clears pairings without claiming unobservable success.
func (app *WebApp) HandleClearBluetoothPairings(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	if !app.requireSetting(w, device, func(s settingsSupport) bool { return s.BluetoothClear }, "Bluetooth paired-device clearing") {
		return
	}

	if err := device.Client.ClearPairedList(); err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	snapshot, err := app.readDeviceSettings(device)
	if err != nil {
		app.sendError(w, err.Error(), http.StatusBadGateway)

		return
	}

	if snapshot.Errors == nil {
		snapshot.Errors = make(map[string]string)
	}

	snapshot.Errors["bluetoothClear"] = "The speaker accepted the clear command but exposes no paired-list readback."

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	if err := json.NewEncoder(w).Encode(struct {
		Success bool                    `json:"success"`
		Outcome string                  `json:"outcome"`
		Data    *deviceSettingsSnapshot `json:"data"`
		Error   string                  `json:"error"`
	}{
		Success: false,
		Outcome: "unverified",
		Data:    snapshot,
		Error:   snapshot.Errors["bluetoothClear"],
	}); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

type sourceNameSettingsRequest struct {
	Source        string `json:"source"`
	SourceAccount string `json:"sourceAccount"`
	Name          string `json:"name"`
}

// HandleSetSourceName renames a supported source and verifies the new name.
func (app *WebApp) HandleSetSourceName(w http.ResponseWriter, r *http.Request) {
	device, ok := app.settingsDevice(w, r)
	if !ok {
		return
	}

	var body sourceNameSettingsRequest
	if err := decodeSettingsRequest(r, &body); err != nil {
		app.sendError(w, "Invalid source-name request: "+err.Error(), http.StatusBadRequest)

		return
	}

	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		app.sendError(w, "Source name must not be empty", http.StatusBadRequest)

		return
	}

	if !app.requireSetting(w, device, func(s settingsSupport) bool { return s.SourceNaming }, "Source naming") {
		return
	}

	sources, err := device.Client.GetSources()
	if err != nil || !sourceIdentityExists(sources, body.Source, body.SourceAccount) {
		app.sendError(w, "The requested source cannot be renamed on this device", http.StatusConflict)

		return
	}

	renameErr := device.Client.RenameSource(body.Source, body.SourceAccount, body.Name)
	if renameErr != nil {
		app.sendError(w, renameErr.Error(), http.StatusBadGateway)

		return
	}

	sources, err = device.Client.GetSources()
	if err != nil || !sourceNameMatches(sources, body) {
		app.sendError(w, "Speaker did not confirm the source name", http.StatusBadGateway)

		return
	}

	app.refreshDeviceSettings(w, device)
}

func sourceIdentityExists(sources *models.Sources, sourceName, sourceAccount string) bool {
	if sources == nil || sourceName != "AUX" {
		return false
	}

	for _, source := range sources.SourceItem {
		if source.Source == sourceName && source.SourceAccount == sourceAccount {
			return true
		}
	}

	return false
}

func sourceNameMatches(sources *models.Sources, request sourceNameSettingsRequest) bool {
	if sources == nil {
		return false
	}

	for _, source := range sources.SourceItem {
		if source.Source == request.Source && source.SourceAccount == request.SourceAccount {
			return source.DisplayName == request.Name
		}
	}

	return false
}

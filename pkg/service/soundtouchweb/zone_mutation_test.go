package soundtouchweb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/gorilla/websocket"
)

func zoneMutationTestDevice(host, deviceID, name string) *webtypes.DeviceConnection {
	connection := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: host}),
		&models.DeviceInfo{Name: name, DeviceID: deviceID},
	)
	connection.SetStatus(&webtypes.DeviceStatus{IsConnected: true})

	return connection
}

func TestHandleZoneAddConfirmsTopologyBeforeSuccess(t *testing.T) {
	var mutated atomic.Bool
	var setCalls atomic.Int32
	var masterReads atomic.Int32
	var slaveReads atomic.Int32

	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getZone":
			masterReads.Add(1)
			w.Header().Set("Content-Type", "application/xml")
			if mutated.Load() {
				_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
			} else {
				_, _ = w.Write([]byte(`<zone master="MASTER"/>`))
			}
		case "/setZone":
			setCalls.Add(1)
			mutated.Store(true)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer masterSpeaker.Close()

	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}
		slaveReads.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
	}))
	defer slaveSpeaker.Close()

	app := NewWebApp()
	master := zoneMutationTestDevice(masterSpeaker.URL, "MASTER", "Master")
	slave := zoneMutationTestDevice(slaveSpeaker.URL, "SLAVE", "Slave")
	app.AddDevice("192.0.2.10", master)
	app.AddDevice("192.0.2.20", slave)

	request := withChiParams(
		httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/add/192.0.2.20", nil),
		map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.20"},
	)
	response := httptest.NewRecorder()
	app.HandleZoneAdd(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("HandleZoneAdd status = %d, body=%s", response.Code, response.Body.String())
	}
	if setCalls.Load() != 1 || masterReads.Load() != 2 || slaveReads.Load() != 1 {
		t.Fatalf("calls set=%d master reads=%d slave reads=%d, want 1/2/1",
			setCalls.Load(), masterReads.Load(), slaveReads.Load())
	}
	if zone := master.Status().Zone; zone == nil || zone.Master != "MASTER" || !zone.IsMember("SLAVE") {
		t.Fatalf("confirmed master topology was not cached: %+v", zone)
	}
}

func TestHandleZoneDissolveConfirmsEveryMemberStandalone(t *testing.T) {
	var mutated atomic.Bool
	var setCalls atomic.Int32

	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getZone":
			w.Header().Set("Content-Type", "application/xml")
			if mutated.Load() {
				_, _ = w.Write([]byte(`<zone master="MASTER"/>`))
			} else {
				_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
			}
		case "/setZone":
			setCalls.Add(1)
			mutated.Store(true)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer masterSpeaker.Close()

	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="SLAVE"/>`))
	}))
	defer slaveSpeaker.Close()

	initialZone := &models.ZoneInfo{
		Master:  "MASTER",
		Members: []models.Member{{DeviceID: "SLAVE", IP: "192.0.2.20"}},
	}
	app := NewWebApp()
	master := zoneMutationTestDevice(masterSpeaker.URL, "MASTER", "Master")
	master.SetStatus(&webtypes.DeviceStatus{IsConnected: true, Zone: initialZone})
	slave := zoneMutationTestDevice(slaveSpeaker.URL, "SLAVE", "Slave")
	slave.SetStatus(&webtypes.DeviceStatus{IsConnected: true, Zone: initialZone})
	app.AddDevice("192.0.2.10", master)
	app.AddDevice("192.0.2.20", slave)

	request := withChiParams(
		httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/dissolve", nil),
		map[string]string{"id": "192.0.2.10"},
	)
	response := httptest.NewRecorder()
	app.HandleZoneDissolve(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("HandleZoneDissolve status = %d, body=%s", response.Code, response.Body.String())
	}
	if setCalls.Load() != 1 {
		t.Fatalf("setZone calls = %d, want 1", setCalls.Load())
	}
	if master.Status().Zone != nil || slave.Status().Zone != nil {
		t.Fatalf("dissolved topology remained cached: master=%+v slave=%+v",
			master.Status().Zone, slave.Status().Zone)
	}
}

func TestHandleZoneAddTransportFailureConfirmedByReadback(t *testing.T) {
	var mutated atomic.Bool
	var setCalls atomic.Int32
	var masterReads atomic.Int32
	var slaveReads atomic.Int32

	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getZone":
			masterReads.Add(1)
			w.Header().Set("Content-Type", "application/xml")
			if mutated.Load() {
				_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
			} else {
				_, _ = w.Write([]byte(`<zone master="MASTER"/>`))
			}
		case "/setZone":
			setCalls.Add(1)
			mutated.Store(true)
			http.Error(w, "write failed", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer masterSpeaker.Close()
	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}
		slaveReads.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
	}))
	defer slaveSpeaker.Close()

	app := NewWebApp()
	app.AddDevice("192.0.2.10", zoneMutationTestDevice(masterSpeaker.URL, "MASTER", "Master"))
	app.AddDevice("192.0.2.20", zoneMutationTestDevice(slaveSpeaker.URL, "SLAVE", "Slave"))
	request := withChiParams(
		httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/add/192.0.2.20", nil),
		map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.20"},
	)
	response := httptest.NewRecorder()
	app.HandleZoneAdd(response, request)

	if response.Code != http.StatusOK || !mutated.Load() || setCalls.Load() != 1 {
		t.Fatalf("transport failure response=%d set=%d body=%s",
			response.Code, setCalls.Load(), response.Body.String())
	}
	if masterReads.Load() != 2 || slaveReads.Load() != 1 {
		t.Fatalf("readbacks master=%d slave=%d, want 2/1", masterReads.Load(), slaveReads.Load())
	}
}

func TestHandleZoneAddTransportFailureRemainsUnverifiedOnMismatch(t *testing.T) {
	var setCalls atomic.Int32
	var slaveReads atomic.Int32

	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getZone":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<zone master="MASTER"/>`))
		case "/setZone":
			setCalls.Add(1)
			http.Error(w, "write failed", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer masterSpeaker.Close()
	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}
		slaveReads.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="SLAVE"/>`))
	}))
	defer slaveSpeaker.Close()

	app := NewWebApp()
	app.AddDevice("192.0.2.10", zoneMutationTestDevice(masterSpeaker.URL, "MASTER", "Master"))
	app.AddDevice("192.0.2.20", zoneMutationTestDevice(slaveSpeaker.URL, "SLAVE", "Slave"))
	request := withChiParams(
		httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/add/192.0.2.20", nil),
		map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.20"},
	)
	response := httptest.NewRecorder()
	app.HandleZoneAdd(response, request)

	if response.Code != http.StatusBadGateway || setCalls.Load() != 1 || slaveReads.Load() != 1 {
		t.Fatalf("unverified response=%d set=%d slave reads=%d body=%s",
			response.Code, setCalls.Load(), slaveReads.Load(), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "mutation transport") {
		t.Fatalf("response does not preserve transport uncertainty: %s", response.Body.String())
	}
}

func TestRunZoneMutationRejectsSupersededReadback(t *testing.T) {
	masterReadStarted := make(chan struct{})
	releaseMasterRead := make(chan struct{})

	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}
		close(masterReadStarted)
		<-releaseMasterRead
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
	}))
	defer masterSpeaker.Close()
	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
	}))
	defer slaveSpeaker.Close()

	app := NewWebApp()
	master := zoneMutationTestDevice(masterSpeaker.URL, "MASTER", "Master")
	app.AddDevice("192.0.2.10", master)
	app.AddDevice("192.0.2.20", zoneMutationTestDevice(slaveSpeaker.URL, "SLAVE", "Slave"))
	request := models.NewZoneRequest("MASTER")
	request.AddMember("SLAVE", "192.0.2.20")
	affected, expectations, fallback := zoneAddMutationPlan(request, "SLAVE")
	readbacks := app.prepareZoneMutationReadbacks(affected, expectations, fallback)

	result := make(chan error, 1)
	go func() {
		result <- app.runZoneMutation(readbacks, func() error { return nil })
	}()
	<-masterReadStarted
	master.BeginZoneRefresh()
	close(releaseMasterRead)
	err := <-result

	if _, ok := err.(*zoneMutationVerificationError); !ok || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("superseded readback error = %v", err)
	}
}

func TestRunZoneMutationReportsPartialReadbackAfterApplyingMaster(t *testing.T) {
	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
	}))
	defer masterSpeaker.Close()
	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer slaveSpeaker.Close()

	app := NewWebApp()
	master := zoneMutationTestDevice(masterSpeaker.URL, "MASTER", "Master")
	app.AddDevice("192.0.2.10", master)
	app.AddDevice("192.0.2.20", zoneMutationTestDevice(slaveSpeaker.URL, "SLAVE", "Slave"))
	request := models.NewZoneRequest("MASTER")
	request.AddMember("SLAVE", "192.0.2.20")
	affected, expectations, fallback := zoneAddMutationPlan(request, "SLAVE")
	readbacks := app.prepareZoneMutationReadbacks(affected, expectations, fallback)

	err := app.runZoneMutation(readbacks, func() error { return nil })
	if _, ok := err.(*zoneMutationVerificationError); !ok || !strings.Contains(err.Error(), "SLAVE") {
		t.Fatalf("partial readback error = %v", err)
	}
	if zone := master.Status().Zone; zone == nil || !zone.IsMember("SLAVE") {
		t.Fatalf("successful master projection was not retained: %+v", zone)
	}
}

func TestRunZoneMutationRejectsUnrelatedCachedMasterReadback(t *testing.T) {
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="OTHER"><member>THIRD</member></zone>`))
	}))
	defer speaker.Close()

	initialZone := &models.ZoneInfo{
		Master:  "CACHED",
		Members: []models.Member{{DeviceID: "REMOVED"}},
	}
	connection := zoneMutationTestDevice(speaker.URL, "CACHED", "Cached master")
	connection.SetStatus(&webtypes.DeviceStatus{IsConnected: true, Zone: initialZone})
	app := NewWebApp()
	app.AddDevice("192.0.2.30", connection)
	readbacks := []pendingZoneMutationReadback{{
		deviceID:   "CACHED",
		connection: connection,
		generation: connection.BeginZoneRefresh(),
		expect:     expectDevicesAbsent("CACHED", []string{"REMOVED"}),
	}}

	err := app.runZoneMutation(readbacks, func() error { return nil })
	if _, ok := err.(*zoneMutationVerificationError); !ok {
		t.Fatalf("unrelated readback error = %v", err)
	}
	if got := connection.Status().Zone; got == nil || got.Master != "CACHED" {
		t.Fatalf("unrelated readback cleared cached topology: %+v", got)
	}
}

func TestRunZoneMutationPublishesMasterBeforeSlowMember(t *testing.T) {
	releaseSlaveRead := make(chan struct{})
	var releaseSlaveOnce sync.Once
	slaveReadStarted := make(chan struct{})

	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
	}))
	defer masterSpeaker.Close()
	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(slaveReadStarted)
		<-releaseSlaveRead
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<zone master="MASTER"><member ipaddress="192.0.2.20">SLAVE</member></zone>`))
	}))
	defer slaveSpeaker.Close()
	defer releaseSlaveOnce.Do(func() { close(releaseSlaveRead) })

	app := NewWebApp()
	master := zoneMutationTestDevice(masterSpeaker.URL, "MASTER", "Master")
	app.AddDevice("192.0.2.10", master)
	app.AddDevice("192.0.2.20", zoneMutationTestDevice(slaveSpeaker.URL, "SLAVE", "Slave"))

	webSocketServer := httptest.NewServer(http.HandlerFunc(app.HandleWebSocket))
	defer webSocketServer.Close()
	webSocketURL := "ws" + strings.TrimPrefix(webSocketServer.URL, "http")
	webSocketClient, _, err := websocket.DefaultDialer.Dial(webSocketURL, nil)
	if err != nil {
		t.Fatalf("connect player WebSocket: %v", err)
	}
	defer webSocketClient.Close()
	var initial webtypes.WebSocketMessage
	if err := webSocketClient.ReadJSON(&initial); err != nil {
		t.Fatalf("read initial player projection: %v", err)
	}

	request := models.NewZoneRequest("MASTER")
	request.AddMember("SLAVE", "192.0.2.20")
	affected, expectations, fallback := zoneAddMutationPlan(request, "SLAVE")
	readbacks := app.prepareZoneMutationReadbacks(affected, expectations, fallback)
	result := make(chan error, 1)
	go func() {
		result <- app.runZoneMutation(readbacks, func() error { return nil })
	}()
	<-slaveReadStarted

	if err := webSocketClient.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set WebSocket deadline: %v", err)
	}
	var update webtypes.WebSocketMessage
	if err := webSocketClient.ReadJSON(&update); err != nil {
		t.Fatalf("master projection was not broadcast while member was blocked: %v", err)
	}
	if update.Type != "devices" {
		t.Fatalf("broadcast type = %q, want devices", update.Type)
	}
	if zone := master.Status().Zone; zone == nil || !zone.IsMember("SLAVE") {
		t.Fatalf("master projection was not applied before member completed: %+v", zone)
	}
	select {
	case err := <-result:
		t.Fatalf("mutation returned before member readback was released: %v", err)
	default:
	}

	releaseSlaveOnce.Do(func() { close(releaseSlaveRead) })
	if err := <-result; err != nil {
		t.Fatalf("mutation result = %v", err)
	}
}

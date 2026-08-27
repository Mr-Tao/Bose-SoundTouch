package soundtouchweb

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/gorilla/websocket"
)

type balanceTestSpeaker struct {
	server            *httptest.Server
	mu                sync.Mutex
	balance           int
	target            *int
	actual            *int
	balanceAvailable  bool
	balanceMin        int
	balanceMax        int
	balanceDefault    int
	writeStatus       int
	readStatus        int
	applyWriteOnError bool
	posts             []int
	balanceGets       int
	postStarted       chan int
	releaseFirst      chan struct{}
	getStarted        chan struct{}
	releaseGet        chan struct{}
	group             *models.Group
	groupStatus       int
	groupStarted      chan struct{}
	releaseGroup      chan struct{}
}

func newBalanceTestSpeaker(t *testing.T, initial int) *balanceTestSpeaker {
	t.Helper()

	speaker := &balanceTestSpeaker{
		balance:          initial,
		balanceAvailable: true,
		balanceMin:       -7,
		balanceMax:       7,
		balanceDefault:   0,
	}
	speaker.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/balance":
			var request models.BalanceRequest
			if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			speaker.mu.Lock()
			speaker.posts = append(speaker.posts, request.Level)
			postNumber := len(speaker.posts)
			status := speaker.writeStatus
			started := speaker.postStarted
			releaseFirst := speaker.releaseFirst
			if status == 0 || status == http.StatusOK || speaker.applyWriteOnError {
				speaker.balance = request.Level
			}
			speaker.mu.Unlock()

			if started != nil {
				started <- postNumber
			}
			if postNumber == 1 && releaseFirst != nil {
				<-releaseFirst
			}
			if status != 0 && status != http.StatusOK {
				http.Error(w, "write failed", status)
				return
			}
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && r.URL.Path == "/balance":
			speaker.mu.Lock()
			speaker.balanceGets++
			status := speaker.readStatus
			available := speaker.balanceAvailable
			minLevel := speaker.balanceMin
			maxLevel := speaker.balanceMax
			defaultLevel := speaker.balanceDefault
			target := speaker.balance
			actual := speaker.balance
			started := speaker.getStarted
			release := speaker.releaseGet
			if speaker.target != nil {
				target = *speaker.target
			}
			if speaker.actual != nil {
				actual = *speaker.actual
			}
			speaker.mu.Unlock()
			if started != nil {
				started <- struct{}{}
			}
			if release != nil {
				<-release
			}
			if status != 0 && status != http.StatusOK {
				http.Error(w, "read failed", status)
				return
			}
			_, _ = fmt.Fprintf(w, `<balance deviceID="LEFT"><balanceAvailable>%t</balanceAvailable><balanceMin>%d</balanceMin><balanceMax>%d</balanceMax><balanceDefault>%d</balanceDefault><targetBalance>%d</targetBalance><actualBalance>%d</actualBalance></balance>`, available, minLevel, maxLevel, defaultLevel, target, actual)

		case r.Method == http.MethodGet && r.URL.Path == "/getGroup":
			speaker.mu.Lock()
			group := speaker.group
			status := speaker.groupStatus
			started := speaker.groupStarted
			release := speaker.releaseGroup
			speaker.mu.Unlock()
			if started != nil {
				started <- struct{}{}
			}
			if release != nil {
				<-release
			}
			if status != 0 && status != http.StatusOK {
				http.Error(w, "group failed", status)
				return
			}
			if group == nil {
				group = &models.Group{}
			}
			if err := xml.NewEncoder(w).Encode(group); err != nil {
				t.Errorf("encode group: %v", err)
			}

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(speaker.server.Close)

	return speaker
}

func (speaker *balanceTestSpeaker) postedLevels() []int {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return append([]int(nil), speaker.posts...)
}

func (speaker *balanceTestSpeaker) getCount() int {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return speaker.balanceGets
}

func (speaker *balanceTestSpeaker) cachedBalance() *models.Balance {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return &models.Balance{
		DeviceID:         "LEFT",
		BalanceAvailable: speaker.balanceAvailable,
		BalanceMin:       speaker.balanceMin,
		BalanceMax:       speaker.balanceMax,
		BalanceDefault:   speaker.balanceDefault,
		TargetBalance:    speaker.balance,
		ActualBalance:    speaker.balance,
		CapabilityKnown:  true,
	}
}

func stereoBalanceGroup(masterRole string) *models.Group {
	return &models.Group{
		ID:             "pair-1",
		Name:           "Living Room",
		MasterDeviceID: "LEFT",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "LEFT", Role: masterRole, IPAddress: "192.0.2.10"},
			{DeviceID: "RIGHT", Role: map[string]string{"LEFT": "RIGHT", "RIGHT": "LEFT"}[masterRole], IPAddress: "192.0.2.20"},
		}},
		Status: "GROUP_OK",
	}
}

func addStereoBalancePair(app *WebApp, speaker *balanceTestSpeaker, masterRole string) (*webtypes.DeviceConnection, *webtypes.DeviceConnection) {
	group := stereoBalanceGroup(masterRole)
	speaker.mu.Lock()
	speaker.group = group
	speaker.mu.Unlock()
	left := webtypes.NewDeviceConnection(
		client.NewClientFromHost(speaker.server.URL),
		&models.DeviceInfo{DeviceID: "LEFT", Name: "Living Room", Type: "SoundTouch 10", IPAddress: "192.0.2.10"},
	)
	right := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID: "RIGHT", Name: "Living Room Right", Type: "SoundTouch 10", IPAddress: "192.0.2.20",
	})
	left.SetStatus(&webtypes.DeviceStatus{Group: group, Balance: speaker.cachedBalance(), IsConnected: true, Connectivity: webtypes.ConnectivityOnline})
	right.SetStatus(&webtypes.DeviceStatus{Group: group, IsConnected: true, Connectivity: webtypes.ConnectivityOnline})
	app.AddDevice("192.0.2.10", left)
	app.AddDevice("192.0.2.20", right)

	return left, right
}

func stereoBalanceRequest(level string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/stereo-pair/balance/"+level, nil)

	return withChiParams(request, map[string]string{"id": "192.0.2.10", "level": level})
}

func decodeStereoBalanceResponse(t *testing.T, recorder *httptest.ResponseRecorder) (webtypes.APIResponse, stereoBalanceResult) {
	t.Helper()

	var payload struct {
		Success bool                `json:"success"`
		Data    stereoBalanceResult `json:"data"`
		Error   string              `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	return webtypes.APIResponse{Success: payload.Success, Error: payload.Error}, payload.Data
}

func addBalanceBroadcastClient(t *testing.T, app *WebApp) *websocket.Conn {
	t.Helper()

	serverConnection := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := app.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade broadcast client: %v", err)
			return
		}
		serverConnection <- conn
	}))
	clientConnection, response, err := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		server.Close()
		t.Fatalf("dial broadcast client: %v", err)
	}
	peer := <-serverConnection
	app.WSMutex.Lock()
	app.WSClients[peer] = true
	app.WSMutex.Unlock()

	t.Cleanup(func() {
		app.WSMutex.Lock()
		delete(app.WSClients, peer)
		app.WSMutex.Unlock()
		_ = peer.Close()
		_ = clientConnection.Close()
		server.Close()
	})

	return clientConnection
}

func TestHandleStereoBalanceSetsMasterReadsBackAndProjectsStatus(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 0)
	app := NewWebApp()
	left, _ := addStereoBalancePair(app, speaker, "LEFT")

	response := httptest.NewRecorder()
	app.HandleStereoBalance(response, stereoBalanceRequest("6"))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	payload, result := decodeStereoBalanceResponse(t, response)
	if !payload.Success || result.Requested != 6 || result.Target == nil || result.Actual == nil ||
		*result.Target != 6 || *result.Actual != 6 || !result.AtTarget {
		t.Fatalf("unexpected response: success=%v data=%+v error=%q", payload.Success, result, payload.Error)
	}
	if got := speaker.postedLevels(); fmt.Sprint(got) != "[6]" {
		t.Fatalf("balance posts = %v, want [6]", got)
	}
	if got := left.Status().Balance; got == nil || got.TargetBalance != 6 || got.ActualBalance != 6 {
		t.Fatalf("cached balance = %+v, want confirmed readback 6", got)
	}
	view := app.deviceViewSnapshot()["192.0.2.10"]
	if view.Status == nil || view.Status.Balance == nil || view.Status.Balance.ActualBalance != 6 {
		t.Fatalf("projected balance = %+v, want 6", view.Status)
	}
}

func TestHandleStereoBalanceRejectsInvalidLevel(t *testing.T) {
	for _, level := range []string{"1.5", "nope"} {
		t.Run(level, func(t *testing.T) {
			app := NewWebApp()
			response := httptest.NewRecorder()
			app.HandleStereoBalance(response, stereoBalanceRequest(level))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandleStereoBalanceUsesConfirmedCapabilityRange(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		speaker.balanceAvailable = false
		app := NewWebApp()
		addStereoBalancePair(app, speaker, "LEFT")

		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("0"))
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
		}
		if len(speaker.postedLevels()) != 0 || speaker.getCount() != 0 {
			t.Fatal("unavailable capability reached the balance endpoint")
		}
	})

	for _, level := range []string{"-8", "8"} {
		t.Run("outside ST10 range "+level, func(t *testing.T) {
			speaker := newBalanceTestSpeaker(t, 0)
			app := NewWebApp()
			addStereoBalancePair(app, speaker, "LEFT")

			response := httptest.NewRecorder()
			app.HandleStereoBalance(response, stereoBalanceRequest(level))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
			if len(speaker.postedLevels()) != 0 || speaker.getCount() != 0 {
				t.Fatal("out-of-range request reached the balance endpoint")
			}
		})
	}

	t.Run("different advertised range", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		speaker.balanceMin = -12
		speaker.balanceMax = 9
		speaker.balanceDefault = 1
		app := NewWebApp()
		addStereoBalancePair(app, speaker, "LEFT")

		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("-10"))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
		}
		if got := speaker.postedLevels(); fmt.Sprint(got) != "[-10]" {
			t.Fatalf("balance posts = %v, want [-10]", got)
		}
	})

	for _, test := range []struct {
		name    string
		balance *models.Balance
	}{
		{name: "unknown", balance: &models.Balance{TargetBalance: 0, ActualBalance: 0}},
		{name: "malformed", balance: &models.Balance{
			BalanceAvailable: true,
			BalanceMin:       7,
			BalanceMax:       -7,
			BalanceDefault:   0,
			CapabilityKnown:  true,
		}},
	} {
		t.Run(test.name+" capability", func(t *testing.T) {
			speaker := newBalanceTestSpeaker(t, 0)
			app := NewWebApp()
			left, _ := addStereoBalancePair(app, speaker, "LEFT")
			left.UpdateStatus(func(status *webtypes.DeviceStatus) {
				status.Balance = test.balance
			})

			response := httptest.NewRecorder()
			app.HandleStereoBalance(response, stereoBalanceRequest("0"))
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
			}
			if len(speaker.postedLevels()) != 0 || speaker.getCount() != 0 {
				t.Fatal("unsafe capability reached the balance endpoint")
			}
		})
	}
}

func TestHandleStereoBalanceRequiresConfirmedMasterCard(t *testing.T) {
	t.Run("ordinary zone", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		app := NewWebApp()
		conn := webtypes.NewDeviceConnection(client.NewClientFromHost(speaker.server.URL), &models.DeviceInfo{
			DeviceID: "LEFT", Type: "SoundTouch 10", IPAddress: "192.0.2.10",
		})
		conn.SetStatus(&webtypes.DeviceStatus{Zone: &models.ZoneInfo{Master: "LEFT"}, IsConnected: true})
		app.AddDevice("192.0.2.10", conn)

		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("0"))
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
		}
		if posts := speaker.postedLevels(); len(posts) != 0 {
			t.Fatalf("ordinary zone received balance writes: %v", posts)
		}
	})

	t.Run("selected right member", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		app := NewWebApp()
		addStereoBalancePair(app, speaker, "LEFT")
		request := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.20/stereo-pair/balance/0", nil)
		request = withChiParams(request, map[string]string{"id": "192.0.2.20", "level": "0"})
		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, request)
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
		}
	})

	t.Run("right-role master", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		app := NewWebApp()
		master, _ := addStereoBalancePair(app, speaker, "RIGHT")
		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("0"))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
		}
		if got := speaker.postedLevels(); fmt.Sprint(got) != "[0]" {
			t.Fatalf("right-role master balance posts = %v, want [0]", got)
		}
		if got := master.Status().Balance; got == nil || got.TargetBalance != 0 || got.ActualBalance != 0 {
			t.Fatalf("right-role master cached balance = %+v, want confirmed readback 0", got)
		}
	})
}

func TestHandleStereoBalanceReportsWriteAndReadbackOutcomes(t *testing.T) {
	t.Run("transport error with successful authoritative readback", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		speaker.writeStatus = http.StatusInternalServerError
		speaker.applyWriteOnError = true
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")
		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("4"))
		payload, result := decodeStereoBalanceResponse(t, response)
		if response.Code != http.StatusOK || !payload.Success || result.Target == nil || result.Actual == nil ||
			*result.Target != 4 || *result.Actual != 4 || !result.AtTarget {
			t.Fatalf("unexpected verified write error: status=%d success=%v data=%+v", response.Code, payload.Success, result)
		}
		if speaker.getCount() != 1 {
			t.Fatal("write error did not perform mandatory readback")
		}
		if got := left.Status().Balance; got == nil || got.ActualBalance != 4 {
			t.Fatalf("authoritative readback was not cached: %+v", got)
		}
	})

	t.Run("unverified readback", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		speaker.readStatus = http.StatusInternalServerError
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")
		broadcastClient := addBalanceBroadcastClient(t, app)
		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("4"))
		payload, result := decodeStereoBalanceResponse(t, response)
		if response.Code != http.StatusBadGateway || payload.Success || result.Target != nil || result.Actual != nil {
			t.Fatalf("unexpected readback failure: status=%d success=%v data=%+v", response.Code, payload.Success, result)
		}
		if got := left.Status().Balance; got != nil {
			t.Fatalf("unverified write retained cached balance: %+v", got)
		}

		if err := broadcastClient.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatalf("set broadcast deadline: %v", err)
		}
		var frame struct {
			Type string `json:"type"`
			Data map[string]struct {
				Status *webtypes.DeviceStatus `json:"status"`
			} `json:"data"`
		}
		if err := broadcastClient.ReadJSON(&frame); err != nil {
			t.Fatalf("read balance invalidation broadcast: %v", err)
		}
		view := frame.Data["192.0.2.10"]
		if frame.Type != "devices" || view.Status == nil || view.Status.Balance != nil {
			t.Fatalf("broadcast retained unverified balance: type=%q status=%+v", frame.Type, view.Status)
		}
	})

	t.Run("write and readback both fail", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		speaker.writeStatus = http.StatusInternalServerError
		speaker.readStatus = http.StatusInternalServerError
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")
		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("4"))
		payload, result := decodeStereoBalanceResponse(t, response)
		if response.Code != http.StatusBadGateway || payload.Success || result.Target != nil || result.Actual != nil {
			t.Fatalf("unexpected dual failure: status=%d success=%v data=%+v", response.Code, payload.Success, result)
		}
		if speaker.getCount() != 1 {
			t.Fatal("write failure did not perform mandatory readback")
		}
		if got := left.Status().Balance; got != nil {
			t.Fatalf("dual failure retained cached balance: %+v", got)
		}
	})

	t.Run("verified mismatch", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		target, actual := 4, 2
		speaker.target, speaker.actual = &target, &actual
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")
		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("4"))
		payload, result := decodeStereoBalanceResponse(t, response)
		if response.Code != http.StatusOK || !payload.Success || result.AtTarget || result.Actual == nil || *result.Actual != 2 {
			t.Fatalf("unexpected mismatch response: status=%d success=%v data=%+v", response.Code, payload.Success, result)
		}
		if got := left.Status().Balance; got == nil || got.ActualBalance != 2 {
			t.Fatalf("verified mismatch was not cached: %+v", got)
		}
	})

	t.Run("readback becomes unavailable", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 0)
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")
		speaker.mu.Lock()
		speaker.balanceAvailable = false
		speaker.mu.Unlock()

		response := httptest.NewRecorder()
		app.HandleStereoBalance(response, stereoBalanceRequest("4"))
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
		}
		got := left.Status().Balance
		if got == nil || !got.CapabilityKnown || got.BalanceAvailable {
			t.Fatalf("authoritative unavailable readback was not cached: %+v", got)
		}
	})
}

func TestHandleStereoBalanceRejectsPreWriteTopologyInvalidation(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(*webtypes.DeviceConnection)
	}{
		{
			name: "group refresh",
			invalidate: func(conn *webtypes.DeviceConnection) {
				conn.BeginGroupRefresh()
			},
		},
		{
			name: "group event",
			invalidate: func(conn *webtypes.DeviceConnection) {
				conn.ApplyGroupEvent(stereoBalanceGroup("LEFT"), time.Now())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			speaker := newBalanceTestSpeaker(t, 0)
			app := NewWebApp()
			left, _ := addStereoBalancePair(app, speaker, "LEFT")
			test.invalidate(left)

			response := httptest.NewRecorder()
			app.HandleStereoBalance(response, stereoBalanceRequest("4"))

			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
			}
			if posts := speaker.postedLevels(); len(posts) != 0 || speaker.getCount() != 0 {
				t.Fatalf("invalidated topology reached balance endpoint: posts=%v gets=%d", posts, speaker.getCount())
			}
		})
	}
}

func TestHandleStereoBalanceRejectsTopologyChangeDuringReadback(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 0)
	speaker.getStarted = make(chan struct{}, 1)
	speaker.releaseGet = make(chan struct{})
	app := NewWebApp()
	left, _ := addStereoBalancePair(app, speaker, "LEFT")

	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		app.HandleStereoBalance(response, stereoBalanceRequest("4"))
		close(done)
	}()
	<-speaker.getStarted

	left.ApplyGroupEvent(&models.Group{}, time.Now())
	replacementGroup := stereoBalanceGroup("LEFT")
	replacementGroup.ID = "pair-2"
	left.ApplyGroupEvent(replacementGroup, time.Now())
	close(speaker.releaseGet)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("balance handler did not finish")
	}
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if got := left.Status().Balance; got != nil {
		t.Fatalf("stale readback crossed teardown/re-pair: %+v", got)
	}
}

func TestRegisteredBalanceOperationRejectsPreWriteRegistryReplacement(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 0)
	app := NewWebApp()
	original, _ := addStereoBalancePair(app, speaker, "LEFT")
	refresh, ok := original.BeginBalanceRefresh()
	if !ok {
		t.Fatal("failed to snapshot original balance generation")
	}
	if !app.RemoveDevice("192.0.2.10") {
		t.Fatal("failed to remove original registry entry")
	}
	replacement := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{DeviceID: "LEFT", Type: "SoundTouch 10"})
	if !app.AddDevice("192.0.2.10", replacement) {
		t.Fatal("failed to register replacement connection")
	}

	operationStarted := false
	current := app.withCurrentBalanceWrite("192.0.2.10", original, refresh, func() {
		operationStarted = true
		_ = original.Client.SetBalanceForRange(4, -7, 7)
	})
	if current || operationStarted {
		t.Fatal("stale registry connection initiated a balance operation")
	}
	if posts := speaker.postedLevels(); len(posts) != 0 {
		t.Fatalf("stale registry connection wrote balance: %v", posts)
	}
}

func TestHandleStereoBalanceRejectsRegistryReplacementDuringReadback(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 0)
	speaker.getStarted = make(chan struct{}, 1)
	speaker.releaseGet = make(chan struct{})
	app := NewWebApp()
	original, _ := addStereoBalancePair(app, speaker, "LEFT")

	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		app.HandleStereoBalance(response, stereoBalanceRequest("4"))
		close(done)
	}()
	<-speaker.getStarted

	replacement := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID: "LEFT", Name: "Replacement", Type: "SoundTouch 10", IPAddress: "192.0.2.10",
	})
	replacement.SetStatus(&webtypes.DeviceStatus{
		Group: stereoBalanceGroup("LEFT"),
		Balance: &models.Balance{
			BalanceAvailable: true,
			BalanceMin:       -7,
			BalanceMax:       7,
			BalanceDefault:   0,
			TargetBalance:    -2,
			ActualBalance:    -2,
			CapabilityKnown:  true,
		},
		IsConnected:  true,
		Connectivity: webtypes.ConnectivityOnline,
	})
	if !app.RemoveDevice("192.0.2.10") {
		t.Fatal("failed to remove original registry entry")
	}
	if !app.AddDevice("192.0.2.10", replacement) {
		t.Fatal("failed to register replacement connection")
	}
	close(speaker.releaseGet)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("balance handler did not finish")
	}
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if got := original.Status().Balance; got == nil || got.ActualBalance != 0 {
		t.Fatalf("detached connection cached stale readback: %+v", got)
	}
	if got := replacement.Status().Balance; got == nil || got.ActualBalance != -2 {
		t.Fatalf("replacement connection was mutated: %+v", got)
	}
}

func TestHandleStereoBalanceSerializesRequestsPerMaster(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 0)
	speaker.postStarted = make(chan int, 2)
	speaker.releaseFirst = make(chan struct{})
	app := NewWebApp()
	addStereoBalancePair(app, speaker, "LEFT")

	firstDone := make(chan struct{})
	go func() {
		app.HandleStereoBalance(httptest.NewRecorder(), stereoBalanceRequest("3"))
		close(firstDone)
	}()
	if post := <-speaker.postStarted; post != 1 {
		t.Fatalf("first started post = %d", post)
	}

	secondDone := make(chan struct{})
	go func() {
		app.HandleStereoBalance(httptest.NewRecorder(), stereoBalanceRequest("5"))
		close(secondDone)
	}()
	select {
	case post := <-speaker.postStarted:
		t.Fatalf("post %d started while the first operation was in flight", post)
	case <-time.After(50 * time.Millisecond):
	}

	close(speaker.releaseFirst)
	select {
	case post := <-speaker.postStarted:
		if post != 2 {
			t.Fatalf("second started post = %d", post)
		}
	case <-time.After(time.Second):
		t.Fatal("second balance operation did not start after first completed")
	}

	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first balance operation did not finish")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second balance operation did not finish")
	}
	if got := speaker.postedLevels(); fmt.Sprint(got) != "[3 5]" {
		t.Fatalf("serialized posts = %v, want [3 5]", got)
	}
}

func TestStereoBalanceErrorResponseKeepsResultShape(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 0)
	speaker.readStatus = http.StatusInternalServerError
	app := NewWebApp()
	addStereoBalancePair(app, speaker, "LEFT")
	response := httptest.NewRecorder()
	app.HandleStereoBalance(response, stereoBalanceRequest("-4"))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw["data"], &data); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	for _, field := range []string{"requested", "target", "actual", "atTarget"} {
		if _, ok := data[field]; !ok {
			t.Errorf("error result omitted %q: %s", field, response.Body.String())
		}
	}
}

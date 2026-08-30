package soundtouchweb

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

func TestUpdateDeviceStatusDoesNotRefreshNowPlayingRevisionOnFailure(t *testing.T) {
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/volume" {
			_, _ = w.Write([]byte(`<volume><targetvolume>35</targetvolume><actualvolume>35</actualvolume><muteenabled>false</muteenabled></volume>`))
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer speaker.Close()

	conn := webtypes.NewDeviceConnection(client.NewClient(&client.Config{Host: speaker.URL}), nil)
	conn.SetStatus(&webtypes.DeviceStatus{NowPlaying: &models.NowPlaying{Source: "SPOTIFY"}})
	baseline := conn.Status()

	NewWebApp().UpdateDeviceStatus("speaker", conn)
	updated := conn.Status()

	if updated.Revision <= baseline.Revision || updated.Volume == nil || updated.Volume.ActualVolume != 35 {
		t.Fatalf("unrelated successful field was not merged: %+v", updated)
	}
	if updated.NowPlaying.Source != "SPOTIFY" || updated.NowPlayingRevision != baseline.NowPlayingRevision {
		t.Fatalf("failed now-playing read advanced source authority: %+v", updated)
	}
}

func TestHandleAPIDevicePublishesCanonicalReadback(t *testing.T) {
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responses := map[string]string{
			"/now_playing": `<nowPlaying source="AUX" sourceAccount="AUX1"><track>Confirmed track</track><playStatus>PLAY_STATE</playStatus></nowPlaying>`,
			"/volume":      `<volume><targetvolume>35</targetvolume><actualvolume>35</actualvolume><muteenabled>false</muteenabled></volume>`,
			"/presets":     `<presets></presets>`,
			"/sources":     `<sources><sourceItem source="AUX" sourceAccount="AUX1" status="READY" isLocal="true">Aux 1</sourceItem></sources>`,
			"/bass":        `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	defer speaker.Close()

	app := NewWebApp()
	conn := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.URL}),
		&models.DeviceInfo{Name: "Speaker"},
	)
	conn.SetStatus(&webtypes.DeviceStatus{NowPlaying: &models.NowPlaying{Source: "STANDBY"}})
	baselineRevision := conn.Status().NowPlayingRevision
	conn.WebSocket = &client.WebSocketClient{}
	app.AddDevice("speaker", conn)

	req := httptest.NewRequest(http.MethodGet, "/api/control/devices/speaker", nil)
	req = withChiParams(req, map[string]string{"id": "speaker"})
	w := httptest.NewRecorder()
	app.HandleAPIDevice(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET device status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Status webtypes.DeviceStatus `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode device readback: %v", err)
	}
	canonical := conn.Status()
	if !response.Success || canonical.NowPlaying.Source != "AUX" || canonical.NowPlaying.Track != "Confirmed track" {
		t.Fatalf("canonical readback not merged: response=%+v status=%+v", response, canonical)
	}
	if response.Data.Status.NowPlaying.Source != canonical.NowPlaying.Source ||
		response.Data.Status.Revision != canonical.Revision ||
		response.Data.Status.NowPlayingRevision != canonical.NowPlayingRevision ||
		canonical.NowPlayingRevision <= baselineRevision {
		t.Fatalf("response did not publish canonical status: response=%+v status=%+v", response.Data.Status, canonical)
	}
}

func TestUpdateSourcesCacheRetainsFailureAndRefreshesSuccess(t *testing.T) {
	firstRead := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	oldSources := &models.Sources{SourceItem: []models.SourceItem{{Source: "AUX"}}}
	status := &webtypes.DeviceStatus{
		Sources:       oldSources,
		SourcesReadAt: firstRead,
	}

	if !updateSourcesCache(status, nil, errors.New("temporary read failure"), firstRead.Add(time.Second), 2) {
		t.Fatal("failed source readback was not recorded")
	}
	if status.Sources != oldSources || !status.SourcesReadAt.Equal(firstRead) || !status.SourcesStale {
		t.Fatalf("failed source readback changed the cache: %+v", status)
	}

	secondRead := firstRead.Add(31 * time.Second)
	newSources := &models.Sources{SourceItem: []models.SourceItem{{Source: "PRODUCT"}}}
	if !updateSourcesCache(status, newSources, nil, secondRead, 3) {
		t.Fatal("successful source readback was not reported as an update")
	}
	if status.Sources != newSources || !status.SourcesReadAt.Equal(secondRead) || status.SourcesStale {
		t.Fatalf("successful source readback did not refresh the cache: %+v", status)
	}
}

func TestUpdateSourcesCacheInitialFailureIsExplicit(t *testing.T) {
	status := &webtypes.DeviceStatus{}
	if !updateSourcesCache(status, nil, errors.New("initial read failure"), time.Now(), 1) {
		t.Fatal("initial failed source poll was not accepted")
	}
	if status.Sources != nil || !status.SourcesStale {
		t.Fatalf("initial source failure was not represented without inventory: %+v", status)
	}
}

func TestUpdateSourcesCacheFailureFencesOlderOverlappingSuccess(t *testing.T) {
	readAt := time.Date(2026, time.August, 30, 12, 0, 2, 0, time.UTC)
	retainedSources := &models.Sources{SourceItem: []models.SourceItem{{Source: "PRODUCT"}}}
	status := &webtypes.DeviceStatus{
		Sources:       retainedSources,
		SourcesReadAt: readAt,
	}

	if !updateSourcesCache(status, nil, errors.New("newer read failed"), readAt.Add(time.Second), 2) {
		t.Fatal("newer failed source poll was not accepted")
	}
	olderSources := &models.Sources{SourceItem: []models.SourceItem{{Source: "AUX"}}}
	if updateSourcesCache(status, olderSources, nil, readAt.Add(2*time.Second), 1) {
		t.Fatal("older successful source poll was accepted after the newer failure")
	}
	if status.Sources != retainedSources || !status.SourcesReadAt.Equal(readAt) || !status.SourcesStale {
		t.Fatalf("older success cleared the newer source failure: %+v", status)
	}
}

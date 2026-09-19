package soundtouchweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestHandleAPIDeviceReportsZoneRoles(t *testing.T) {
	app := NewWebApp()
	zone := &models.ZoneInfo{
		Master: "master-id",
		Members: []models.Member{
			{DeviceID: "master-id", IP: "192.0.2.10"},
			{DeviceID: "member-id", IP: "192.0.2.11"},
		},
	}
	for _, entry := range []DeviceEntry{
		projectionDeviceWithZone("192.0.2.10", "master-id", "Atrium", true, nil, zone),
		projectionDeviceWithZone("192.0.2.11", "member-id", "Breakfast Room", true, nil, nil),
	} {
		app.AddDevice(entry.ID, entry.Device)
	}

	member := apiDeviceDetail(t, app, "192.0.2.11")
	if member.Info == nil || member.Info.Name != "Breakfast Room" {
		t.Fatalf("member detail = %+v, want the logical zone member", member)
	}
	if member.ZoneMembership == nil || member.ZoneMembership.MasterControlID != "192.0.2.10" {
		t.Fatalf("member detail lost its zone membership: %+v", member.ZoneMembership)
	}

	master := apiDeviceDetail(t, app, "192.0.2.10")
	if master.Zone == nil || master.Zone.MemberCount != 2 {
		t.Fatalf("master detail lost its zone: %+v", master.Zone)
	}
}

func apiDeviceDetail(t *testing.T, app *WebApp, id string) deviceView {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/control/devices/"+id, nil)
	req = withChiParams(req, map[string]string{"id": id})
	response := httptest.NewRecorder()
	app.HandleAPIDevice(response, req)

	var payload struct {
		Success bool       `json:"success"`
		Data    deviceView `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode detail for %s: %v", id, err)
	}
	if response.Code != http.StatusOK || !payload.Success {
		t.Fatalf("detail for %s: status=%d payload=%+v", id, response.Code, payload)
	}

	return payload.Data
}

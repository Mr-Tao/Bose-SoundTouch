package client

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestClient_GetGroup_Configured(t *testing.T) {
	responseXML := `<?xml version="1.0" encoding="UTF-8" ?>
<group id="1234567">
  <name>Living Room Pair</name>
  <masterDeviceId>9070658C9D4A</masterDeviceId>
  <roles>
    <groupRole>
      <deviceId>9070658C9D4A</deviceId>
      <role>LEFT</role>
      <ipAddress>192.0.2.131</ipAddress>
    </groupRole>
    <groupRole>
      <deviceId>F45EAB3115DA</deviceId>
      <role>RIGHT</role>
      <ipAddress>192.0.2.134</ipAddress>
    </groupRole>
  </roles>
  <senderIPAddress>192.0.2.131</senderIPAddress>
  <status>GROUP_OK</status>
</group>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getGroup" {
			t.Errorf("path = %q, want /getGroup", r.URL.Path)
		}

		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}

		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(responseXML))
	}))
	defer server.Close()

	g, err := createTestClient(server.URL).GetGroup()
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}

	if g.ID != "1234567" {
		t.Errorf("ID = %q, want 1234567", g.ID)
	}

	if g.Name != "Living Room Pair" {
		t.Errorf("Name = %q, want Living Room Pair", g.Name)
	}

	if g.MasterDeviceID != "9070658C9D4A" {
		t.Errorf("MasterDeviceID = %q", g.MasterDeviceID)
	}

	if g.Status != "GROUP_OK" {
		t.Errorf("Status = %q, want GROUP_OK", g.Status)
	}

	if len(g.Roles.Roles) != 2 {
		t.Fatalf("roles = %d, want 2", len(g.Roles.Roles))
	}

	if g.Roles.Roles[0].Role != "LEFT" || g.Roles.Roles[1].Role != "RIGHT" {
		t.Errorf("role order LEFT/RIGHT not preserved: %+v", g.Roles.Roles)
	}

	if g.IsEmpty() {
		t.Errorf("IsEmpty = true for populated group")
	}
}

func TestClient_GetGroup_Empty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<group />`))
	}))
	defer server.Close()

	g, err := createTestClient(server.URL).GetGroup()
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}

	if !g.IsEmpty() {
		t.Errorf("IsEmpty = false for <group/>, got %+v", g)
	}
}

func TestClient_AddGroup(t *testing.T) {
	var capturedBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/addGroup" {
			t.Errorf("path = %q, want /addGroup", r.URL.Path)
		}

		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}

		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)

		// Echo the request back with an assigned ID and GROUP_OK status —
		// matches real device behaviour.
		var got models.Group
		if err := xml.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		got.ID = "9999999"
		got.Status = "GROUP_OK"
		got.SenderIPAddress = "192.0.2.131"

		w.Header().Set("Content-Type", "application/xml")

		enc, _ := xml.Marshal(&got)
		_, _ = w.Write(enc)
	}))
	defer server.Close()

	req := &models.Group{
		Name:           "Living Room",
		MasterDeviceID: "9070658C9D4A",
		Roles: models.GroupRoles{
			Roles: []models.GroupRole{
				{DeviceID: "9070658C9D4A", Role: "LEFT", IPAddress: "192.0.2.131"},
				{DeviceID: "F45EAB3115DA", Role: "RIGHT", IPAddress: "192.0.2.134"},
			},
		},
	}

	resp, err := createTestClient(server.URL).AddGroup(req)
	if err != nil {
		t.Fatalf("AddGroup: %v", err)
	}

	if resp.ID != "9999999" {
		t.Errorf("response ID = %q, want 9999999", resp.ID)
	}

	if resp.Status != "GROUP_OK" {
		t.Errorf("response Status = %q, want GROUP_OK", resp.Status)
	}

	// Wire-shape sanity: the request body must carry both roles and the
	// master ID (the device validates these on the wire).
	for _, want := range []string{"<role>LEFT</role>", "<role>RIGHT</role>", "9070658C9D4A"} {
		if !strings.Contains(capturedBody, want) {
			t.Errorf("request body missing %q\nbody:\n%s", want, capturedBody)
		}
	}
}

func TestClient_UpdateGroup_RenameRoundtrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/updateGroup" {
			t.Errorf("path = %q, want /updateGroup", r.URL.Path)
		}

		body, _ := io.ReadAll(r.Body)

		var got models.Group
		if err := xml.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}

		got.Status = "GROUP_OK"

		w.Header().Set("Content-Type", "application/xml")

		enc, _ := xml.Marshal(&got)
		_, _ = w.Write(enc)
	}))
	defer server.Close()

	req := &models.Group{
		ID:             "1234567",
		Name:           "Kitchen Pair",
		MasterDeviceID: "AAAA",
		Roles: models.GroupRoles{
			Roles: []models.GroupRole{
				{DeviceID: "AAAA", Role: "LEFT"},
				{DeviceID: "BBBB", Role: "RIGHT"},
			},
		},
	}

	resp, err := createTestClient(server.URL).UpdateGroup(req)
	if err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}

	if resp.Name != "Kitchen Pair" {
		t.Errorf("Name = %q, want Kitchen Pair", resp.Name)
	}

	if resp.ID != "1234567" {
		t.Errorf("ID = %q, want 1234567", resp.ID)
	}
}

func TestClient_RemoveGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/removeGroup" {
			t.Errorf("path = %q, want /removeGroup", r.URL.Path)
		}

		// The wiki specifies GET (not DELETE) for /removeGroup. We honour
		// that, surprising as it is for a state-mutating endpoint.
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}

		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<group />`))
	}))
	defer server.Close()

	if err := createTestClient(server.URL).RemoveGroup(); err != nil {
		t.Fatalf("RemoveGroup: %v", err)
	}
}

func TestClientRemoveGroupDoesNotReplayDroppedResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getGroup" {
			_, _ = w.Write([]byte(`<group />`))
			return
		}

		if r.URL.Path != "/removeGroup" {
			http.NotFound(w, r)
			return
		}

		calls.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack response: %v", err)
			return
		}

		_ = connection.Close()
	}))
	defer server.Close()

	client := createTestClient(server.URL)
	if _, err := client.GetGroup(); err != nil {
		t.Fatalf("prime ordinary client connection: %v", err)
	}

	err := client.RemoveGroup()
	if err == nil {
		t.Fatal("RemoveGroup succeeded after the response was dropped")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("/removeGroup requests = %d, want exactly 1", got)
	}
}

func TestClientRemoveGroupDoesNotFollowRedirect(t *testing.T) {
	var calls atomic.Int32
	var redirectedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		switch r.URL.Path {
		case "/removeGroup":
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
		case "/redirected":
			redirectedCalls.Add(1)
			_, _ = w.Write([]byte(`<group />`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := createTestClient(server.URL).RemoveGroup()
	if err == nil {
		t.Fatal("RemoveGroup followed a redirect")
	}
	if !strings.Contains(err.Error(), "status 307") {
		t.Fatalf("RemoveGroup error = %q, want redirect status", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want exactly 1", got)
	}
	if got := redirectedCalls.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
}

package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestClientSystemSettingsGETs(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		response string
		call     func(*Client) error
	}{
		{
			name:     "system timeout",
			path:     "/systemtimeout",
			response: `<systemtimeout><powersaving_enabled>true</powersaving_enabled></systemtimeout>`,
			call: func(client *Client) error {
				setting, err := client.GetSystemTimeout()
				if err == nil && !setting.PowerSavingEnabled {
					t.Error("PowerSavingEnabled = false, want true")
				}
				return err
			},
		},
		{
			name:     "rebroadcast latency",
			path:     "/rebroadcastlatencymode",
			response: `<rebroadcastlatencymode mode="SYNC_TO_ZONE" controllable="true"/>`,
			call: func(client *Client) error {
				setting, err := client.GetRebroadcastLatencyMode()
				if err == nil && (setting.Mode != models.RebroadcastLatencySyncToZone || !setting.Controllable) {
					t.Errorf("setting = %#v", setting)
				}
				return err
			},
		},
		{
			name:     "known language",
			path:     "/language",
			response: `<sysLanguage>15</sysLanguage>`,
			call: func(client *Client) error {
				language, err := client.GetLanguage()
				if err == nil && language.Code != models.LanguageCzech {
					t.Errorf("Code = %d, want %d", language.Code, models.LanguageCzech)
				}
				return err
			},
		},
		{
			name:     "unknown language remains readable",
			path:     "/language",
			response: `<sysLanguage>99</sysLanguage>`,
			call: func(client *Client) error {
				language, err := client.GetLanguage()
				if err == nil && language.Code != 99 {
					t.Errorf("Code = %d, want 99", language.Code)
				}
				return err
			},
		},
		{
			name:     "Bluetooth info",
			path:     "/bluetoothInfo",
			response: `<BluetoothInfo BluetoothMACAddress="0CB2B709F837"/>`,
			call: func(client *Client) error {
				info, err := client.GetBluetoothInfo()
				if err == nil && info.BluetoothMACAddress != "0CB2B709F837" {
					t.Errorf("BluetoothMACAddress = %q", info.BluetoothMACAddress)
				}
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if r.URL.Path != test.path {
					t.Errorf("path = %s, want %s", r.URL.Path, test.path)
				}
				if got := r.Header.Get("Accept"); got != "application/xml" {
					t.Errorf("Accept = %q", got)
				}
				if got := r.Header.Get("User-Agent"); got != "Bose-SoundTouch-Go-Client/1.0" {
					t.Errorf("User-Agent = %q", got)
				}
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()

			if err := test.call(createTestClient(server.URL)); err != nil {
				t.Fatalf("call(): %v", err)
			}
		})
	}
}

func TestClientSystemSettingsGETErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		call   func(*Client) error
	}{
		{
			name: "malformed system timeout XML",
			body: `<systemtimeout><powersaving_enabled>`,
			call: func(client *Client) error { _, err := client.GetSystemTimeout(); return err },
		},
		{
			name: "incomplete Bluetooth XML",
			body: `<BluetoothInfo/>`,
			call: func(client *Client) error { _, err := client.GetBluetoothInfo(); return err },
		},
		{
			name:   "language non-200",
			status: http.StatusNotFound,
			body:   `unsupported`,
			call:   func(client *Client) error { _, err := client.GetLanguage(); return err },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				status := test.status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()

			if err := test.call(createTestClient(server.URL)); err == nil {
				t.Fatal("call() unexpectedly succeeded")
			}
		})
	}
}

func TestClientSystemSettingsPOSTs(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		call func(*Client) error
	}{
		{
			name: "system timeout",
			path: "/systemtimeout",
			body: `<systemtimeout><powersaving_enabled>false</powersaving_enabled></systemtimeout>`,
			call: func(client *Client) error {
				return client.SetSystemTimeout(&models.SystemTimeout{PowerSavingEnabled: false})
			},
		},
		{
			name: "rebroadcast latency",
			path: "/rebroadcastlatencymode",
			body: `<rebroadcastlatencymode mode="SYNC_TO_ROOM"></rebroadcastlatencymode>`,
			call: func(client *Client) error {
				return client.SetRebroadcastLatencyMode(models.RebroadcastLatencySyncToRoom)
			},
		},
		{
			name: "language",
			path: "/language",
			body: `<sysLanguage>3</sysLanguage>`,
			call: func(client *Client) error { return client.SetLanguage(models.LanguageEnglish) },
		},
		{
			name: "source rename with account",
			path: "/nameSource",
			body: `<ContentItem source="AUX" sourceAccount="AUX1"><itemName>Turntable</itemName></ContentItem>`,
			call: func(client *Client) error { return client.RenameSource("AUX", "AUX1", "Turntable") },
		},
		{
			name: "source rename without account",
			path: "/nameSource",
			body: `<ContentItem source="BLUETOOTH"><itemName>Phone</itemName></ContentItem>`,
			call: func(client *Client) error { return client.RenameSource("BLUETOOTH", "", "Phone") },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if r.URL.Path != test.path {
					t.Errorf("path = %s, want %s", r.URL.Path, test.path)
				}
				if got := r.Header.Get("Content-Type"); got != "application/xml" {
					t.Errorf("Content-Type = %q", got)
				}
				if got := r.Header.Get("Accept"); got != "application/xml" {
					t.Errorf("Accept = %q", got)
				}
				if got := r.Header.Get("User-Agent"); got != "Bose-SoundTouch-Go-Client/1.0" {
					t.Errorf("User-Agent = %q", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("ReadAll(): %v", err)
				}
				if string(body) != test.body {
					t.Errorf("body = %q, want %q", body, test.body)
				}
			}))
			defer server.Close()

			if err := test.call(createTestClient(server.URL)); err != nil {
				t.Fatalf("call(): %v", err)
			}
		})
	}
}

func TestClientSystemSettingsPOSTValidation(t *testing.T) {
	client := createTestClient("http://127.0.0.1:1")
	for _, test := range []struct {
		name string
		call func() error
	}{
		{"nil timeout", func() error { return client.SetSystemTimeout(nil) }},
		{"unknown latency", func() error { return client.SetRebroadcastLatencyMode("OTHER") }},
		{"unknown language", func() error { return client.SetLanguage(99) }},
		{"missing source", func() error { return client.RenameSource("", "", "Name") }},
		{"missing item name", func() error { return client.RenameSource("AUX", "AUX1", "") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("call() unexpectedly succeeded")
			}
		})
	}
}

func TestClientSystemSettingsPOSTNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	defer server.Close()

	err := createTestClient(server.URL).SetLanguage(models.LanguageEnglish)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("SetLanguage() error = %v, want status 400", err)
	}
}

func TestClientBluetoothMutatingGETs(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		call func(*Client) error
	}{
		{"enter pairing mode", "/enterPairingMode", func(client *Client) error { return client.EnterPairingMode() }},
		{"clear paired list", "/clearPairedList", func(client *Client) error { return client.ClearPairedList() }},
		{"enter Bluetooth pairing", "/enterBluetoothPairing", func(client *Client) error { return client.EnterBluetoothPairing() }},
		{"clear Bluetooth paired", "/clearBluetoothPaired", func(client *Client) error { return client.ClearBluetoothPaired() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if r.URL.Path != test.path {
					t.Errorf("path = %s, want %s", r.URL.Path, test.path)
				}
				if got := r.Header.Get("Accept"); got != "application/xml" {
					t.Errorf("Accept = %q", got)
				}
				if got := r.Header.Get("User-Agent"); got != "Bose-SoundTouch-Go-Client/1.0" {
					t.Errorf("User-Agent = %q", got)
				}
				_, _ = io.WriteString(w, `<status>accepted</status>`)
			}))
			defer server.Close()

			if err := test.call(createTestClient(server.URL)); err != nil {
				t.Fatalf("call(): %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestClientMutatingGETDoesNotFollowRedirect(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/enterBluetoothPairing" {
			http.Redirect(w, r, "/replayed", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `<status>replayed</status>`)
	}))
	defer server.Close()

	err := createTestClient(server.URL).EnterBluetoothPairing()
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("EnterBluetoothPairing() error = %v, want status 307", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

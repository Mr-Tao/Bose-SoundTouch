package models

import (
	"encoding/xml"
	"reflect"
	"testing"
)

func TestSystemTimeoutXML(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		input := `<systemtimeout><powersaving_enabled>` + map[bool]string{true: "true", false: "false"}[enabled] + `</powersaving_enabled></systemtimeout>`
		var setting SystemTimeout
		if err := xml.Unmarshal([]byte(input), &setting); err != nil {
			t.Fatalf("xml.Unmarshal(%q): %v", input, err)
		}
		if setting.PowerSavingEnabled != enabled {
			t.Fatalf("PowerSavingEnabled = %t, want %t", setting.PowerSavingEnabled, enabled)
		}

		got, err := xml.Marshal(setting)
		if err != nil {
			t.Fatalf("xml.Marshal(): %v", err)
		}
		if string(got) != input {
			t.Fatalf("xml.Marshal() = %q, want %q", got, input)
		}
	}
}

func TestSystemTimeoutRejectsInvalidXML(t *testing.T) {
	for _, input := range []string{
		`<systemtimeout/>`,
		`<systemtimeout><powersaving_enabled>maybe</powersaving_enabled></systemtimeout>`,
		`<wrong><powersaving_enabled>true</powersaving_enabled></wrong>`,
	} {
		var setting SystemTimeout
		if err := xml.Unmarshal([]byte(input), &setting); err == nil {
			t.Errorf("xml.Unmarshal(%q) unexpectedly succeeded", input)
		}
	}
}

func TestRebroadcastLatencyModeXML(t *testing.T) {
	input := `<rebroadcastlatencymode mode="SYNC_TO_ZONE" controllable="true"></rebroadcastlatencymode>`
	var setting RebroadcastLatencyMode
	if err := xml.Unmarshal([]byte(input), &setting); err != nil {
		t.Fatalf("xml.Unmarshal(): %v", err)
	}
	if setting.Mode != RebroadcastLatencySyncToZone || !setting.Controllable {
		t.Fatalf("setting = %#v", setting)
	}
	if err := setting.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}

	request := RebroadcastLatencyModeRequest{Mode: RebroadcastLatencySyncToRoom}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	got, err := xml.Marshal(request)
	if err != nil {
		t.Fatalf("xml.Marshal(): %v", err)
	}
	if want := `<rebroadcastlatencymode mode="SYNC_TO_ROOM"></rebroadcastlatencymode>`; string(got) != want {
		t.Fatalf("xml.Marshal() = %q, want %q", got, want)
	}
}

func TestRebroadcastLatencyModeValidation(t *testing.T) {
	for _, input := range []string{
		`<rebroadcastlatencymode controllable="true"/>`,
		`<rebroadcastlatencymode mode="SYNC_TO_ROOM"/>`,
		`<rebroadcastlatencymode mode="OTHER" controllable="true"/>`,
		`<rebroadcastlatencymode mode="SYNC_TO_ROOM" controllable="maybe"/>`,
	} {
		var setting RebroadcastLatencyMode
		if err := xml.Unmarshal([]byte(input), &setting); err == nil {
			t.Errorf("xml.Unmarshal(%q) unexpectedly succeeded", input)
		}
	}

	request := RebroadcastLatencyModeRequest{Mode: "OTHER"}
	if err := request.Validate(); err == nil {
		t.Error("Validate() unexpectedly accepted an unknown mode")
	}
}

func TestSystemLanguageCodesMatchStockholm(t *testing.T) {
	want := map[LanguageCode]string{
		1: "Dansk", 2: "Deutsch", 3: "English", 4: "Español", 5: "Français",
		6: "Italiano", 7: "Nederlands", 8: "Svenska", 9: "日本語", 10: "简体中文",
		11: "繁體中文", 12: "한국어", 13: "ไทย", 15: "Čeština", 16: "Suomi",
		17: "Ελληνικά", 18: "Norsk", 19: "Polski", 20: "Português", 21: "Română",
		22: "Русский", 23: "Slovenščina", 24: "Türkçe", 25: "Magyar",
	}
	if !reflect.DeepEqual(SystemLanguageNames, want) {
		t.Fatalf("SystemLanguageNames = %#v, want %#v", SystemLanguageNames, want)
	}
	if LanguageEnglish != 3 || LanguageCzech != 15 {
		t.Fatalf("English/Czech codes = %d/%d, want 3/15", LanguageEnglish, LanguageCzech)
	}
}

func TestSystemLanguageReadAndWriteValidation(t *testing.T) {
	for _, test := range []struct {
		input string
		code  LanguageCode
	}{
		{`<sysLanguage>15</sysLanguage>`, LanguageCzech},
		{`<sysLanguage>99</sysLanguage>`, 99},
	} {
		var language SystemLanguage
		if err := xml.Unmarshal([]byte(test.input), &language); err != nil {
			t.Fatalf("xml.Unmarshal(%q): %v", test.input, err)
		}
		if language.Code != test.code {
			t.Fatalf("Code = %d, want %d", language.Code, test.code)
		}
	}

	known := SystemLanguage{Code: LanguageEnglish}
	if err := known.Validate(); err != nil {
		t.Fatalf("Validate(English): %v", err)
	}
	got, err := xml.Marshal(known)
	if err != nil {
		t.Fatalf("xml.Marshal(): %v", err)
	}
	if want := `<sysLanguage>3</sysLanguage>`; string(got) != want {
		t.Fatalf("xml.Marshal() = %q, want %q", got, want)
	}

	unknown := SystemLanguage{Code: 99}
	if err := unknown.Validate(); err == nil {
		t.Error("Validate() unexpectedly accepted unknown code 99")
	}
	SystemLanguageNames[99] = "Future language"
	defer delete(SystemLanguageNames, 99)
	if err := unknown.Validate(); err == nil {
		t.Error("Validate() was widened by a caller-modified display map")
	}
	var malformed SystemLanguage
	if err := xml.Unmarshal([]byte(`<sysLanguage>English</sysLanguage>`), &malformed); err == nil {
		t.Error("xml.Unmarshal() unexpectedly accepted a non-integer language")
	}
}

func TestBluetoothInfoXML(t *testing.T) {
	input := `<BluetoothInfo BluetoothMACAddress="0CB2B709F837"></BluetoothInfo>`
	var info BluetoothInfo
	if err := xml.Unmarshal([]byte(input), &info); err != nil {
		t.Fatalf("xml.Unmarshal(): %v", err)
	}
	if info.BluetoothMACAddress != "0CB2B709F837" {
		t.Fatalf("BluetoothMACAddress = %q", info.BluetoothMACAddress)
	}
	if err := info.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if err := (*BluetoothInfo)(nil).Validate(); err == nil {
		t.Fatal("nil Validate() unexpectedly succeeded")
	}
}

func TestSourceRenameRequestXMLAndValidation(t *testing.T) {
	for _, test := range []struct {
		request SourceRenameRequest
		want    string
	}{
		{
			request: SourceRenameRequest{Source: "AUX", SourceAccount: "AUX1", ItemName: "Turntable"},
			want:    `<ContentItem source="AUX" sourceAccount="AUX1"><itemName>Turntable</itemName></ContentItem>`,
		},
		{
			request: SourceRenameRequest{Source: "BLUETOOTH", ItemName: "Phone"},
			want:    `<ContentItem source="BLUETOOTH"><itemName>Phone</itemName></ContentItem>`,
		},
	} {
		if err := test.request.Validate(); err != nil {
			t.Fatalf("Validate(): %v", err)
		}
		got, err := xml.Marshal(test.request)
		if err != nil {
			t.Fatalf("xml.Marshal(): %v", err)
		}
		if string(got) != test.want {
			t.Fatalf("xml.Marshal() = %q, want %q", got, test.want)
		}
	}

	for _, request := range []*SourceRenameRequest{
		nil,
		{ItemName: "Name"},
		{Source: "AUX"},
	} {
		if err := request.Validate(); err == nil {
			t.Errorf("Validate(%#v) unexpectedly succeeded", request)
		}
	}
}

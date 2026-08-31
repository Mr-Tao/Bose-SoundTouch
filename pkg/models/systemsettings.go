package models

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// SystemTimeout is the power-saving setting returned by /systemtimeout.
type SystemTimeout struct {
	XMLName            xml.Name `xml:"systemtimeout"`
	PowerSavingEnabled bool     `xml:"powersaving_enabled"`
}

// Validate checks whether the update model is present.
func (s *SystemTimeout) Validate() error {
	if s == nil {
		return fmt.Errorf("system timeout is nil")
	}

	return nil
}

// UnmarshalXML rejects responses that omit the required power-saving value.
func (s *SystemTimeout) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "systemtimeout" {
		return fmt.Errorf("expected systemtimeout element, got %s", start.Name.Local)
	}

	var wire struct {
		PowerSavingEnabled *bool `xml:"powersaving_enabled"`
	}
	if err := d.DecodeElement(&wire, &start); err != nil {
		return err
	}

	if wire.PowerSavingEnabled == nil {
		return fmt.Errorf("systemtimeout is missing powersaving_enabled")
	}

	s.XMLName = start.Name
	s.PowerSavingEnabled = *wire.PowerSavingEnabled

	return nil
}

// RebroadcastLatencyModeValue is a firmware-supported rebroadcast timing mode.
type RebroadcastLatencyModeValue string

const (
	// RebroadcastLatencySyncToRoom prioritizes the selected room for video sync.
	RebroadcastLatencySyncToRoom RebroadcastLatencyModeValue = "SYNC_TO_ROOM"
	// RebroadcastLatencySyncToZone prioritizes synchronization across the zone.
	RebroadcastLatencySyncToZone RebroadcastLatencyModeValue = "SYNC_TO_ZONE"
)

// Validate rejects values that the SoundTouch firmware does not understand.
func (m RebroadcastLatencyModeValue) Validate() error {
	switch m {
	case RebroadcastLatencySyncToRoom, RebroadcastLatencySyncToZone:
		return nil
	default:
		return fmt.Errorf("unknown rebroadcast latency mode %q", m)
	}
}

// RebroadcastLatencyMode is the setting returned by /rebroadcastlatencymode.
// Controllable is response metadata and is not included in update requests.
type RebroadcastLatencyMode struct {
	XMLName      xml.Name                    `xml:"rebroadcastlatencymode"`
	Mode         RebroadcastLatencyModeValue `xml:"mode,attr"`
	Controllable bool                        `xml:"controllable,attr"`
}

// Validate checks whether the reported mode is supported.
func (r *RebroadcastLatencyMode) Validate() error {
	if r == nil {
		return fmt.Errorf("rebroadcast latency mode is nil")
	}

	return r.Mode.Validate()
}

// UnmarshalXML rejects responses that omit either required attribute.
func (r *RebroadcastLatencyMode) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "rebroadcastlatencymode" {
		return fmt.Errorf("expected rebroadcastlatencymode element, got %s", start.Name.Local)
	}

	var wire struct {
		Mode         *RebroadcastLatencyModeValue `xml:"mode,attr"`
		Controllable *bool                        `xml:"controllable,attr"`
	}
	if err := d.DecodeElement(&wire, &start); err != nil {
		return err
	}

	if wire.Mode == nil {
		return fmt.Errorf("rebroadcastlatencymode is missing mode")
	}

	if wire.Controllable == nil {
		return fmt.Errorf("rebroadcastlatencymode is missing controllable")
	}

	if err := wire.Mode.Validate(); err != nil {
		return err
	}

	r.XMLName = start.Name
	r.Mode = *wire.Mode
	r.Controllable = *wire.Controllable

	return nil
}

// RebroadcastLatencyModeRequest is the update body accepted by the firmware.
type RebroadcastLatencyModeRequest struct {
	XMLName xml.Name                    `xml:"rebroadcastlatencymode"`
	Mode    RebroadcastLatencyModeValue `xml:"mode,attr"`
}

// Validate checks whether the requested latency mode is known.
func (r *RebroadcastLatencyModeRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("rebroadcast latency mode request is nil")
	}

	return r.Mode.Validate()
}

// LanguageCode is a SoundTouch system-language identifier.
type LanguageCode int

// Supported system language codes match the set exposed by Stockholm.
const (
	LanguageDanish             LanguageCode = 1
	LanguageGerman             LanguageCode = 2
	LanguageEnglish            LanguageCode = 3
	LanguageSpanish            LanguageCode = 4
	LanguageFrench             LanguageCode = 5
	LanguageItalian            LanguageCode = 6
	LanguageDutch              LanguageCode = 7
	LanguageSwedish            LanguageCode = 8
	LanguageJapanese           LanguageCode = 9
	LanguageSimplifiedChinese  LanguageCode = 10
	LanguageTraditionalChinese LanguageCode = 11
	LanguageKorean             LanguageCode = 12
	LanguageThai               LanguageCode = 13
	LanguageCzech              LanguageCode = 15
	LanguageFinnish            LanguageCode = 16
	LanguageGreek              LanguageCode = 17
	LanguageNorwegian          LanguageCode = 18
	LanguagePolish             LanguageCode = 19
	LanguagePortuguese         LanguageCode = 20
	LanguageRomanian           LanguageCode = 21
	LanguageRussian            LanguageCode = 22
	LanguageSlovenian          LanguageCode = 23
	LanguageTurkish            LanguageCode = 24
	LanguageHungarian          LanguageCode = 25
)

var knownSystemLanguageNames = map[LanguageCode]string{
	LanguageDanish:             "Dansk",
	LanguageGerman:             "Deutsch",
	LanguageEnglish:            "English",
	LanguageSpanish:            "Español",
	LanguageFrench:             "Français",
	LanguageItalian:            "Italiano",
	LanguageDutch:              "Nederlands",
	LanguageSwedish:            "Svenska",
	LanguageJapanese:           "日本語",
	LanguageSimplifiedChinese:  "简体中文",
	LanguageTraditionalChinese: "繁體中文",
	LanguageKorean:             "한국어",
	LanguageThai:               "ไทย",
	LanguageCzech:              "Čeština",
	LanguageFinnish:            "Suomi",
	LanguageGreek:              "Ελληνικά",
	LanguageNorwegian:          "Norsk",
	LanguagePolish:             "Polski",
	LanguagePortuguese:         "Português",
	LanguageRomanian:           "Română",
	LanguageRussian:            "Русский",
	LanguageSlovenian:          "Slovenščina",
	LanguageTurkish:            "Türkçe",
	LanguageHungarian:          "Magyar",
}

// SystemLanguageNames returns the language labels and codes used by Stockholm.
// Each call returns a copy so callers cannot mutate shared validation state.
func SystemLanguageNames() map[LanguageCode]string {
	names := make(map[LanguageCode]string, len(knownSystemLanguageNames))
	for code, name := range knownSystemLanguageNames {
		names[code] = name
	}

	return names
}

// Validate rejects language codes that Stockholm does not offer for writes.
func (l LanguageCode) Validate() error {
	if _, ok := knownSystemLanguageNames[l]; !ok {
		return fmt.Errorf("unknown system language code %d", l)
	}

	return nil
}

// SystemLanguage is the integer value read from or written to /language.
// Unknown values are retained when reading so newer firmware remains usable.
type SystemLanguage struct {
	XMLName xml.Name     `xml:"sysLanguage"`
	Code    LanguageCode `xml:",chardata"`
}

// UnmarshalXML retains unknown integer codes for forward compatibility while
// rejecting a response that omits the language value entirely.
func (l *SystemLanguage) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "sysLanguage" {
		return fmt.Errorf("expected sysLanguage element, got %s", start.Name.Local)
	}

	var raw string
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("sysLanguage is missing its language code")
	}

	code, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("invalid sysLanguage code %q: %w", raw, err)
	}

	l.XMLName = start.Name
	l.Code = LanguageCode(code)

	return nil
}

// Validate checks whether this language can be sent to the speaker.
func (l *SystemLanguage) Validate() error {
	if l == nil {
		return fmt.Errorf("system language is nil")
	}

	return l.Code.Validate()
}

// BluetoothInfo is the speaker Bluetooth adapter information.
type BluetoothInfo struct {
	XMLName             xml.Name `xml:"BluetoothInfo"`
	BluetoothMACAddress string   `xml:"BluetoothMACAddress,attr"`
}

// Validate requires the adapter address returned by the firmware.
func (b *BluetoothInfo) Validate() error {
	if b == nil {
		return fmt.Errorf("bluetooth info is nil")
	}

	if b.BluetoothMACAddress == "" {
		return fmt.Errorf("bluetooth info is missing BluetoothMACAddress")
	}

	return nil
}

// UnmarshalXML rejects responses without the adapter address.
func (b *BluetoothInfo) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "BluetoothInfo" {
		return fmt.Errorf("expected BluetoothInfo element, got %s", start.Name.Local)
	}

	var wire struct {
		BluetoothMACAddress string `xml:"BluetoothMACAddress,attr"`
	}
	if err := d.DecodeElement(&wire, &start); err != nil {
		return err
	}

	b.XMLName = start.Name
	b.BluetoothMACAddress = wire.BluetoothMACAddress

	return b.Validate()
}

// SourceRenameRequest is the exact update body accepted by /nameSource.
type SourceRenameRequest struct {
	XMLName       xml.Name `xml:"ContentItem"`
	Source        string   `xml:"source,attr"`
	SourceAccount string   `xml:"sourceAccount,attr,omitempty"`
	ItemName      string   `xml:"itemName"`
}

// Validate requires the source identity and replacement display name.
func (r *SourceRenameRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("source rename request is nil")
	}

	if r.Source == "" {
		return fmt.Errorf("source is required")
	}

	if r.ItemName == "" {
		return fmt.Errorf("item name is required")
	}

	return nil
}

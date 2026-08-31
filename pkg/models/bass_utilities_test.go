package models

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBassCapabilities_UtilityMethods(t *testing.T) {
	t.Run("IsBassSupported", func(t *testing.T) {
		tests := []struct {
			name     string
			caps     BassCapabilities
			expected bool
		}{
			{
				name: "Bass supported device",
				caps: BassCapabilities{
					DeviceID:      "1234",
					BassMin:       -9,
					BassMax:       9,
					BassDefault:   0,
					BassAvailable: true,
				},
				expected: true,
			},
			{
				name: "Bass not supported device",
				caps: BassCapabilities{
					DeviceID:      "5678",
					BassMin:       0,
					BassMax:       0,
					BassDefault:   0,
					BassAvailable: false,
				},
				expected: false,
			},
			{
				name: "Bass not available",
				caps: BassCapabilities{
					DeviceID:      "9999",
					BassMin:       -9,
					BassMax:       9,
					BassDefault:   0,
					BassAvailable: false,
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := tt.caps.IsBassSupported()
				if result != tt.expected {
					t.Errorf("IsBassSupported() = %v, expected %v", result, tt.expected)
				}
			})
		}
	})

	t.Run("GetMinLevel", func(t *testing.T) {
		caps := BassCapabilities{
			DeviceID:      "1234",
			BassMin:       -9,
			BassMax:       9,
			BassDefault:   0,
			BassAvailable: true,
		}

		result := caps.GetMinLevel()
		expected := -9

		if result != expected {
			t.Errorf("GetMinLevel() = %d, expected %d", result, expected)
		}
	})

	t.Run("GetMaxLevel", func(t *testing.T) {
		caps := BassCapabilities{
			DeviceID:      "1234",
			BassMin:       -9,
			BassMax:       9,
			BassDefault:   0,
			BassAvailable: true,
		}

		result := caps.GetMaxLevel()
		expected := 9

		if result != expected {
			t.Errorf("GetMaxLevel() = %d, expected %d", result, expected)
		}
	})

	t.Run("GetDefaultLevel", func(t *testing.T) {
		caps := BassCapabilities{
			DeviceID:      "1234",
			BassMin:       -9,
			BassMax:       9,
			BassDefault:   0,
			BassAvailable: true,
		}

		result := caps.GetDefaultLevel()
		expected := 0

		if result != expected {
			t.Errorf("GetDefaultLevel() = %d, expected %d", result, expected)
		}
	})

	t.Run("ValidateLevel", func(t *testing.T) {
		caps := BassCapabilities{
			DeviceID:      "1234",
			BassMin:       -9,
			BassMax:       9,
			BassDefault:   0,
			BassAvailable: true,
		}

		tests := []struct {
			name     string
			level    int
			expected bool
		}{
			{"Valid min level", -9, true},
			{"Valid max level", 9, true},
			{"Valid middle level", 0, true},
			{"Valid positive level", 3, true},
			{"Valid negative level", -5, true},
			{"Invalid too low", -10, false},
			{"Invalid too high", 10, false},
			{"Invalid way too high", 100, false},
			{"Invalid way too low", -100, false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := caps.ValidateLevel(tt.level)
				if result != tt.expected {
					t.Errorf("ValidateLevel(%d) = %v, expected %v", tt.level, result, tt.expected)
				}
			})
		}
	})

	t.Run("ClampLevel", func(t *testing.T) {
		caps := BassCapabilities{
			DeviceID:      "1234",
			BassMin:       -9,
			BassMax:       9,
			BassDefault:   0,
			BassAvailable: true,
		}

		tests := []struct {
			name     string
			level    int
			expected int
		}{
			{"Valid level unchanged", 5, 5},
			{"Valid min level unchanged", -9, -9},
			{"Valid max level unchanged", 9, 9},
			{"Too low clamped to min", -15, -9},
			{"Too high clamped to max", 15, 9},
			{"Way too low clamped", -100, -9},
			{"Way too high clamped", 100, 9},
			{"Zero unchanged", 0, 0},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := caps.ClampLevel(tt.level)
				if result != tt.expected {
					t.Errorf("ClampLevel(%d) = %d, expected %d", tt.level, result, tt.expected)
				}
			})
		}
	})

	t.Run("String", func(t *testing.T) {
		caps := BassCapabilities{
			DeviceID:      "TEST123",
			BassMin:       -9,
			BassMax:       9,
			BassDefault:   0,
			BassAvailable: true,
		}

		result := caps.String()

		// Check that the string contains expected information
		expected := "Bass: -9 to 9 (default: 0)"
		if result != expected {
			t.Errorf("String() = %q, expected %q", result, expected)
		}
	})

	t.Run("String for unsupported device", func(t *testing.T) {
		caps := BassCapabilities{
			DeviceID:      "TEST456",
			BassMin:       0,
			BassMax:       0,
			BassDefault:   0,
			BassAvailable: false,
		}

		result := caps.String()
		expected := "Bass control not supported"

		if result != expected {
			t.Errorf("String() for unsupported device = %q, expected %q", result, expected)
		}
	})
}

func TestBassCapabilities_Validate(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities *BassCapabilities
		wantError    bool
	}{
		{
			name: "available valid range",
			capabilities: &BassCapabilities{
				BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
			},
		},
		{
			name: "explicit unavailable complete range",
			capabilities: &BassCapabilities{
				BassAvailable: false, BassMin: 0, BassMax: 0, BassDefault: 0,
			},
		},
		{
			name: "minimum exceeds maximum",
			capabilities: &BassCapabilities{
				BassAvailable: true, BassMin: 1, BassMax: 0, BassDefault: 0,
			},
			wantError: true,
		},
		{
			name: "default outside range",
			capabilities: &BassCapabilities{
				BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 1,
			},
			wantError: true,
		},
		{name: "nil", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.capabilities.Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestBassMarshalXML(t *testing.T) {
	bass := Bass{
		DeviceID:   "1234567890AB",
		TargetBass: 5,
		ActualBass: 5,
	}

	var buf strings.Builder

	encoder := xml.NewEncoder(&buf)

	err := bass.MarshalXML(encoder, xml.StartElement{Name: xml.Name{Local: "bass"}})
	if err != nil {
		t.Fatalf("MarshalXML failed: %v", err)
	}

	if err := encoder.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	xmlStr := buf.String()

	// Check that XML contains expected elements
	expectedElements := []string{
		`deviceID="1234567890AB"`,
		`<targetbass>5</targetbass>`,
		`<actualbass>5</actualbass>`,
	}

	for _, expected := range expectedElements {
		if !strings.Contains(xmlStr, expected) {
			t.Errorf("MarshalXML result %q does not contain expected element %q", xmlStr, expected)
		}
	}
}

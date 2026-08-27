package models

import (
	"encoding/xml"
	"testing"
)

func TestNewBalanceRequest(t *testing.T) {
	tests := []struct {
		name      string
		level     int
		wantError bool
		wantLevel int
	}{
		{
			name:      "Valid balance level 0",
			level:     0,
			wantError: false,
			wantLevel: 0,
		},
		{
			name:      "Valid balance level +7",
			level:     7,
			wantError: false,
			wantLevel: 7,
		},
		{
			name:      "Valid balance level -7",
			level:     -7,
			wantError: false,
			wantLevel: -7,
		},
		{
			name:      "Valid balance level +3",
			level:     3,
			wantError: false,
			wantLevel: 3,
		},
		{
			name:      "Valid balance level -3",
			level:     -3,
			wantError: false,
			wantLevel: -3,
		},
		{
			name:      "Invalid balance level +8",
			level:     8,
			wantError: true,
		},
		{
			name:      "Invalid balance level -8",
			level:     -8,
			wantError: true,
		},
		{
			name:      "Invalid balance level +100",
			level:     100,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := NewBalanceRequest(tt.level)
			if tt.wantError {
				if err == nil {
					t.Errorf("NewBalanceRequest() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("NewBalanceRequest() unexpected error: %v", err)
				}

				if req.Level != tt.wantLevel {
					t.Errorf("NewBalanceRequest() level = %d, want %d", req.Level, tt.wantLevel)
				}
			}
		})
	}
}

func TestValidateBalanceLevel(t *testing.T) {
	tests := []struct {
		name  string
		level int
		want  bool
	}{
		{
			name:  "Valid minimum level",
			level: -7,
			want:  true,
		},
		{
			name:  "Valid maximum level",
			level: 7,
			want:  true,
		},
		{
			name:  "Valid zero level",
			level: 0,
			want:  true,
		},
		{
			name:  "Valid positive level",
			level: 3,
			want:  true,
		},
		{
			name:  "Valid negative level",
			level: -3,
			want:  true,
		},
		{
			name:  "Invalid too high",
			level: 8,
			want:  false,
		},
		{
			name:  "Invalid too low",
			level: -8,
			want:  false,
		},
		{
			name:  "Invalid way too high",
			level: 100,
			want:  false,
		},
		{
			name:  "Invalid way too low",
			level: -100,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateBalanceLevel(tt.level); got != tt.want {
				t.Errorf("ValidateBalanceLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClampBalanceLevel(t *testing.T) {
	tests := []struct {
		name  string
		level int
		want  int
	}{
		{
			name:  "Valid level unchanged",
			level: 0,
			want:  0,
		},
		{
			name:  "Valid positive level unchanged",
			level: 3,
			want:  3,
		},
		{
			name:  "Valid negative level unchanged",
			level: -3,
			want:  -3,
		},
		{
			name:  "Maximum level unchanged",
			level: 7,
			want:  7,
		},
		{
			name:  "Minimum level unchanged",
			level: -7,
			want:  -7,
		},
		{
			name:  "Too high clamped to max",
			level: 8,
			want:  7,
		},
		{
			name:  "Too low clamped to min",
			level: -8,
			want:  -7,
		},
		{
			name:  "Way too high clamped to max",
			level: 100,
			want:  7,
		},
		{
			name:  "Way too low clamped to min",
			level: -100,
			want:  -7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampBalanceLevel(tt.level); got != tt.want {
				t.Errorf("ClampBalanceLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetBalanceLevelName(t *testing.T) {
	tests := []struct {
		name  string
		level int
		want  string
	}{
		{
			name:  "Far left balance",
			level: -50,
			want:  "Far Left",
		},
		{
			name:  "Left balance",
			level: -20,
			want:  "Left",
		},
		{
			name:  "Slightly left balance",
			level: -5,
			want:  "Slightly Left",
		},
		{
			name:  "Center balance",
			level: 0,
			want:  "Center",
		},
		{
			name:  "Slightly right balance",
			level: 5,
			want:  "Slightly Right",
		},
		{
			name:  "Right balance",
			level: 20,
			want:  "Right",
		},
		{
			name:  "Far right balance",
			level: 50,
			want:  "Far Right",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetBalanceLevelName(tt.level); got != tt.want {
				t.Errorf("GetBalanceLevelName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetBalanceLevelCategory(t *testing.T) {
	tests := []struct {
		name  string
		level int
		want  string
	}{
		{
			name:  "Left channel negative",
			level: -25,
			want:  "Left Channel",
		},
		{
			name:  "Left channel minimum",
			level: -50,
			want:  "Left Channel",
		},
		{
			name:  "Balanced center",
			level: 0,
			want:  "Balanced",
		},
		{
			name:  "Right channel positive",
			level: 25,
			want:  "Right Channel",
		},
		{
			name:  "Right channel maximum",
			level: 50,
			want:  "Right Channel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetBalanceLevelCategory(tt.level); got != tt.want {
				t.Errorf("GetBalanceLevelCategory() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBalance_GetMethods(t *testing.T) {
	balance := &Balance{
		TargetBalance: 25,
		ActualBalance: 20,
		DeviceID:      "1234567890AB",
	}

	if got := balance.GetLevel(); got != 25 {
		t.Errorf("GetLevel() = %v, want %v", got, 25)
	}

	if got := balance.GetActualLevel(); got != 20 {
		t.Errorf("GetActualLevel() = %v, want %v", got, 20)
	}

	if got := balance.IsAtTarget(); got != false {
		t.Errorf("IsAtTarget() = %v, want %v", got, false)
	}

	if got := balance.GetBalanceChangeNeeded(); got != 5 {
		t.Errorf("GetBalanceChangeNeeded() = %v, want %v", got, 5)
	}
}

func TestBalance_BooleanMethods(t *testing.T) {
	tests := []struct {
		name         string
		balance      *Balance
		wantLeft     bool
		wantRight    bool
		wantBalanced bool
		wantAtTarget bool
	}{
		{
			name:         "Right balance",
			balance:      &Balance{TargetBalance: 25, ActualBalance: 25},
			wantLeft:     false,
			wantRight:    true,
			wantBalanced: false,
			wantAtTarget: true,
		},
		{
			name:         "Left balance",
			balance:      &Balance{TargetBalance: -15, ActualBalance: -15},
			wantLeft:     true,
			wantRight:    false,
			wantBalanced: false,
			wantAtTarget: true,
		},
		{
			name:         "Center balance",
			balance:      &Balance{TargetBalance: 0, ActualBalance: 0},
			wantLeft:     false,
			wantRight:    false,
			wantBalanced: true,
			wantAtTarget: true,
		},
		{
			name:         "Not at target",
			balance:      &Balance{TargetBalance: 25, ActualBalance: 10},
			wantLeft:     false,
			wantRight:    true,
			wantBalanced: false,
			wantAtTarget: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.balance.IsLeftBalance(); got != tt.wantLeft {
				t.Errorf("IsLeftBalance() = %v, want %v", got, tt.wantLeft)
			}

			if got := tt.balance.IsRightBalance(); got != tt.wantRight {
				t.Errorf("IsRightBalance() = %v, want %v", got, tt.wantRight)
			}

			if got := tt.balance.IsBalanced(); got != tt.wantBalanced {
				t.Errorf("IsBalanced() = %v, want %v", got, tt.wantBalanced)
			}

			if got := tt.balance.IsAtTarget(); got != tt.wantAtTarget {
				t.Errorf("IsAtTarget() = %v, want %v", got, tt.wantAtTarget)
			}
		})
	}
}

func TestBalance_GetLeftRightPercentage(t *testing.T) {
	tests := []struct {
		name      string
		balance   *Balance
		wantLeft  int
		wantRight int
	}{
		{
			name:      "Center balance",
			balance:   &Balance{TargetBalance: 0},
			wantLeft:  50,
			wantRight: 50,
		},
		{
			name:      "Right balance +20",
			balance:   &Balance{TargetBalance: 20},
			wantLeft:  40,
			wantRight: 60,
		},
		{
			name:      "Left balance -20",
			balance:   &Balance{TargetBalance: -20},
			wantLeft:  60,
			wantRight: 40,
		},
		{
			name:      "Far right +50",
			balance:   &Balance{TargetBalance: 50},
			wantLeft:  25,
			wantRight: 75,
		},
		{
			name:      "Far left -50",
			balance:   &Balance{TargetBalance: -50},
			wantLeft:  75,
			wantRight: 25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := tt.balance.GetLeftRightPercentage()
			if left != tt.wantLeft {
				t.Errorf("GetLeftRightPercentage() left = %v, want %v", left, tt.wantLeft)
			}

			if right != tt.wantRight {
				t.Errorf("GetLeftRightPercentage() right = %v, want %v", right, tt.wantRight)
			}
		})
	}
}

func TestBalance_String(t *testing.T) {
	balance := &Balance{
		TargetBalance: 15,
		ActualBalance: 15,
	}

	expected := "Balance: 15 (Right)"
	if got := balance.String(); got != expected {
		t.Errorf("String() = %v, want %v", got, expected)
	}
}

func TestBalance_UnmarshalXML(t *testing.T) {
	tests := []struct {
		name      string
		xmlData   string
		wantError bool
		want      Balance
	}{
		{
			name: "Known unavailable unpaired balance",
			xmlData: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="1234567890AB">
  <balanceAvailable>false</balanceAvailable>
  <balanceMin>-7</balanceMin>
  <balanceMax>7</balanceMax>
  <balanceDefault>0</balanceDefault>
  <targetBalance>0</targetBalance>
  <actualBalance>0</actualBalance>
</balance>`,
			wantError: false,
			want: Balance{
				DeviceID:         "1234567890AB",
				BalanceAvailable: false,
				BalanceMin:       -7,
				BalanceMax:       7,
				BalanceDefault:   0,
				TargetBalance:    0,
				ActualBalance:    0,
				CapabilityKnown:  true,
			},
		},
		{
			name: "Known available ST10 range",
			xmlData: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="1234567890AB">
  <balanceAvailable>true</balanceAvailable>
  <balanceMin>-7</balanceMin>
  <balanceMax>7</balanceMax>
  <balanceDefault>0</balanceDefault>
  <targetBalance>-6</targetBalance>
  <actualBalance>-5</actualBalance>
</balance>`,
			wantError: false,
			want: Balance{
				DeviceID:         "1234567890AB",
				BalanceAvailable: true,
				BalanceMin:       -7,
				BalanceMax:       7,
				BalanceDefault:   0,
				TargetBalance:    -6,
				ActualBalance:    -5,
				CapabilityKnown:  true,
			},
		},
		{
			name: "Hypothetical different valid range",
			xmlData: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="1234567890AB">
  <balanceAvailable>true</balanceAvailable>
  <balanceMin>-12</balanceMin>
  <balanceMax>9</balanceMax>
  <balanceDefault>1</balanceDefault>
  <targetBalance>8</targetBalance>
  <actualBalance>7</actualBalance>
</balance>`,
			wantError: false,
			want: Balance{
				DeviceID:         "1234567890AB",
				BalanceAvailable: true,
				BalanceMin:       -12,
				BalanceMax:       9,
				BalanceDefault:   1,
				TargetBalance:    8,
				ActualBalance:    7,
				CapabilityKnown:  true,
			},
		},
		{
			name: "Legacy capability remains unknown",
			xmlData: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="1234567890AB">
  <targetbalance>6</targetbalance>
  <actualbalance>5</actualbalance>
</balance>`,
			wantError: false,
			want: Balance{
				DeviceID:      "1234567890AB",
				TargetBalance: 6,
				ActualBalance: 5,
			},
		},
		{
			name: "Partial capability is malformed",
			xmlData: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="1234567890AB">
  <balanceAvailable>true</balanceAvailable>
  <balanceMin>-7</balanceMin>
  <targetBalance>0</targetBalance>
  <actualBalance>0</actualBalance>
</balance>`,
			wantError: true,
		},
		{
			name: "Reversed capability range is malformed",
			xmlData: `<balance deviceID="1234567890AB">
  <balanceAvailable>true</balanceAvailable>
  <balanceMin>7</balanceMin>
  <balanceMax>-7</balanceMax>
  <balanceDefault>0</balanceDefault>
  <targetBalance>0</targetBalance>
  <actualBalance>0</actualBalance>
</balance>`,
			wantError: true,
		},
		{
			name: "Readback outside advertised range is malformed",
			xmlData: `<balance deviceID="1234567890AB">
  <balanceAvailable>true</balanceAvailable>
  <balanceMin>-7</balanceMin>
  <balanceMax>7</balanceMax>
  <balanceDefault>0</balanceDefault>
  <targetBalance>8</targetBalance>
  <actualBalance>7</actualBalance>
</balance>`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var balance Balance

			err := xml.Unmarshal([]byte(tt.xmlData), &balance)

			if tt.wantError {
				if err == nil {
					t.Errorf("UnmarshalXML() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("UnmarshalXML() unexpected error: %v", err)
				}

				if balance.DeviceID != tt.want.DeviceID {
					t.Errorf("DeviceID = %v, want %v", balance.DeviceID, tt.want.DeviceID)
				}

				if balance.TargetBalance != tt.want.TargetBalance {
					t.Errorf("TargetBalance = %v, want %v", balance.TargetBalance, tt.want.TargetBalance)
				}

				if balance.ActualBalance != tt.want.ActualBalance {
					t.Errorf("ActualBalance = %v, want %v", balance.ActualBalance, tt.want.ActualBalance)
				}

				if balance.CapabilityKnown != tt.want.CapabilityKnown {
					t.Errorf("CapabilityKnown = %v, want %v", balance.CapabilityKnown, tt.want.CapabilityKnown)
				}

				if balance.BalanceAvailable != tt.want.BalanceAvailable || balance.BalanceMin != tt.want.BalanceMin ||
					balance.BalanceMax != tt.want.BalanceMax || balance.BalanceDefault != tt.want.BalanceDefault {
					t.Errorf("capability = %+v, want %+v", balance, tt.want)
				}
			}
		})
	}
}

func TestBalanceCapabilityValidation(t *testing.T) {
	balance := &Balance{
		BalanceAvailable: true,
		BalanceMin:       -12,
		BalanceMax:       9,
		BalanceDefault:   1,
		CapabilityKnown:  true,
	}

	if !balance.ValidateRequestedLevel(-12) || !balance.ValidateRequestedLevel(9) {
		t.Fatal("advertised endpoints should be valid")
	}
	if balance.ValidateRequestedLevel(-13) || balance.ValidateRequestedLevel(10) {
		t.Fatal("values outside the advertised range should be invalid")
	}

	balance.BalanceAvailable = false
	if balance.ValidateRequestedLevel(0) {
		t.Fatal("an unavailable capability must reject writes")
	}
}

func TestBalance_MarshalXML(t *testing.T) {
	tests := []struct {
		name      string
		balance   Balance
		wantError bool
	}{
		{
			name: "Valid balance marshal",
			balance: Balance{
				DeviceID:      "1234567890AB",
				TargetBalance: 15,
				ActualBalance: 15,
			},
			wantError: false,
		},
		{
			name: "Valid negative balance marshal",
			balance: Balance{
				DeviceID:      "1234567890AB",
				TargetBalance: -25,
				ActualBalance: -25,
			},
			wantError: false,
		},
		{
			name: "Valid zero balance marshal",
			balance: Balance{
				DeviceID:      "1234567890AB",
				TargetBalance: 0,
				ActualBalance: 0,
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := xml.Marshal(tt.balance)

			if tt.wantError {
				if err == nil {
					t.Errorf("MarshalXML() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("MarshalXML() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestBalanceRequest_MarshalXML(t *testing.T) {
	req := &BalanceRequest{
		Level: 25,
	}

	data, err := xml.Marshal(req)
	if err != nil {
		t.Errorf("MarshalXML() unexpected error: %v", err)
	}

	expected := "<balance>25</balance>"
	if string(data) != expected {
		t.Errorf("MarshalXML() = %v, want %v", string(data), expected)
	}
}

func TestBalanceConstants(t *testing.T) {
	if BalanceLevelMin != -7 {
		t.Errorf("BalanceLevelMin = %v, want %v", BalanceLevelMin, -7)
	}

	if BalanceLevelMax != 7 {
		t.Errorf("BalanceLevelMax = %v, want %v", BalanceLevelMax, 7)
	}

	if BalanceLevelDefault != 0 {
		t.Errorf("BalanceLevelDefault = %v, want %v", BalanceLevelDefault, 0)
	}
}

func TestBalanceLevelEdgeCases(t *testing.T) {
	// Test boundary values
	t.Run("Minimum boundary", func(t *testing.T) {
		if !ValidateBalanceLevel(-7) {
			t.Error("ValidateBalanceLevel(-7) should be true")
		}

		if ValidateBalanceLevel(-8) {
			t.Error("ValidateBalanceLevel(-8) should be false")
		}
	})

	t.Run("Maximum boundary", func(t *testing.T) {
		if !ValidateBalanceLevel(7) {
			t.Error("ValidateBalanceLevel(7) should be true")
		}

		if ValidateBalanceLevel(8) {
			t.Error("ValidateBalanceLevel(8) should be false")
		}
	})

	t.Run("Zero boundary", func(t *testing.T) {
		if !ValidateBalanceLevel(0) {
			t.Error("ValidateBalanceLevel(0) should be true")
		}
	})
}

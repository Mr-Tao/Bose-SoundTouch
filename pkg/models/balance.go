// Package models provides data structures and types for the Bose SoundTouch API.
package models

import (
	"encoding/xml"
	"fmt"
)

// Balance represents the response from /balance endpoint
type Balance struct {
	XMLName          xml.Name `xml:"balance" json:"-"`
	DeviceID         string   `xml:"deviceID,attr" json:"deviceID,omitempty"`
	BalanceAvailable bool     `xml:"-" json:"balanceAvailable"`
	BalanceMin       int      `xml:"-" json:"balanceMin"`
	BalanceMax       int      `xml:"-" json:"balanceMax"`
	BalanceDefault   int      `xml:"-" json:"balanceDefault"`
	TargetBalance    int      `xml:"-" json:"targetBalance"`
	ActualBalance    int      `xml:"-" json:"actualBalance"`
	CapabilityKnown  bool     `xml:"-" json:"capabilityKnown"`
}

type balanceXML struct {
	DeviceID            string `xml:"deviceID,attr"`
	BalanceAvailable    *bool  `xml:"balanceAvailable,omitempty"`
	BalanceMin          *int   `xml:"balanceMin,omitempty"`
	BalanceMax          *int   `xml:"balanceMax,omitempty"`
	BalanceDefault      *int   `xml:"balanceDefault,omitempty"`
	TargetBalance       *int   `xml:"targetBalance,omitempty"`
	ActualBalance       *int   `xml:"actualBalance,omitempty"`
	LegacyTargetBalance *int   `xml:"targetbalance,omitempty"`
	LegacyActualBalance *int   `xml:"actualbalance,omitempty"`
}

// BalanceRequest represents the request for POST /balance endpoint
type BalanceRequest struct {
	XMLName xml.Name `xml:"balance"`
	Level   int      `xml:"targetBalance"`
}

// Balance level constants are the conservative fallback used by callers that
// do not yet have a confirmed /balance capability response.
const (
	BalanceLevelMin     = -7
	BalanceLevelMax     = 7
	BalanceLevelDefault = 0
)

// NewBalanceRequest creates a new balance request with validation
func NewBalanceRequest(level int) (*BalanceRequest, error) {
	return NewBalanceRequestForRange(level, BalanceLevelMin, BalanceLevelMax)
}

// NewBalanceRequestForRange creates a balance request validated against a
// capability range confirmed by the caller.
func NewBalanceRequestForRange(level, minLevel, maxLevel int) (*BalanceRequest, error) {
	if !ValidateBalanceLevelForRange(level, minLevel, maxLevel) {
		return nil, fmt.Errorf("invalid balance level: %d (must be between %d and %d)", level, minLevel, maxLevel)
	}

	return &BalanceRequest{
		Level: level,
	}, nil
}

// ValidateBalanceLevel validates that a balance level is within the allowed range
func ValidateBalanceLevel(level int) bool {
	return ValidateBalanceLevelForRange(level, BalanceLevelMin, BalanceLevelMax)
}

// ValidateBalanceLevelForRange validates a balance level against an explicit
// device-advertised range.
func ValidateBalanceLevelForRange(level, minLevel, maxLevel int) bool {
	return minLevel <= maxLevel && level >= minLevel && level <= maxLevel
}

// ClampBalanceLevel clamps a balance level to the valid range
func ClampBalanceLevel(level int) int {
	return ClampBalanceLevelForRange(level, BalanceLevelMin, BalanceLevelMax)
}

// ClampBalanceLevelForRange clamps a balance level to an explicit range.
func ClampBalanceLevelForRange(level, minLevel, maxLevel int) int {
	if minLevel > maxLevel {
		return level
	}

	if level < minLevel {
		return minLevel
	}

	if level > maxLevel {
		return maxLevel
	}

	return level
}

// Capability returns the latest advertised balance capability when all
// capability fields are present and internally consistent.
func (b *Balance) Capability() (available bool, minLevel, maxLevel, defaultLevel int, ok bool) {
	if b == nil || !b.CapabilityKnown || b.BalanceMin > b.BalanceMax ||
		!ValidateBalanceLevelForRange(b.BalanceDefault, b.BalanceMin, b.BalanceMax) {
		return false, 0, 0, 0, false
	}

	return b.BalanceAvailable, b.BalanceMin, b.BalanceMax, b.BalanceDefault, true
}

// ValidateRequestedLevel reports whether the device currently advertises an
// available capability whose range includes level.
func (b *Balance) ValidateRequestedLevel(level int) bool {
	available, minLevel, maxLevel, _, ok := b.Capability()

	return ok && available && ValidateBalanceLevelForRange(level, minLevel, maxLevel)
}

// GetLevel returns the target balance level
func (b *Balance) GetLevel() int {
	return b.TargetBalance
}

// GetActualLevel returns the actual balance level
func (b *Balance) GetActualLevel() int {
	return b.ActualBalance
}

// IsAtTarget returns true if actual balance matches target balance
func (b *Balance) IsAtTarget() bool {
	return b.TargetBalance == b.ActualBalance
}

// GetBalanceLevelName returns a descriptive name for the balance level
func GetBalanceLevelName(level int) string {
	switch {
	case level < -30:
		return "Far Left"
	case level < -10:
		return "Left"
	case level < 0:
		return "Slightly Left"
	case level == 0:
		return "Center"
	case level <= 10:
		return "Slightly Right"
	case level <= 30:
		return "Right"
	default:
		return "Far Right"
	}
}

// GetBalanceLevelCategory returns the balance category
func GetBalanceLevelCategory(level int) string {
	switch {
	case level < 0:
		return "Left Channel"
	case level == 0:
		return "Balanced"
	default:
		return "Right Channel"
	}
}

// String returns a human-readable string representation
func (b *Balance) String() string {
	return fmt.Sprintf("Balance: %d (%s)", b.GetLevel(), GetBalanceLevelName(b.GetLevel()))
}

// UnmarshalXML accepts the camel-case fields emitted by current SoundTouch
// firmware as well as the legacy lower-case target/actual field spelling.
func (b *Balance) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var wire balanceXML
	if err := d.DecodeElement(&wire, &start); err != nil {
		return err
	}

	*b = Balance{XMLName: start.Name, DeviceID: wire.DeviceID}

	target := wire.TargetBalance
	if target == nil {
		target = wire.LegacyTargetBalance
	}

	actual := wire.ActualBalance
	if actual == nil {
		actual = wire.LegacyActualBalance
	}

	if target != nil {
		b.TargetBalance = *target
	}

	if actual != nil {
		b.ActualBalance = *actual
	}

	capabilityFields := 0

	for _, present := range []bool{
		wire.BalanceAvailable != nil,
		wire.BalanceMin != nil,
		wire.BalanceMax != nil,
		wire.BalanceDefault != nil,
	} {
		if present {
			capabilityFields++
		}
	}

	if capabilityFields == 0 {
		return nil
	}

	if capabilityFields != 4 {
		return fmt.Errorf("incomplete balance capability response")
	}

	b.BalanceAvailable = *wire.BalanceAvailable
	b.BalanceMin = *wire.BalanceMin
	b.BalanceMax = *wire.BalanceMax
	b.BalanceDefault = *wire.BalanceDefault

	b.CapabilityKnown = true
	if _, _, _, _, ok := b.Capability(); !ok {
		return fmt.Errorf("invalid balance capability range %d..%d with default %d", b.BalanceMin, b.BalanceMax, b.BalanceDefault)
	}

	if target == nil || actual == nil {
		return fmt.Errorf("incomplete balance readback response")
	}

	if !ValidateBalanceLevelForRange(b.TargetBalance, b.BalanceMin, b.BalanceMax) {
		return fmt.Errorf("invalid target balance level: %d", b.TargetBalance)
	}

	if !ValidateBalanceLevelForRange(b.ActualBalance, b.BalanceMin, b.BalanceMax) {
		return fmt.Errorf("invalid actual balance level: %d", b.ActualBalance)
	}

	return nil
}

// MarshalXML implements custom XML marshaling
func (b *Balance) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if start.Name.Local == "" {
		start.Name.Local = "balance"
	}

	if b.CapabilityKnown {
		if _, _, _, _, ok := b.Capability(); !ok {
			return fmt.Errorf("invalid balance capability range %d..%d with default %d", b.BalanceMin, b.BalanceMax, b.BalanceDefault)
		}
	}

	target := b.TargetBalance
	actual := b.ActualBalance

	wire := balanceXML{
		DeviceID:      b.DeviceID,
		TargetBalance: &target,
		ActualBalance: &actual,
	}
	if b.CapabilityKnown {
		wire.BalanceAvailable = &b.BalanceAvailable
		wire.BalanceMin = &b.BalanceMin
		wire.BalanceMax = &b.BalanceMax
		wire.BalanceDefault = &b.BalanceDefault
	}

	return e.EncodeElement(wire, start)
}

// IsLeftBalance returns true if balance favors left channel (negative level)
func (b *Balance) IsLeftBalance() bool {
	return b.GetLevel() < 0
}

// IsRightBalance returns true if balance favors right channel (positive level)
func (b *Balance) IsRightBalance() bool {
	return b.GetLevel() > 0
}

// IsBalanced returns true if balance is centered (zero level)
func (b *Balance) IsBalanced() bool {
	return b.GetLevel() == 0
}

// GetBalanceChangeNeeded returns the amount of change needed to reach target from actual
func (b *Balance) GetBalanceChangeNeeded() int {
	return b.TargetBalance - b.ActualBalance
}

// GetLeftRightPercentage returns the balance as left/right percentages
func (b *Balance) GetLeftRightPercentage() (left, right int) {
	level := b.GetLevel()
	if level <= 0 {
		// Left emphasis or center
		left = 50 + (-level / 2)
		right = 50 - (-level / 2)
	} else {
		// Right emphasis
		left = 50 - (level / 2)
		right = 50 + (level / 2)
	}

	return left, right
}

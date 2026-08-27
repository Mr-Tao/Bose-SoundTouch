package models

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBalanceMarshalXML(t *testing.T) {
	balance := Balance{
		DeviceID:      "1234567890AB",
		TargetBalance: -25,
		ActualBalance: -25,
	}

	var buf strings.Builder

	encoder := xml.NewEncoder(&buf)

	err := balance.MarshalXML(encoder, xml.StartElement{Name: xml.Name{Local: "balance"}})
	if err != nil {
		t.Fatalf("MarshalXML failed: %v", err)
	}

	if err := encoder.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Convert to string for easier testing
	xmlStr := buf.String()

	// Check that XML contains expected elements
	expectedElements := []string{
		`deviceID="1234567890AB"`,
		`<targetBalance>-25</targetBalance>`,
		`<actualBalance>-25</actualBalance>`,
	}

	for _, expected := range expectedElements {
		if !strings.Contains(xmlStr, expected) {
			t.Errorf("MarshalXML result %q does not contain expected element %q", xmlStr, expected)
		}
	}
}

func TestBalanceMarshalXML_PositiveValue(t *testing.T) {
	balance := Balance{
		DeviceID:      "ABCDEF123456",
		TargetBalance: 30,
		ActualBalance: 30,
	}

	var buf strings.Builder

	encoder := xml.NewEncoder(&buf)

	err := balance.MarshalXML(encoder, xml.StartElement{Name: xml.Name{Local: "balance"}})
	if err != nil {
		t.Fatalf("MarshalXML failed: %v", err)
	}

	if err := encoder.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	xmlStr := buf.String()
	expectedElements := []string{
		`deviceID="ABCDEF123456"`,
		`<targetBalance>30</targetBalance>`,
		`<actualBalance>30</actualBalance>`,
	}

	for _, expected := range expectedElements {
		if !strings.Contains(xmlStr, expected) {
			t.Errorf("MarshalXML result %q does not contain expected element %q", xmlStr, expected)
		}
	}
}

func TestBalanceMarshalXML_ZeroValue(t *testing.T) {
	balance := Balance{
		DeviceID:      "ZERO0000TEST",
		TargetBalance: 0,
		ActualBalance: 0,
	}

	var buf strings.Builder

	encoder := xml.NewEncoder(&buf)

	err := balance.MarshalXML(encoder, xml.StartElement{Name: xml.Name{Local: "balance"}})
	if err != nil {
		t.Fatalf("MarshalXML failed: %v", err)
	}

	if err := encoder.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	xmlStr := buf.String()
	expectedElements := []string{
		`deviceID="ZERO0000TEST"`,
		`<targetBalance>0</targetBalance>`,
		`<actualBalance>0</actualBalance>`,
	}

	for _, expected := range expectedElements {
		if !strings.Contains(xmlStr, expected) {
			t.Errorf("MarshalXML result %q does not contain expected element %q", xmlStr, expected)
		}
	}
}

func TestBalanceMarshalXML_ExtremeValues(t *testing.T) {
	balance := Balance{
		DeviceID:      "EXTREME_TEST",
		TargetBalance: -50, // Min value
		ActualBalance: 50,  // Max value
	}

	var buf strings.Builder

	encoder := xml.NewEncoder(&buf)

	err := balance.MarshalXML(encoder, xml.StartElement{Name: xml.Name{Local: "balance"}})
	if err != nil {
		t.Fatalf("MarshalXML failed: %v", err)
	}

	if err := encoder.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	xmlStr := buf.String()
	expectedElements := []string{
		`deviceID="EXTREME_TEST"`,
		`<targetBalance>-50</targetBalance>`,
		`<actualBalance>50</actualBalance>`,
	}

	for _, expected := range expectedElements {
		if !strings.Contains(xmlStr, expected) {
			t.Errorf("MarshalXML result %q does not contain expected element %q", xmlStr, expected)
		}
	}
}

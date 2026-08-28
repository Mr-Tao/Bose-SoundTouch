package models

import (
	"encoding/xml"
	"testing"
)

func TestNetworkStatsUnmarshalAndFindRunningWireless(t *testing.T) {
	data := `<network-data><devices><device id="radio">
<interfaces>
<interface><name>eth0</name><mac-addr>11:22:33:44:55:66</mac-addr><bindings><ipv4address>192.0.2.10</ipv4address></bindings><running>true</running><kind>Wireless</kind><ssid>Test WiFi</ssid><rssi>Marginal</rssi><frequencyKHz>5200000</frequencyKHz></interface>
<interface><name>wlan0</name><mac-addr>AA:BB:CC:DD:EE:FF</mac-addr><bindings/><running>false</running><kind>Wireless</kind><rssi>Poor</rssi></interface>
</interfaces></device></devices></network-data>`

	var stats NetworkStats
	if err := xml.Unmarshal([]byte(data), &stats); err != nil {
		t.Fatalf("unmarshal network stats: %v", err)
	}

	iface := stats.FindRunningWireless("192.0.2.10", "Test WiFi")
	if iface == nil {
		t.Fatal("running wireless interface was not found")
	}
	if iface.Name != "eth0" || iface.MACAddress != "11:22:33:44:55:66" || iface.RSSI != "Marginal" || iface.FrequencyKHz != 5200000 {
		t.Fatalf("unexpected running wireless interface: %+v", iface)
	}
}

func TestNetworkStatsFindRunningWirelessUsesSemanticTieBreakers(t *testing.T) {
	stats := NetworkStats{Devices: []NetworkStatsDevice{{Interfaces: []NetworkStatsInterface{
		{Running: true, Kind: "Wireless", SSID: "Other", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.20"}},
		{Running: true, Kind: "wireless", SSID: "Target", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.10"}, RSSI: "Good"},
		{Running: true, Kind: "Ethernet", SSID: "Target", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.10"}, RSSI: "Poor"},
	}}}}

	iface := stats.FindRunningWireless("192.0.2.10", "Target")
	if iface == nil || iface.RSSI != "Good" {
		t.Fatalf("semantic match = %+v, want unique running wireless target", iface)
	}

	if iface := stats.FindRunningWireless("", ""); iface != nil {
		t.Fatalf("identity-free running wireless match = %+v, want nil", iface)
	}
}

func TestNetworkStatsFindRunningWirelessRequiresUniqueConsistentIdentity(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		ssid string
		list []NetworkStatsInterface
		want string
	}{
		{
			name: "both expected values match",
			ip:   "192.0.2.10",
			ssid: "Target",
			list: []NetworkStatsInterface{
				{Running: true, Kind: "Wireless", Name: "wrong-ssid", SSID: "Other", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.10"}},
				{Running: true, Kind: "Wireless", Name: "match", SSID: "Target", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.10"}},
			},
			want: "match",
		},
		{
			name: "IP match cannot override conflicting expected SSID",
			ip:   "192.0.2.10",
			ssid: "Target",
			list: []NetworkStatsInterface{{Running: true, Kind: "Wireless", Name: "only", SSID: "Other", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.10"}}},
		},
		{
			name: "SSID match cannot override conflicting expected IP",
			ip:   "192.0.2.10",
			ssid: "Target",
			list: []NetworkStatsInterface{{Running: true, Kind: "Wireless", Name: "only", SSID: "Target", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.99"}}},
		},
		{
			name: "sole wrong candidate is rejected",
			ip:   "192.0.2.10",
			list: []NetworkStatsInterface{{Running: true, Kind: "Wireless", Name: "only", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.99"}}},
		},
		{
			name: "IP-only evidence must be unique",
			ip:   "192.0.2.10",
			list: []NetworkStatsInterface{
				{Running: true, Kind: "Wireless", Name: "first", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.10"}},
				{Running: true, Kind: "Wireless", Name: "second", Bindings: NetworkStatsBindings{IPv4Address: "192.0.2.10"}},
			},
		},
		{
			name: "SSID-only evidence must be unique",
			ssid: "Target",
			list: []NetworkStatsInterface{
				{Running: true, Kind: "Wireless", Name: "first", SSID: "Target"},
				{Running: true, Kind: "Wireless", Name: "second", SSID: "Target"},
			},
		},
		{
			name: "SSID-only unique match",
			ssid: "Target",
			list: []NetworkStatsInterface{
				{Running: true, Kind: "Wireless", Name: "other", SSID: "Other"},
				{Running: true, Kind: "Wireless", Name: "match", SSID: "Target"},
			},
			want: "match",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stats := NetworkStats{Devices: []NetworkStatsDevice{{Interfaces: test.list}}}
			match := stats.FindRunningWireless(test.ip, test.ssid)
			if test.want == "" {
				if match != nil {
					t.Fatalf("match = %+v, want nil", match)
				}
				return
			}
			if match == nil || match.Name != test.want {
				t.Fatalf("match = %+v, want %q", match, test.want)
			}
		})
	}
}

func TestNetworkStatsInterfaceCanonicalRSSI(t *testing.T) {
	tests := []struct {
		input string
		want  string
		valid bool
	}{
		{input: "Excellent", want: "Excellent", valid: true},
		{input: " GOOD ", want: "Good", valid: true},
		{input: "FaIr", want: "Fair", valid: true},
		{input: "mArGiNaL", want: "Marginal", valid: true},
		{input: "poor", want: "Poor", valid: true},
		{input: ""},
		{input: "-54 dBm"},
		{input: "Unknown"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, valid := (&NetworkStatsInterface{RSSI: test.input}).CanonicalRSSI()
			if got != test.want || valid != test.valid {
				t.Fatalf("CanonicalRSSI(%q) = (%q, %t), want (%q, %t)", test.input, got, valid, test.want, test.valid)
			}
		})
	}
}

package models

import (
	"encoding/xml"
	"strings"
)

// NetworkStats represents firmware network data from the /netStats endpoint.
type NetworkStats struct {
	XMLName xml.Name             `xml:"network-data"`
	Devices []NetworkStatsDevice `xml:"devices>device"`
}

// NetworkStatsDevice contains the interfaces reported for one network device.
type NetworkStatsDevice struct {
	Interfaces []NetworkStatsInterface `xml:"interfaces>interface"`
}

// NetworkStatsInterface is one interface reported by /netStats.
type NetworkStatsInterface struct {
	Name         string               `xml:"name"`
	MACAddress   string               `xml:"mac-addr"`
	Bindings     NetworkStatsBindings `xml:"bindings"`
	Running      bool                 `xml:"running"`
	Kind         string               `xml:"kind"`
	SSID         string               `xml:"ssid"`
	RSSI         string               `xml:"rssi"`
	FrequencyKHz int                  `xml:"frequencyKHz"`
}

// NetworkStatsBindings contains assigned interface addresses.
type NetworkStatsBindings struct {
	IPv4Address string `xml:"ipv4address"`
}

// FindRunningWireless returns the unique running wireless interface identified
// by the available /networkInfo IP address and SSID evidence.
func (n *NetworkStats) FindRunningWireless(ipAddress, ssid string) *NetworkStatsInterface {
	expectedIP := strings.TrimSpace(ipAddress)
	expectedSSID := strings.TrimSpace(ssid)

	if expectedIP == "" && expectedSSID == "" {
		return nil
	}

	var selected *NetworkStatsInterface

	for deviceIndex := range n.Devices {
		interfaces := n.Devices[deviceIndex].Interfaces
		for interfaceIndex := range interfaces {
			iface := &interfaces[interfaceIndex]
			if !iface.Running || !strings.EqualFold(strings.TrimSpace(iface.Kind), "Wireless") {
				continue
			}

			candidateIP := strings.TrimSpace(iface.Bindings.IPv4Address)
			candidateSSID := strings.TrimSpace(iface.SSID)

			if expectedIP != "" && candidateIP != expectedIP {
				continue
			}

			if expectedSSID != "" && candidateSSID != expectedSSID {
				continue
			}

			if selected != nil {
				return nil
			}

			selected = iface
		}
	}

	return selected
}

// CanonicalRSSI validates the categorical value carried in the /netStats RSSI
// field. The firmware category is not a measured per-interface RSSI value.
func (i *NetworkStatsInterface) CanonicalRSSI() (string, bool) {
	switch strings.ToLower(strings.TrimSpace(i.RSSI)) {
	case "excellent":
		return "Excellent", true
	case "good":
		return "Good", true
	case "fair":
		return "Fair", true
	case "marginal":
		return "Marginal", true
	case "poor":
		return "Poor", true
	default:
		return "", false
	}
}

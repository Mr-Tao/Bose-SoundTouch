package models

import (
	"encoding/xml"
	"strings"
)

// Group represents a stereo pair of two ST10 SoundTouch speakers.
type Group struct {
	XMLName         xml.Name   `xml:"group"`
	ID              string     `xml:"id,attr,omitempty"`
	Name            string     `xml:"name"`
	MasterDeviceID  string     `xml:"masterDeviceId"`
	Roles           GroupRoles `xml:"roles"`
	SenderIPAddress string     `xml:"senderIPAddress,omitempty"`
	// Status is populated by the device on GET /group (e.g. "GROUP_OK")
	// and omitted from requests we send back.
	Status string `xml:"status,omitempty"`
}

// IsEmpty reports whether the device returned an empty <group/> element,
// which is the speaker's way of saying "no stereo pair configured".
func (g *Group) IsEmpty() bool {
	return g.ID == "" && g.MasterDeviceID == "" && len(g.Roles.Roles) == 0
}

// GroupRoles contains the role assignments for devices in a group.
type GroupRoles struct {
	Roles []GroupRole `xml:"groupRole"`
}

// GroupRole describes the role (LEFT or RIGHT) of a single device in a group.
type GroupRole struct {
	DeviceID  string `xml:"deviceId"`
	Role      string `xml:"role"`
	IPAddress string `xml:"ipAddress,omitempty"`
}

// SameGroup reports whether left and right describe the same stereo-pair
// configuration, comparing role assignments by device ID rather than by
// slice order. The device's own /getGroup response and its groupUpdated
// WebSocket event both populate Roles.Roles directly from XML unmarshaling
// in wire order, so a polled read and a pushed event for the identical pair
// are not guaranteed to list roles in the same order -- comparing with
// reflect.DeepEqual (order-sensitive) would then report a spurious change
// even though nothing about the pair actually changed. Two nil Groups are
// equal; exactly one nil is not.
func SameGroup(left, right *Group) bool {
	if left == nil && right == nil {
		return true
	}

	if left == nil || right == nil {
		return false
	}

	if left.ID != right.ID || left.MasterDeviceID != right.MasterDeviceID ||
		len(left.Roles.Roles) != len(right.Roles.Roles) {
		return false
	}

	rightRoles := make(map[string]string, len(right.Roles.Roles))
	for _, role := range right.Roles.Roles {
		rightRoles[strings.TrimSpace(role.DeviceID)] = strings.ToUpper(strings.TrimSpace(role.Role))
	}

	for _, role := range left.Roles.Roles {
		if rightRoles[strings.TrimSpace(role.DeviceID)] != strings.ToUpper(strings.TrimSpace(role.Role)) {
			return false
		}
	}

	return true
}

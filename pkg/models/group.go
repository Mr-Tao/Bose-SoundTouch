package models

import (
	"encoding/xml"
	"net"
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

// SameGroupRoles reports whether two role slices describe the same stereo
// pair topology: matching length, no duplicate Role value on either side, and
// every role paired by Role with equal DeviceID and IPAddress. It tolerates
// any role count rather than assuming exactly LEFT/RIGHT.
//
// This is the shared core behind both pkg/stereopair's and
// pkg/service/datastore's topology-equality checks -- they used to compare
// IPAddress independently (one via net.ParseIP, one via plain string
// equality), which could disagree about whether two differently-formatted
// but equal addresses matched. See i655 code-review finding #10.
func SameGroupRoles(a, b []GroupRole) bool {
	if len(a) != len(b) {
		return false
	}

	byRole := make(map[string]GroupRole, len(a))
	for _, role := range a {
		if _, duplicate := byRole[role.Role]; duplicate {
			return false
		}

		byRole[role.Role] = role
	}

	seen := make(map[string]struct{}, len(b))
	for _, role := range b {
		if _, duplicate := seen[role.Role]; duplicate {
			return false
		}

		seen[role.Role] = struct{}{}

		other, ok := byRole[role.Role]
		if !ok || other.DeviceID != role.DeviceID || !sameRoleIPAddress(other.IPAddress, role.IPAddress) {
			return false
		}
	}

	return true
}

// sameRoleIPAddress treats identical strings (including two empty/unset
// addresses) as equal, and otherwise falls back to parsed-IP equality so two
// differently-formatted representations of the same address still match. It
// never treats one populated and one empty/unparsable address as a match.
func sameRoleIPAddress(a, b string) bool {
	if a == b {
		return true
	}

	parsedA, parsedB := net.ParseIP(a), net.ParseIP(b)

	return parsedA != nil && parsedB != nil && parsedA.Equal(parsedB)
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
//
// SameGroup stays a distinct, IP-agnostic implementation from
// SameGroupRoles: its callers (event/status projection) need
// order-independence without caring about IP, and adding an IP check here
// would change that contract.
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

package soundtouchweb

import (
	"strings"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

// deviceView is the player-facing representation of one control target.
// A stereo pair is projected as one target keyed by its master speaker's host;
// the underlying registry continues to track both physical speakers.
type deviceView struct {
	Info       *models.DeviceInfo     `json:"info"`
	Status     *webtypes.DeviceStatus `json:"status"`
	LastSeen   time.Time              `json:"lastSeen"`
	StereoPair *stereoPairView        `json:"stereoPair,omitempty"`
}

// deviceProjectionEntry captures one immutable status pointer per physical
// device. Projection must not re-read live connection state midway through
// building a response, otherwise group membership and the emitted status can
// describe different moments.
type deviceProjectionEntry struct {
	ID       string
	Info     *models.DeviceInfo
	Status   *webtypes.DeviceStatus
	LastSeen time.Time
}

// stereoPairView describes the physical members represented by a logical
// player target. Controls are always sent to MasterDeviceID via the map key.
type stereoPairView struct {
	ID                   string                 `json:"id"`
	Name                 string                 `json:"name,omitempty"`
	MasterDeviceID       string                 `json:"masterDeviceId"`
	Status               string                 `json:"status,omitempty"`
	MemberCount          int                    `json:"memberCount"`
	AvailableMemberCount int                    `json:"availableMemberCount"`
	Degraded             bool                   `json:"degraded"`
	Members              []stereoPairMemberView `json:"members"`
}

// stereoPairMemberView is the player-facing role and availability of one
// physical speaker in a stereo pair.
type stereoPairMemberView struct {
	DeviceID  string `json:"deviceId"`
	Role      string `json:"role"`
	IPAddress string `json:"ipAddress,omitempty"`
	Name      string `json:"name,omitempty"`
	Available bool   `json:"available"`
}

func stereoPairCapable(info *models.DeviceInfo) bool {
	if info == nil {
		return false
	}

	typeName := strings.ToLower(strings.TrimSpace(info.Type))

	return typeName == "st10" || typeName == "soundtouch 10"
}

// deviceViewSnapshot projects the physical registry into logical control
// targets for the HTTP API and the global player WebSocket.
func (app *WebApp) deviceViewSnapshot() map[string]deviceView {
	return projectDeviceEntries(app.DeviceSnapshot())
}

func projectDeviceEntries(snapshot []DeviceEntry) map[string]deviceView {
	return projectCapturedDeviceEntries(captureDeviceProjectionEntries(snapshot))
}

func captureDeviceProjectionEntries(snapshot []DeviceEntry) []deviceProjectionEntry {
	captured := make([]deviceProjectionEntry, 0, len(snapshot))
	for _, entry := range snapshot {
		if entry.Device == nil {
			continue
		}

		captured = append(captured, deviceProjectionEntry{
			ID:       entry.ID,
			Info:     entry.Device.DeviceInfo,
			Status:   entry.Device.Status(),
			LastSeen: entry.LastSeen,
		})
	}

	return captured
}

func projectCapturedDeviceEntries(snapshot []deviceProjectionEntry) map[string]deviceView {
	byDeviceID := make(map[string][]deviceProjectionEntry, len(snapshot))
	for _, entry := range snapshot {
		if entry.Info == nil {
			continue
		}

		deviceID := strings.TrimSpace(entry.Info.DeviceID)
		if deviceID != "" {
			byDeviceID[deviceID] = append(byDeviceID[deviceID], entry)
		}
	}

	masters := make(map[string]*stereoPairView)
	hidden := make(map[string]bool)

	for _, entry := range snapshot {
		if entry.Info == nil {
			continue
		}

		if entry.Status == nil || !validMasterGroup(entry.Info.DeviceID, entry.Status.Group) {
			continue
		}

		master, unique := uniqueDeviceEntry(byDeviceID, entry.Status.Group.MasterDeviceID)
		if !unique || master.ID != entry.ID || !registeredMembersAgree(entry.Status.Group, byDeviceID) {
			continue
		}

		pair := newStereoPairView(entry.Status.Group, byDeviceID)
		masters[entry.ID] = pair

		for _, role := range entry.Status.Group.Roles.Roles {
			member, ok := uniqueDeviceEntry(byDeviceID, role.DeviceID)
			if ok && member.ID != entry.ID {
				hidden[member.ID] = true
			}
		}
	}

	devices := make(map[string]deviceView, len(snapshot))
	for _, entry := range snapshot {
		if hidden[entry.ID] {
			continue
		}

		pair := masters[entry.ID]
		devices[entry.ID] = deviceView{
			Info:       projectedDeviceInfo(entry.Info, pair),
			Status:     entry.Status,
			LastSeen:   entry.LastSeen,
			StereoPair: pair,
		}
	}

	return devices
}

func validMasterGroup(deviceID string, group *models.Group) bool {
	if group == nil || group.IsEmpty() || strings.TrimSpace(group.ID) == "" ||
		strings.TrimSpace(group.MasterDeviceID) == "" || len(group.Roles.Roles) != 2 ||
		strings.TrimSpace(deviceID) != strings.TrimSpace(group.MasterDeviceID) {
		return false
	}

	seenDevices := make(map[string]bool, len(group.Roles.Roles))
	seenRoles := make(map[string]bool, len(group.Roles.Roles))
	masterPresent := false

	for _, role := range group.Roles.Roles {
		memberID := strings.TrimSpace(role.DeviceID)

		memberRole := strings.ToUpper(strings.TrimSpace(role.Role))
		if memberID == "" || seenDevices[memberID] || (memberRole != "LEFT" && memberRole != "RIGHT") || seenRoles[memberRole] {
			return false
		}

		seenDevices[memberID] = true
		seenRoles[memberRole] = true
		masterPresent = masterPresent || memberID == strings.TrimSpace(group.MasterDeviceID)
	}

	return masterPresent && seenRoles["LEFT"] && seenRoles["RIGHT"]
}

func uniqueDeviceEntry(byDeviceID map[string][]deviceProjectionEntry, deviceID string) (deviceProjectionEntry, bool) {
	entries := byDeviceID[strings.TrimSpace(deviceID)]
	if len(entries) != 1 {
		return deviceProjectionEntry{}, false
	}

	return entries[0], true
}

func registeredMembersAgree(group *models.Group, byDeviceID map[string][]deviceProjectionEntry) bool {
	for _, role := range group.Roles.Roles {
		entries := byDeviceID[strings.TrimSpace(role.DeviceID)]
		if len(entries) > 1 {
			return false
		}

		if len(entries) == 0 {
			continue
		}

		if entries[0].Status == nil || !sameGroupClaim(group, entries[0].Status.Group) {
			return false
		}
	}

	return true
}

func sameGroupClaim(left, right *models.Group) bool {
	if left == nil || right == nil || left.ID != right.ID || left.MasterDeviceID != right.MasterDeviceID ||
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

func newStereoPairView(group *models.Group, byDeviceID map[string][]deviceProjectionEntry) *stereoPairView {
	members := make([]stereoPairMemberView, 0, len(group.Roles.Roles))
	available := 0

	for _, role := range group.Roles.Roles {
		member := stereoPairMemberView{
			DeviceID:  role.DeviceID,
			Role:      role.Role,
			IPAddress: role.IPAddress,
		}

		if entry, ok := uniqueDeviceEntry(byDeviceID, role.DeviceID); ok {
			if entry.Info != nil {
				member.Name = entry.Info.Name
				if entry.Info.IPAddress != "" {
					member.IPAddress = entry.Info.IPAddress
				}
			}

			member.Available = entry.Status != nil && entry.Status.IsConnected
			if member.Available {
				available++
			}
		}

		members = append(members, member)
	}

	return &stereoPairView{
		ID:                   group.ID,
		Name:                 logicalPairName(group.Name, members),
		MasterDeviceID:       group.MasterDeviceID,
		Status:               group.Status,
		MemberCount:          len(members),
		AvailableMemberCount: available,
		Degraded:             available != len(members) || (group.Status != "" && group.Status != "GROUP_OK"),
		Members:              members,
	}
}

func projectedDeviceInfo(info *models.DeviceInfo, pair *stereoPairView) *models.DeviceInfo {
	if info == nil || pair == nil || pair.Name == "" || pair.Name == info.Name {
		return info
	}

	projected := *info
	projected.Name = pair.Name

	return &projected
}

func logicalPairName(groupName string, members []stereoPairMemberView) string {
	commonName := ""

	for _, member := range members {
		name := strings.TrimSpace(member.Name)
		if name == "" {
			return groupName
		}

		if commonName == "" {
			commonName = name
			continue
		}

		if !strings.EqualFold(commonName, name) {
			return groupName
		}
	}

	if commonName != "" {
		return commonName
	}

	return groupName
}

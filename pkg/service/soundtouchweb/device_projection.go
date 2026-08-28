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
	Zone       *zoneView              `json:"zone,omitempty"`
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

// zoneView describes one master-authoritative multiroom zone after physical
// stereo members have been folded into logical player targets.
type zoneView struct {
	MasterDeviceID       string           `json:"masterDeviceId"`
	MasterControlID      string           `json:"masterControlId"`
	MemberCount          int              `json:"memberCount"`
	PhysicalMemberCount  int              `json:"physicalMemberCount"`
	AvailableMemberCount int              `json:"availableMemberCount"`
	Degraded             bool             `json:"degraded"`
	Volume               *int             `json:"volume,omitempty"`
	Members              []zoneMemberView `json:"members"`
}

type zoneMemberView struct {
	Kind            string                   `json:"kind"`
	ControlID       string                   `json:"controlId,omitempty"`
	IP              string                   `json:"ip,omitempty"`
	HardwareID      string                   `json:"hwId,omitempty"`
	Name            string                   `json:"name,omitempty"`
	Model           string                   `json:"model"`
	Type            string                   `json:"type"`
	DeviceIDs       []string                 `json:"deviceIds"`
	Available       bool                     `json:"available"`
	Connectivity    webtypes.Connectivity    `json:"connectivity"`
	ActualVolume    *int                     `json:"actualVolume,omitempty"`
	PhysicalMembers []zonePhysicalMemberView `json:"physicalMembers"`
	StereoPair      *stereoPairView          `json:"stereoPair,omitempty"`
	Balance         *models.Balance          `json:"balance,omitempty"`
}

type zonePhysicalMemberView struct {
	DeviceID     string                `json:"deviceId"`
	Role         string                `json:"role,omitempty"`
	IP           string                `json:"ip"`
	Name         string                `json:"name"`
	Type         string                `json:"type"`
	Available    bool                  `json:"available"`
	Connectivity webtypes.Connectivity `json:"connectivity"`
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
			Info:     entry.Device.Info(),
			Status:   entry.Device.Status(),
			LastSeen: entry.LastSeen,
		})
	}

	return captured
}

func projectCapturedDeviceEntries(snapshot []deviceProjectionEntry) map[string]deviceView {
	devices, physicalToLogical, byDeviceID := projectLogicalDeviceEntries(snapshot)

	return projectZoneViews(snapshot, devices, physicalToLogical, byDeviceID)
}

// projectLogicalDeviceEntries folds physical stereo members into their shared
// control target. Zone projection and the zone-detail endpoint both build on
// this representation so names and member status cannot diverge.
func projectLogicalDeviceEntries(snapshot []deviceProjectionEntry) (
	map[string]deviceView,
	map[string]string,
	map[string][]deviceProjectionEntry,
) {
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

	physicalToLogical := make(map[string]string, len(byDeviceID))
	for deviceID, entries := range byDeviceID {
		if len(entries) == 1 {
			physicalToLogical[deviceID] = entries[0].ID
		}
	}

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
			physicalToLogical[strings.TrimSpace(role.DeviceID)] = entry.ID

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

	return devices, physicalToLogical, byDeviceID
}

func projectZoneInfo(zone *models.ZoneInfo, snapshot []deviceProjectionEntry) (*zoneView, bool) {
	devices, physicalToLogical, byDeviceID := projectLogicalDeviceEntries(snapshot)
	candidate, ok := newZoneProjectionCandidate(zone, devices, physicalToLogical, byDeviceID)

	return candidate.view, ok
}

type zoneProjectionCandidate struct {
	masterControlID string
	view            *zoneView
	logicalMembers  []string
}

func projectZoneViews(
	snapshot []deviceProjectionEntry,
	devices map[string]deviceView,
	physicalToLogical map[string]string,
	byDeviceID map[string][]deviceProjectionEntry,
) map[string]deviceView {
	candidates := make([]zoneProjectionCandidate, 0)
	claims := make(map[string]int)

	for _, entry := range snapshot {
		if entry.Info == nil || entry.Status == nil ||
			!validMasterZone(entry.Info.DeviceID, entry.Status.Zone) {
			continue
		}

		candidate, ok := newZoneProjectionCandidate(
			entry.Status.Zone,
			devices,
			physicalToLogical,
			byDeviceID,
		)
		if !ok {
			continue
		}

		candidates = append(candidates, candidate)
		for _, logicalID := range candidate.logicalMembers {
			claims[logicalID]++
		}
	}

	for _, candidate := range candidates {
		conflict := false

		for _, logicalID := range candidate.logicalMembers {
			if claims[logicalID] != 1 {
				conflict = true
				break
			}
		}

		if conflict {
			continue
		}

		master := devices[candidate.masterControlID]
		master.Zone = candidate.view
		devices[candidate.masterControlID] = master

		for _, logicalID := range candidate.logicalMembers {
			if logicalID != candidate.masterControlID {
				delete(devices, logicalID)
			}
		}
	}

	return devices
}

func validMasterZone(deviceID string, zone *models.ZoneInfo) bool {
	return zone != nil && !zone.IsStandalone() &&
		strings.TrimSpace(zone.Master) != "" &&
		strings.TrimSpace(zone.Master) == strings.TrimSpace(deviceID)
}

func newZoneProjectionCandidate(
	zone *models.ZoneInfo,
	devices map[string]deviceView,
	physicalToLogical map[string]string,
	byDeviceID map[string][]deviceProjectionEntry,
) (zoneProjectionCandidate, bool) {
	masterControlID := physicalToLogical[strings.TrimSpace(zone.Master)]
	if _, ok := devices[masterControlID]; !ok {
		return zoneProjectionCandidate{}, false
	}

	members := make([]zoneMemberView, 0, zone.GetTotalDeviceCount())
	memberByLogicalID := make(map[string]int)
	logicalMembers := make([]string, 0, zone.GetTotalDeviceCount())
	availableCount := 0
	physicalMemberCount := 0
	degraded := false
	groupVolume := 0
	groupVolumeKnown := false

	zoneMemberIPs := make(map[string]string, len(zone.Members))
	for _, zoneMember := range zone.Members {
		deviceID := strings.TrimSpace(zoneMember.DeviceID)
		if deviceID != "" {
			zoneMemberIPs[deviceID] = strings.TrimSpace(zoneMember.IP)
		}
	}

	for _, deviceID := range zone.GetAllDeviceIDs() {
		deviceID = strings.TrimSpace(deviceID)
		logicalID := physicalToLogical[deviceID]

		controlID := logicalID
		if controlID == "" {
			controlID = zoneMemberIPs[deviceID]
		}

		if _, exists := memberByLogicalID[logicalID]; logicalID != "" && exists {
			continue
		}

		member := zoneMemberView{
			Kind:         "speaker",
			ControlID:    controlID,
			IP:           controlID,
			HardwareID:   deviceID,
			DeviceIDs:    []string{deviceID},
			Connectivity: webtypes.ConnectivityOffline,
			PhysicalMembers: []zonePhysicalMemberView{{
				DeviceID:     deviceID,
				IP:           zoneMemberIPs[deviceID],
				Connectivity: webtypes.ConnectivityOffline,
			}},
		}

		if view, ok := devices[logicalID]; ok {
			if view.Info != nil {
				member.Name = view.Info.Name
				member.Model = view.Info.Type
				member.Type = view.Info.Type
			}

			member.Connectivity = projectedConnectivity(view.Status)
			member.Available = member.Connectivity != webtypes.ConnectivityOffline

			if view.StereoPair != nil {
				member.Kind = "stereoPair"
				member.HardwareID = view.StereoPair.MasterDeviceID
				member.StereoPair = view.StereoPair
				if view.Status != nil {
					member.Balance = view.Status.Balance
				}

				member.DeviceIDs = member.DeviceIDs[:0]
				for _, pairMember := range view.StereoPair.Members {
					member.DeviceIDs = append(member.DeviceIDs, pairMember.DeviceID)
				}
				member.PhysicalMembers = physicalZoneMembers(view.StereoPair, byDeviceID)

				if view.StereoPair.Degraded {
					degraded = true
				}
			} else {
				member.PhysicalMembers = physicalZoneMembers(nil, map[string][]deviceProjectionEntry{
					deviceID: byDeviceID[deviceID],
				})
			}

			if view.Status != nil && view.Status.Volume != nil {
				volume := view.Status.Volume.ActualVolume
				member.ActualVolume = &volume
			}
		}

		if member.ActualVolume == nil && member.Kind != "stereoPair" {
			member.ActualVolume = maximumPhysicalVolume(byDeviceID, member.DeviceIDs)
		}
		if member.ActualVolume != nil {
			groupVolumeKnown = true

			if *member.ActualVolume > groupVolume {
				groupVolume = *member.ActualVolume
			}
		}

		if member.Available {
			availableCount++
		} else {
			degraded = true
		}

		members = append(members, member)
		physicalMemberCount += len(member.PhysicalMembers)
		if logicalID != "" {
			memberByLogicalID[logicalID] = len(members) - 1
			logicalMembers = append(logicalMembers, logicalID)
		}
	}

	if len(members) < 2 {
		return zoneProjectionCandidate{}, false
	}

	var projectedVolume *int
	if groupVolumeKnown {
		projectedVolume = &groupVolume
	}

	return zoneProjectionCandidate{
		masterControlID: masterControlID,
		logicalMembers:  logicalMembers,
		view: &zoneView{
			MasterDeviceID:       zone.Master,
			MasterControlID:      masterControlID,
			MemberCount:          len(members),
			PhysicalMemberCount:  physicalMemberCount,
			AvailableMemberCount: availableCount,
			Degraded:             degraded,
			Volume:               projectedVolume,
			Members:              members,
		},
	}, true
}

func physicalZoneMembers(
	pair *stereoPairView,
	byDeviceID map[string][]deviceProjectionEntry,
) []zonePhysicalMemberView {
	if pair == nil {
		for deviceID := range byDeviceID {
			return []zonePhysicalMemberView{newZonePhysicalMember(deviceID, "", "", byDeviceID)}
		}

		return []zonePhysicalMemberView{}
	}

	members := make([]zonePhysicalMemberView, 0, len(pair.Members))
	for _, pairMember := range pair.Members {
		members = append(members, newZonePhysicalMember(
			pairMember.DeviceID,
			pairMember.Role,
			pairMember.IPAddress,
			byDeviceID,
		))
	}

	return members
}

func newZonePhysicalMember(
	deviceID string,
	role string,
	fallbackIP string,
	byDeviceID map[string][]deviceProjectionEntry,
) zonePhysicalMemberView {
	member := zonePhysicalMemberView{
		DeviceID:     deviceID,
		Role:         role,
		IP:           fallbackIP,
		Connectivity: webtypes.ConnectivityOffline,
	}

	entry, ok := uniqueDeviceEntry(byDeviceID, deviceID)
	if !ok {
		return member
	}

	member.IP = entry.ID
	if entry.Info != nil {
		member.Name = entry.Info.Name
		member.Type = entry.Info.Type
	}
	member.Connectivity = projectedConnectivity(entry.Status)
	member.Available = member.Connectivity != webtypes.ConnectivityOffline

	return member
}

func maximumPhysicalVolume(byDeviceID map[string][]deviceProjectionEntry, deviceIDs []string) *int {
	var actualVolume *int

	for _, physicalID := range deviceIDs {
		volume, ok := physicalVolume(byDeviceID, physicalID)
		if !ok {
			continue
		}

		if actualVolume == nil || volume > *actualVolume {
			value := volume
			actualVolume = &value
		}
	}

	return actualVolume
}

func projectedConnectivity(status *webtypes.DeviceStatus) webtypes.Connectivity {
	if status == nil {
		return webtypes.ConnectivityOffline
	}

	switch status.Connectivity {
	case webtypes.ConnectivityOnline, webtypes.ConnectivityStale, webtypes.ConnectivityOffline:
		return status.Connectivity
	default:
		if status.IsConnected {
			return webtypes.ConnectivityOnline
		}

		return webtypes.ConnectivityOffline
	}
}

func physicalVolume(byDeviceID map[string][]deviceProjectionEntry, deviceID string) (int, bool) {
	entry, ok := uniqueDeviceEntry(byDeviceID, deviceID)
	if !ok || entry.Status == nil || entry.Status.Volume == nil {
		return 0, false
	}

	return entry.Status.Volume.ActualVolume, true
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

package setup

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/marge"
)

// MigrationDataNotReadyError means migration was refused because the service
// cannot prove that its rendered account data preserves the speaker's presets.
type MigrationDataNotReadyError struct {
	Reason string
	Action string
}

func (e *MigrationDataNotReadyError) Error() string {
	action := e.Action
	if action == "" {
		action = "Run Data Sync for this device and retry migration."
	}

	return fmt.Sprintf("Migration data is not ready: %s. %s", e.Reason, action)
}

func migrationDataNotReadyf(format string, args ...any) error {
	return &MigrationDataNotReadyError{Reason: fmt.Sprintf(format, args...)}
}

func migrationDataNotReadyWithAction(reason, action string) error {
	return &MigrationDataNotReadyError{Reason: reason, Action: action}
}

// checkMigrationDataReady proves that redirecting the speaker to this service
// will not replace its live presets with missing, stale, or filtered account
// data. Every operation in this check is read-only.
func (m *Manager) checkMigrationDataReady(deviceIP string) error {
	if m.DataStore == nil {
		// CLI callers do not own the service datastore and cannot enforce this
		// check. ExecuteInitPlan is a separate onboarding flow which establishes
		// account state only after its intentional URL rewrite.
		return nil
	}

	info, err := m.GetLiveDeviceInfo(deviceIP)
	if err != nil {
		return migrationDataNotReadyf("cannot read live /info: %v", err)
	}

	deviceID := strings.TrimSpace(info.DeviceID)
	accountID := strings.TrimSpace(info.MargeAccountUUID)

	if deviceID == "" {
		return migrationDataNotReadyf("live /info has no deviceID")
	}

	if accountID == "" {
		// An unpaired speaker, typically factory-reset, has no account data to
		// preserve, so there is nothing for this check to compare and nothing
		// to lose. Refusing here would also break the documented onboarding
		// order: the admin UI migrates first and pairs afterwards (see the
		// "Pairing runs after the URL flip" comment in the setup page), and
		// MIGRATION-GUIDE.md tells the user to Generate an account ID on a
		// factory-reset device. Data Sync cannot unblock it either, since it
		// files an account-less device under "default", which never matches an
		// empty live account.
		return nil
	}

	if !datastore.IsSafeIdentifier(accountID) || !datastore.IsSafeIdentifier(deviceID) {
		return migrationDataNotReadyf("live /info contains an invalid account or device identifier")
	}

	persistedInfo, err := m.DataStore.GetExactDeviceInfo(accountID, deviceID)
	if err != nil {
		return migrationDataNotReadyf("DeviceInfo.xml is not persisted under account %q and device %q", accountID, deviceID)
	}

	if persistedInfo.DeviceID != deviceID {
		return migrationDataNotReadyf("persisted DeviceInfo.xml identifies device %q instead of %q", persistedInfo.DeviceID, deviceID)
	}

	snapshot, err := m.DataStore.ReadPresetSnapshot(accountID, deviceID)
	if err != nil {
		return migrationDataNotReadyf("cannot read the persisted preset snapshot: %v", err)
	}

	if snapshot.State != datastore.PresetSnapshotValid {
		return migrationDataNotReadyf("persisted Presets.xml is %s", snapshot.State)
	}

	if snapshot.NeedsRewrite {
		return migrationDataNotReadyf("persisted Presets.xml uses a legacy format that must be refreshed")
	}

	livePresets, err := m.fetchLivePresets(deviceIP)
	if err != nil {
		return migrationDataNotReadyf("cannot read live /presets: %v", err)
	}

	fullXML, err := marge.AccountFullToXMLReadOnly(m.DataStore, accountID)
	if err != nil {
		return migrationDataNotReadyf("cannot render account /full: %v", err)
	}

	fullPresets, accountDeviceCount, err := migrationFullPresets(fullXML, deviceID)
	if err != nil {
		return migrationDataNotReadyf("rendered account /full is incomplete: %v", err)
	}

	if accountDeviceCount != 1 {
		return migrationDataNotReadyWithAction(
			fmt.Sprintf("rendered account /full contains %d devices; shared-account firmware resync can wipe presets after reboot", accountDeviceCount),
			"Move this speaker to a dedicated account, run Data Sync for it, then retry migration.",
		)
	}

	persisted := migrationPresetIdentities(snapshot.Presets)
	live := migrationPresetIdentities(livePresets)

	if mismatch := compareMigrationPresets("persisted snapshot", persisted, "rendered /full", fullPresets); mismatch != "" {
		return migrationDataNotReadyf("%s", mismatch)
	}

	if mismatch := compareMigrationPresets("live /presets", live, "rendered /full", fullPresets); mismatch != "" {
		return migrationDataNotReadyf("%s", mismatch)
	}

	return nil
}

type migrationPresetIdentity struct {
	Slot     string
	Name     string
	Location string
}

func migrationPresetIdentities(presets []models.ServicePreset) []migrationPresetIdentity {
	result := make([]migrationPresetIdentity, 0, len(presets))
	for i := range presets {
		slot := presets[i].ButtonNumber
		if slot == "" {
			slot = presets[i].ID
		}

		result = append(result, migrationPresetIdentity{
			Slot:     slot,
			Name:     presets[i].Name,
			Location: presets[i].Location,
		})
	}

	return result
}

func migrationFullPresets(fullXML []byte, deviceID string) ([]migrationPresetIdentity, int, error) {
	var full struct {
		Devices []struct {
			DeviceID string `xml:"deviceid,attr"`
			Presets  []struct {
				Slot     string `xml:"buttonNumber,attr"`
				Name     string `xml:"name"`
				Location string `xml:"location"`
			} `xml:"presets>preset"`
		} `xml:"devices>device"`
	}

	if err := xml.Unmarshal(fullXML, &full); err != nil {
		return nil, 0, fmt.Errorf("malformed XML: %w", err)
	}

	for i := range full.Devices {
		if full.Devices[i].DeviceID != deviceID {
			continue
		}

		presets := make([]migrationPresetIdentity, 0, len(full.Devices[i].Presets))
		for _, preset := range full.Devices[i].Presets {
			presets = append(presets, migrationPresetIdentity{
				Slot:     preset.Slot,
				Name:     preset.Name,
				Location: preset.Location,
			})
		}

		return presets, len(full.Devices), nil
	}

	return nil, len(full.Devices), fmt.Errorf("target device %q is missing", deviceID)
}

func compareMigrationPresets(leftName string, left []migrationPresetIdentity, rightName string, right []migrationPresetIdentity) string {
	if len(left) != len(right) {
		return fmt.Sprintf("%s has %d preset(s), but %s has %d", leftName, len(left), rightName, len(right))
	}

	leftBySlot, problem := indexMigrationPresets(leftName, left)
	if problem != "" {
		return problem
	}

	rightBySlot, problem := indexMigrationPresets(rightName, right)
	if problem != "" {
		return problem
	}

	for slot, leftPreset := range leftBySlot {
		rightPreset, ok := rightBySlot[slot]
		if !ok {
			return fmt.Sprintf("preset slot %s from %s is missing from %s", slot, leftName, rightName)
		}

		if leftPreset.Name != rightPreset.Name || leftPreset.Location != rightPreset.Location {
			return fmt.Sprintf("preset slot %s differs between %s and %s", slot, leftName, rightName)
		}
	}

	return ""
}

func indexMigrationPresets(name string, presets []migrationPresetIdentity) (map[string]migrationPresetIdentity, string) {
	bySlot := make(map[string]migrationPresetIdentity, len(presets))
	for _, preset := range presets {
		if preset.Slot == "" {
			return nil, fmt.Sprintf("%s contains a preset without a slot", name)
		}

		if _, exists := bySlot[preset.Slot]; exists {
			return nil, fmt.Sprintf("%s contains duplicate preset slot %s", name, preset.Slot)
		}

		bySlot[preset.Slot] = preset
	}

	return bySlot, ""
}

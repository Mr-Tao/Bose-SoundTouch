package datastore

import (
	"bytes"
	"encoding/xml"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func lifecycleTestGroup(master, left, right, name string) models.Group {
	return models.Group{
		Name:           name,
		MasterDeviceID: master,
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: left, Role: "LEFT"},
			{DeviceID: right, Role: "RIGHT"},
		}},
	}
}

func countLifecycleGroupFiles(t *testing.T, ds *DataStore, account string) int {
	t.Helper()

	entries, err := os.ReadDir(ds.AccountDevicesDir(account))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}

		t.Fatalf("read account devices directory: %v", err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "Group_") && strings.HasSuffix(entry.Name(), ".xml") {
			count++
		}
	}

	return count
}

func writeLifecycleGroup(t *testing.T, ds *DataStore, account, groupID string, group models.Group) {
	t.Helper()

	group.ID = groupID

	data, err := xml.MarshalIndent(&group, "", "    ")
	if err != nil {
		t.Fatalf("marshal group %s: %v", groupID, err)
	}

	if err := ds.rootMkdirAll(ds.AccountDevicesDir(account), 0755); err != nil {
		t.Fatalf("create account %s devices directory: %v", account, err)
	}

	if err := ds.atomicWriteFile(ds.groupFilePath(account, groupID), append([]byte(xml.Header), data...)); err != nil {
		t.Fatalf("write group %s: %v", groupID, err)
	}
}

func TestGroupGenerationReservationsAreGlobal(t *testing.T) {
	ds := NewDataStore(t.TempDir())

	writeLifecycleGroup(t, ds, "ACCOUNT1", "1234567",
		lifecycleTestGroup("MASTER1", "MASTER1", "SLAVE1", "Active pair"))

	if err := ds.rootMkdirAll(ds.AccountDevicesDir("ACCOUNT2"), 0755); err != nil {
		t.Fatalf("create tombstone account: %v", err)
	}
	if err := ds.atomicWriteFile(ds.retiredGroupFilePath("ACCOUNT2", "7654321"), []byte("retired\n")); err != nil {
		t.Fatalf("write cross-account tombstone: %v", err)
	}

	locations, err := ds.loadGroupGenerationLocationsNoLock()
	if err != nil {
		t.Fatalf("load generation reservations: %v", err)
	}

	if got := locations["1234567"]; len(got.active) != 1 || got.active[0].account != "ACCOUNT1" {
		t.Fatalf("active reservation = %#v, want ACCOUNT1", got)
	}
	if got := locations["7654321"]; len(got.retired) != 1 || got.retired[0].account != "ACCOUNT2" {
		t.Fatalf("retired reservation = %#v, want ACCOUNT2", got)
	}
}

func TestAddGroupReusesStoredStereoPair(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	const account = "ACCOUNT1"

	original := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
	original.SenderIPAddress = "192.0.2.10"

	firstID, err := ds.AddGroup(account, &original)
	if err != nil {
		t.Fatalf("add original group: %v", err)
	}

	retry := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Retry name")
	retry.Roles.Roles[0].IPAddress = "198.51.100.10"

	retryID, err := ds.AddGroup(account, &retry)
	if err != nil {
		t.Fatalf("retry group creation: %v", err)
	}

	if retryID != firstID {
		t.Fatalf("retry ID = %q, want stored ID %q", retryID, firstID)
	}

	if retry.ID != firstID || retry.Name != original.Name || retry.SenderIPAddress != original.SenderIPAddress {
		t.Fatalf("retry returned %#v, want unchanged stored group %#v", retry, original)
	}

	if got := countLifecycleGroupFiles(t, ds, account); got != 1 {
		t.Fatalf("stored group files = %d, want 1", got)
	}
}

func TestAddGroupRejectsExistingDeviceMembership(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	const account = "ACCOUNT1"

	original := lifecycleTestGroup("MASTER1", "MASTER1", "SHARED", "First pair")
	if _, err := ds.AddGroup(account, &original); err != nil {
		t.Fatalf("add original group: %v", err)
	}

	conflicting := lifecycleTestGroup("MASTER2", "MASTER2", "SHARED", "Conflicting pair")
	_, err := ds.AddGroup(account, &conflicting)
	if !errors.Is(err, ErrGroupMembershipConflict) {
		t.Fatalf("conflicting add error = %v, want ErrGroupMembershipConflict", err)
	}

	if got := countLifecycleGroupFiles(t, ds, account); got != 1 {
		t.Fatalf("stored group files = %d after conflict, want 1", got)
	}

	if group, getErr := ds.GetGroupForDevice(account, "SHARED"); getErr != nil || group.ID != original.ID {
		t.Fatalf("stored group changed after conflict: group=%#v err=%v", group, getErr)
	}
}

func TestAddGroupRejectsCrossAccountMembership(t *testing.T) {
	tests := []struct {
		name      string
		requested models.Group
	}{
		{
			name:      "same stereo pair",
			requested: lifecycleTestGroup("MASTER1", "MASTER1", "SLAVE1", "Same pair in another account"),
		},
		{
			name:      "shared member",
			requested: lifecycleTestGroup("MASTER2", "MASTER2", "SLAVE1", "Conflicting pair in another account"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds := NewDataStore(t.TempDir())
			stored := lifecycleTestGroup("MASTER1", "MASTER1", "SLAVE1", "Stored pair")
			writeLifecycleGroup(t, ds, "ACCOUNT1", "1234567", stored)

			_, err := ds.AddGroup("ACCOUNT2", &test.requested)
			if !errors.Is(err, ErrGroupMembershipConflict) {
				t.Fatalf("cross-account add error = %v, want ErrGroupMembershipConflict", err)
			}
			if got := countLifecycleGroupFiles(t, ds, "ACCOUNT1"); got != 1 {
				t.Fatalf("source account group files = %d after conflict, want 1", got)
			}
			if got := countLifecycleGroupFiles(t, ds, "ACCOUNT2"); got != 0 {
				t.Fatalf("requested account group files = %d after conflict, want 0", got)
			}
		})
	}
}

func TestAddGroupDoesNotReuseMalformedSuperset(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	const account = "ACCOUNT1"

	original := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Pair")
	if _, err := ds.AddGroup(account, &original); err != nil {
		t.Fatalf("add original group: %v", err)
	}

	malformed := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Pair")
	malformed.Roles.Roles = append(malformed.Roles.Roles, models.GroupRole{DeviceID: "EXTRA", Role: "CENTER"})
	if _, err := ds.AddGroup(account, &malformed); !errors.Is(err, ErrGroupMembershipConflict) {
		t.Fatalf("malformed superset error = %v, want ErrGroupMembershipConflict", err)
	}
}

func TestDeleteGroupGenerationForDevice(t *testing.T) {
	t.Run("removes only the exact generation containing the device", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		const (
			account  = "ACCOUNT1"
			deviceID = "SLAVE1"
		)

		first := lifecycleTestGroup("MASTER1", "MASTER1", "SLAVE1", "First pair")
		second := lifecycleTestGroup("MASTER2", "MASTER2", "SLAVE2", "Second pair")
		if _, err := ds.AddGroup(account, &first); err != nil {
			t.Fatalf("add first group: %v", err)
		}
		if _, err := ds.AddGroup(account, &second); err != nil {
			t.Fatalf("add second group: %v", err)
		}

		if err := ds.DeleteGroupGenerationForDevice(deviceID, first.ID, &first); err != nil {
			t.Fatalf("delete exact generation: %v", err)
		}

		if _, err := ds.GetGroupForDevice(account, deviceID); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("deleted device lookup error = %v, want ErrGroupNotFound", err)
		}
		if group, err := ds.GetGroupForDevice(account, "SLAVE2"); err != nil || group.ID != second.ID {
			t.Fatalf("unrelated group was not preserved: group=%#v err=%v", group, err)
		}
	})

	t.Run("stale generation is an idempotent no-op", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		const account = "ACCOUNT1"

		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Current pair")
		if _, err := ds.AddGroup(account, &group); err != nil {
			t.Fatalf("add group: %v", err)
		}

		if err := ds.DeleteGroupGenerationForDevice("MASTER", "OLDER-ID", nil); err != nil {
			t.Fatalf("delete stale generation: %v", err)
		}

		if current, err := ds.GetGroupForDevice(account, "MASTER"); err != nil || current.ID != group.ID {
			t.Fatalf("current generation changed: group=%#v err=%v", current, err)
		}
	})

	t.Run("missing generation is idempotent", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		if err := ds.DeleteGroupGenerationForDevice("MASTER", "PAIR-ID", nil); err != nil {
			t.Fatalf("delete missing generation: %v", err)
		}
	})

	t.Run("ambiguous duplicate generation fails closed", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		const deviceID = "MASTER"

		first := lifecycleTestGroup(deviceID, deviceID, "SLAVE", "First pair")
		if _, err := ds.AddGroup("ACCOUNT1", &first); err != nil {
			t.Fatalf("add first group: %v", err)
		}

		second := lifecycleTestGroup(deviceID, deviceID, "SLAVE", "Second pair")
		writeLifecycleGroup(t, ds, "ACCOUNT2", first.ID, second)

		err := ds.DeleteGroupGenerationForDevice(deviceID, first.ID, &first)
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("ambiguous delete error = %v, want ErrGroupDeleteAmbiguous", err)
		}
		if got := countLifecycleGroupFiles(t, ds, "ACCOUNT1") + countLifecycleGroupFiles(t, ds, "ACCOUNT2"); got != 2 {
			t.Fatalf("stored group files = %d after ambiguity, want 2", got)
		}
	})

	t.Run("same ID for another device fails closed", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Current pair")
		if _, err := ds.AddGroup("ACCOUNT1", &group); err != nil {
			t.Fatalf("add group: %v", err)
		}

		err := ds.DeleteGroupGenerationForDevice("OTHER", group.ID, &group)
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("wrong-device delete error = %v, want ErrGroupDeleteAmbiguous", err)
		}
		if !ds.rootExists(ds.groupFilePath("ACCOUNT1", group.ID)) {
			t.Fatal("wrong-device delete retired the active group")
		}
	})

	t.Run("submitted topology must match the stored generation", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		stored := lifecycleTestGroup("MASTER", "MASTER", "REAL-SLAVE", "Current pair")
		if _, err := ds.AddGroup("ACCOUNT1", &stored); err != nil {
			t.Fatalf("add group: %v", err)
		}

		submitted := stored
		submitted.Roles.Roles = append([]models.GroupRole(nil), stored.Roles.Roles...)
		submitted.Roles.Roles[1].DeviceID = "SUBSTITUTE-SLAVE"
		err := ds.DeleteGroupGenerationForDevice("MASTER", stored.ID, &submitted)
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("topology mismatch error = %v, want ErrGroupDeleteAmbiguous", err)
		}
		if !ds.rootExists(ds.groupFilePath("ACCOUNT1", stored.ID)) {
			t.Fatal("topology mismatch retired the active group")
		}
	})
}

func TestRenameGroupGenerationForDevice(t *testing.T) {
	t.Run("renames an exact generation across accounts", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())

		unrelated := lifecycleTestGroup("OTHER-MASTER", "OTHER-MASTER", "OTHER-SLAVE", "Unrelated pair")
		if _, err := ds.AddGroup("ACCOUNT1", &unrelated); err != nil {
			t.Fatalf("add unrelated group: %v", err)
		}

		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
		group.Roles.Roles[0].IPAddress = "192.0.2.10"
		group.Roles.Roles[1].IPAddress = "192.0.2.11"
		if _, err := ds.AddGroup("ACCOUNT2", &group); err != nil {
			t.Fatalf("add group: %v", err)
		}

		updated, err := ds.RenameGroupGenerationForDevice("MASTER", group.ID, &group, "Renamed pair")
		if err != nil {
			t.Fatalf("rename exact generation: %v", err)
		}
		if updated.ID != group.ID || updated.Name != "Renamed pair" {
			t.Fatalf("updated group = %#v, want ID %q and renamed name", updated, group.ID)
		}

		stored, err := ds.GetGroupForDevice("ACCOUNT2", "MASTER")
		if err != nil || !reflect.DeepEqual(stored, updated) {
			t.Fatalf("stored renamed group = %#v err=%v, want %#v", stored, err, updated)
		}
		if current, err := ds.GetGroupForDevice("ACCOUNT1", "OTHER-MASTER"); err != nil || current.Name != unrelated.Name {
			t.Fatalf("unrelated group changed: group=%#v err=%v", current, err)
		}
	})

	t.Run("retry allows the stored name to differ from expected", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
		if _, err := ds.AddGroup("ACCOUNT", &group); err != nil {
			t.Fatalf("add group: %v", err)
		}

		if _, err := ds.RenameGroupGenerationForDevice("MASTER", group.ID, &group, "Renamed pair"); err != nil {
			t.Fatalf("first rename: %v", err)
		}

		updated, err := ds.RenameGroupGenerationForDevice("MASTER", group.ID, &group, "Renamed pair")
		if err != nil {
			t.Fatalf("idempotent rename retry: %v", err)
		}
		if updated.Name != "Renamed pair" {
			t.Fatalf("retry returned name %q, want Renamed pair", updated.Name)
		}
	})

	t.Run("topology mismatch does not write", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		stored := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
		stored.Roles.Roles[0].IPAddress = "192.0.2.10"
		stored.Roles.Roles[1].IPAddress = "192.0.2.11"
		if _, err := ds.AddGroup("ACCOUNT", &stored); err != nil {
			t.Fatalf("add group: %v", err)
		}

		before, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT", stored.ID))
		if err != nil {
			t.Fatalf("read group before rename: %v", err)
		}

		expected := stored
		expected.Roles.Roles = append([]models.GroupRole(nil), stored.Roles.Roles...)
		expected.Roles.Roles[1].IPAddress = "198.51.100.11"
		_, err = ds.RenameGroupGenerationForDevice("MASTER", stored.ID, &expected, "Renamed pair")
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("topology mismatch error = %v, want ErrGroupDeleteAmbiguous", err)
		}

		after, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT", stored.ID))
		if err != nil {
			t.Fatalf("read group after rename: %v", err)
		}
		if !bytes.Equal(after, before) {
			t.Fatal("topology mismatch rewrote the active group")
		}
	})

	t.Run("unrelated device does not write", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
		if _, err := ds.AddGroup("ACCOUNT", &group); err != nil {
			t.Fatalf("add group: %v", err)
		}

		before, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT", group.ID))
		if err != nil {
			t.Fatalf("read group before rename: %v", err)
		}

		_, err = ds.RenameGroupGenerationForDevice("OTHER", group.ID, &group, "Renamed pair")
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("unrelated-device error = %v, want ErrGroupDeleteAmbiguous", err)
		}

		after, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT", group.ID))
		if err != nil {
			t.Fatalf("read group after rename: %v", err)
		}
		if !bytes.Equal(after, before) {
			t.Fatal("unrelated-device rename rewrote the active group")
		}
	})

	t.Run("ambiguous duplicate generation does not write", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
		writeLifecycleGroup(t, ds, "ACCOUNT1", "1234567", group)
		writeLifecycleGroup(t, ds, "ACCOUNT2", "1234567", group)
		group.ID = "1234567"

		_, err := ds.RenameGroupGenerationForDevice("MASTER", group.ID, &group, "Renamed pair")
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("ambiguous rename error = %v, want ErrGroupDeleteAmbiguous", err)
		}

		for _, account := range []string{"ACCOUNT1", "ACCOUNT2"} {
			stored, getErr := ds.GetGroupForDevice(account, "MASTER")
			if getErr != nil || stored.Name != "Original name" {
				t.Fatalf("group in %s changed after ambiguity: group=%#v err=%v", account, stored, getErr)
			}
		}
	})

	t.Run("empty name is rejected", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
		if _, err := ds.AddGroup("ACCOUNT", &group); err != nil {
			t.Fatalf("add group: %v", err)
		}

		_, err := ds.RenameGroupGenerationForDevice("MASTER", group.ID, &group, "")
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("empty-name error = %v, want ErrGroupDeleteAmbiguous", err)
		}
		if current, getErr := ds.GetGroupForDevice("ACCOUNT", "MASTER"); getErr != nil || current.Name != group.Name {
			t.Fatalf("group changed after empty name: group=%#v err=%v", current, getErr)
		}
	})

	t.Run("non-master device is rejected", func(t *testing.T) {
		ds := NewDataStore(t.TempDir())
		group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Original name")
		if _, err := ds.AddGroup("ACCOUNT", &group); err != nil {
			t.Fatalf("add group: %v", err)
		}

		_, err := ds.RenameGroupGenerationForDevice("SLAVE", group.ID, &group, "Renamed pair")
		if !errors.Is(err, ErrGroupDeleteAmbiguous) {
			t.Fatalf("non-master error = %v, want ErrGroupDeleteAmbiguous", err)
		}
		if current, getErr := ds.GetGroupForDevice("ACCOUNT", "MASTER"); getErr != nil || current.Name != group.Name {
			t.Fatalf("group changed after non-master rename: group=%#v err=%v", current, getErr)
		}
	})
}

func TestEnsureNoGroupsForDevicesReportsStaleGroupsAcrossAccountsWithoutMutation(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	const (
		firstID  = "1234567"
		secondID = "7654321"
	)

	first := lifecycleTestGroup("MOVED", "MOVED", "OLD-SLAVE-1", "First stale pair")
	writeLifecycleGroup(t, ds, "ACCOUNT1", firstID, first)

	second := lifecycleTestGroup("MOVED", "MOVED", "OLD-SLAVE-2", "Second stale pair")
	writeLifecycleGroup(t, ds, "ACCOUNT2", secondID, second)

	unrelated := lifecycleTestGroup("OTHER-MASTER", "OTHER-MASTER", "OTHER-SLAVE", "Unrelated pair")
	if _, err := ds.AddGroup("ACCOUNT3", &unrelated); err != nil {
		t.Fatalf("add unrelated group: %v", err)
	}

	if firstID == unrelated.ID || secondID == unrelated.ID {
		t.Fatalf("active generation IDs are not globally unique: %q %q %q", firstID, secondID, unrelated.ID)
	}

	firstBefore, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT1", firstID))
	if err != nil {
		t.Fatalf("read first group before check: %v", err)
	}
	secondBefore, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT2", secondID))
	if err != nil {
		t.Fatalf("read second group before check: %v", err)
	}

	err = ds.EnsureNoGroupsForDevices([]string{"MOVED"})
	if !errors.Is(err, ErrGroupMembershipConflict) {
		t.Fatalf("cross-account check error = %v, want ErrGroupMembershipConflict", err)
	}

	var conflict *GroupMembershipConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("cross-account check error type = %T, want *GroupMembershipConflictError", err)
	}
	wantGenerations := []GroupGeneration{
		{Account: "ACCOUNT1", ID: firstID},
		{Account: "ACCOUNT2", ID: secondID},
	}
	if !reflect.DeepEqual(conflict.Generations, wantGenerations) {
		t.Fatalf("conflicting generations = %#v, want %#v", conflict.Generations, wantGenerations)
	}

	firstAfter, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT1", firstID))
	if err != nil {
		t.Fatalf("read first group after check: %v", err)
	}
	secondAfter, err := ds.rootReadFile(ds.groupFilePath("ACCOUNT2", secondID))
	if err != nil {
		t.Fatalf("read second group after check: %v", err)
	}
	if !bytes.Equal(firstAfter, firstBefore) || !bytes.Equal(secondAfter, secondBefore) {
		t.Fatal("read-only group check changed active group data")
	}
	if ds.rootExists(ds.retiredGroupFilePath("ACCOUNT1", firstID)) ||
		ds.rootExists(ds.retiredGroupFilePath("ACCOUNT2", secondID)) {
		t.Fatal("read-only group check created a tombstone")
	}
	if got := countLifecycleGroupFiles(t, ds, "ACCOUNT3"); got != 1 {
		t.Fatalf("unrelated active group files = %d, want 1", got)
	}
}

func TestRetireGroupAtomicallyRenamesActiveXML(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	const (
		account = "ACCOUNT1"
		groupID = "1234567"
	)

	group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Pair")
	writeLifecycleGroup(t, ds, account, groupID, group)

	activePath := ds.groupFilePath(account, groupID)
	retiredPath := ds.retiredGroupFilePath(account, groupID)
	activeInfo, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat active group: %v", err)
	}
	activeXML, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active group: %v", err)
	}

	if err := ds.DeleteGroup(account, groupID); err != nil {
		t.Fatalf("retire group: %v", err)
	}

	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Fatalf("active path stat error = %v, want not exist", err)
	}
	retiredInfo, err := os.Stat(retiredPath)
	if err != nil {
		t.Fatalf("stat retired group: %v", err)
	}
	if !os.SameFile(activeInfo, retiredInfo) {
		t.Fatal("retired group is not the renamed active file")
	}
	retiredXML, err := os.ReadFile(retiredPath)
	if err != nil {
		t.Fatalf("read retired group: %v", err)
	}
	if !bytes.Equal(retiredXML, activeXML) {
		t.Fatal("retired group did not preserve the active XML contents")
	}
}

func TestEnsureNoGroupsForDevicesRejectsActiveTombstoneAmbiguity(t *testing.T) {
	ds := NewDataStore(t.TempDir())

	group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Ambiguous pair")
	if _, err := ds.AddGroup("ACCOUNT1", &group); err != nil {
		t.Fatalf("add active group: %v", err)
	}

	if err := ds.rootMkdirAll(ds.AccountDevicesDir("ACCOUNT2"), 0755); err != nil {
		t.Fatalf("create tombstone account: %v", err)
	}
	if err := ds.atomicWriteFile(ds.retiredGroupFilePath("ACCOUNT2", group.ID), []byte("retired\n")); err != nil {
		t.Fatalf("write conflicting tombstone: %v", err)
	}

	err := ds.EnsureNoGroupsForDevices([]string{"MASTER"})
	if !errors.Is(err, ErrGroupDeleteAmbiguous) {
		t.Fatalf("ambiguous check error = %v, want ErrGroupDeleteAmbiguous", err)
	}
	if !ds.rootExists(ds.groupFilePath("ACCOUNT1", group.ID)) {
		t.Fatal("ambiguous check removed the active group")
	}
	if _, readErr := ds.rootReadFile(ds.retiredGroupFilePath("ACCOUNT2", group.ID)); readErr != nil {
		t.Fatalf("ambiguous check changed the tombstone: %v", readErr)
	}
}

func TestGroupReadsFailClosedOnMalformedOrUnreadableData(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ds *DataStore) string
	}{
		{
			name: "malformed XML",
			setup: func(t *testing.T, ds *DataStore) string {
				t.Helper()

				path := ds.groupFilePath("ACCOUNT1", "1234567")
				if err := ds.rootMkdirAll(ds.AccountDevicesDir("ACCOUNT1"), 0755); err != nil {
					t.Fatalf("create account directory: %v", err)
				}
				if err := ds.atomicWriteFile(path, []byte("<group>")); err != nil {
					t.Fatalf("write malformed group: %v", err)
				}

				return path
			},
		},
		{
			name: "unreadable group",
			setup: func(t *testing.T, ds *DataStore) string {
				t.Helper()

				if err := ds.rootMkdirAll(ds.AccountDevicesDir("ACCOUNT1"), 0755); err != nil {
					t.Fatalf("create account directory: %v", err)
				}
				path := ds.groupFilePath("ACCOUNT1", "1234567")
				if err := os.Symlink("missing-group-target", path); err != nil {
					t.Fatalf("create unreadable group symlink: %v", err)
				}

				return path
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds := NewDataStore(t.TempDir())
			path := test.setup(t, ds)

			if err := ds.EnsureNoGroupsForDevices([]string{"MASTER"}); err == nil {
				t.Fatal("EnsureNoGroupsForDevices error = nil, want datastore error")
			}
			if _, err := ds.GetGroupForDevice("ACCOUNT1", "MASTER"); err == nil || errors.Is(err, ErrGroupNotFound) {
				t.Fatalf("GetGroupForDevice error = %v, want datastore error", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("fail-closed reads mutated group path: %v", err)
			}
		})
	}
}

func TestGetGroupForDeviceFailsClosedOnDuplicateMembership(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	first := lifecycleTestGroup("MASTER1", "MASTER1", "SHARED", "First pair")
	second := lifecycleTestGroup("MASTER2", "MASTER2", "SHARED", "Second pair")

	writeLifecycleGroup(t, ds, "ACCOUNT1", "1234567", first)
	writeLifecycleGroup(t, ds, "ACCOUNT1", "7654321", second)

	group, err := ds.GetGroupForDevice("ACCOUNT1", "SHARED")
	if group != nil || !errors.Is(err, ErrGroupMembershipConflict) {
		t.Fatalf("group=%#v error=%v, want membership conflict", group, err)
	}
}

func TestDeleteGroupClassifiesMissingAndAmbiguousGenerations(t *testing.T) {
	ds := NewDataStore(t.TempDir())

	if err := ds.DeleteGroup("ACCOUNT1", "1234567"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("missing delete error = %v, want ErrGroupNotFound", err)
	}

	group := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Ambiguous pair")
	writeLifecycleGroup(t, ds, "ACCOUNT1", "7654321", group)
	if err := ds.atomicWriteFile(ds.retiredGroupFilePath("ACCOUNT1", "7654321"), []byte("retired\n")); err != nil {
		t.Fatalf("write conflicting tombstone: %v", err)
	}

	err := ds.DeleteGroup("ACCOUNT1", "7654321")
	if !errors.Is(err, ErrGroupDeleteAmbiguous) {
		t.Fatalf("ambiguous delete error = %v, want ErrGroupDeleteAmbiguous", err)
	}
	if !ds.rootExists(ds.groupFilePath("ACCOUNT1", "7654321")) {
		t.Fatal("ambiguous delete removed the active group")
	}
}

func TestRetiredStereoPairGetsFreshGeneration(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	const account = "ACCOUNT1"

	first := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Pair")
	firstID, err := ds.AddGroup(account, &first)
	if err != nil {
		t.Fatalf("add first generation: %v", err)
	}
	if err := ds.DeleteGroupGenerationForDevice("MASTER", firstID, &first); err != nil {
		t.Fatalf("retire first generation: %v", err)
	}
	if !ds.rootExists(ds.retiredGroupFilePath(account, firstID)) {
		t.Fatalf("retired generation %q has no tombstone", firstID)
	}
	tombstone, err := ds.rootReadFile(ds.retiredGroupFilePath(account, firstID))
	if err != nil {
		t.Fatalf("read retired generation: %v", err)
	}
	var retired models.Group
	if err := xml.Unmarshal(tombstone, &retired); err != nil {
		t.Fatalf("retired generation does not contain group XML: %v", err)
	}
	if retired.ID != firstID {
		t.Fatalf("retired generation ID = %q, want %q", retired.ID, firstID)
	}
	if err := ds.DeleteGroup(account, firstID); err != nil {
		t.Fatalf("repeat exact generation delete should be idempotent: %v", err)
	}

	second := lifecycleTestGroup("MASTER", "MASTER", "SLAVE", "Pair")
	secondID, err := ds.AddGroup(account, &second)
	if err != nil {
		t.Fatalf("add second generation: %v", err)
	}
	if secondID == firstID {
		t.Fatalf("new physical generation reused retired ID %q", firstID)
	}
}

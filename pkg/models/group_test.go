package models

import "testing"

func TestSameGroupRoles(t *testing.T) {
	base := []GroupRole{
		{DeviceID: "LEFT-ID", Role: "LEFT", IPAddress: "192.0.2.10"},
		{DeviceID: "RIGHT-ID", Role: "RIGHT", IPAddress: "192.0.2.11"},
	}

	t.Run("identical roles match", func(t *testing.T) {
		if !SameGroupRoles(base, append([]GroupRole(nil), base...)) {
			t.Fatal("identical roles reported as different")
		}
	})

	t.Run("role order does not matter", func(t *testing.T) {
		reordered := []GroupRole{base[1], base[0]}
		if !SameGroupRoles(base, reordered) {
			t.Fatal("reordered roles reported as different")
		}
	})

	t.Run("differently formatted equal IP matches", func(t *testing.T) {
		other := append([]GroupRole(nil), base...)
		other[0].IPAddress = "::ffff:192.0.2.10" // IPv4-mapped IPv6 form of the same address
		if !SameGroupRoles(base, other) {
			t.Fatal("differently-formatted equal IP addresses reported as different")
		}
	})

	t.Run("both empty IP addresses match", func(t *testing.T) {
		noIP := []GroupRole{
			{DeviceID: "LEFT-ID", Role: "LEFT"},
			{DeviceID: "RIGHT-ID", Role: "RIGHT"},
		}
		if !SameGroupRoles(noIP, append([]GroupRole(nil), noIP...)) {
			t.Fatal("two roles with unset IP addresses reported as different")
		}
	})

	t.Run("different IP does not match", func(t *testing.T) {
		other := append([]GroupRole(nil), base...)
		other[0].IPAddress = "198.51.100.10"
		if SameGroupRoles(base, other) {
			t.Fatal("different IP addresses reported as same")
		}
	})

	t.Run("populated vs empty IP does not match", func(t *testing.T) {
		other := append([]GroupRole(nil), base...)
		other[0].IPAddress = ""
		if SameGroupRoles(base, other) {
			t.Fatal("populated vs empty IP address reported as same")
		}
	})

	t.Run("different DeviceID does not match", func(t *testing.T) {
		other := append([]GroupRole(nil), base...)
		other[0].DeviceID = "OTHER-ID"
		if SameGroupRoles(base, other) {
			t.Fatal("different DeviceID reported as same")
		}
	})

	t.Run("different Role does not match", func(t *testing.T) {
		other := append([]GroupRole(nil), base...)
		other[0].Role = "RIGHT"
		if SameGroupRoles(base, other) {
			t.Fatal("mismatched Role reported as same")
		}
	})

	t.Run("different length does not match", func(t *testing.T) {
		if SameGroupRoles(base, base[:1]) {
			t.Fatal("different-length role slices reported as same")
		}
	})

	t.Run("duplicate role on either side does not match", func(t *testing.T) {
		duplicateA := []GroupRole{base[0], base[0]}
		duplicateB := []GroupRole{base[1], base[1]}
		if SameGroupRoles(duplicateA, base) {
			t.Fatal("duplicate role in first argument reported as same")
		}
		if SameGroupRoles(base, duplicateB) {
			t.Fatal("duplicate role in second argument reported as same")
		}
	})
}

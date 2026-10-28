package access

import "testing"

func TestAllows(t *testing.T) {
	cases := []struct {
		role Role
		perm Permission
		want bool
	}{
		{RoleOwner, Read, true},
		{RoleOwner, Write, true},
		{RoleOwner, Manage, true},
		{RoleMember, Read, true},
		{RoleMember, Write, true},
		{RoleMember, Manage, false},
		{Role("guest"), Read, false},
		{Role(""), Write, false},
	}
	for _, c := range cases {
		if got := allows(c.role, c.perm); got != c.want {
			t.Errorf("allows(%q, %d) = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
}

func TestValidUUID(t *testing.T) {
	if !ValidUUID("11111111-1111-4111-8111-111111111111") {
		t.Fatal("valid uuid rejected")
	}
	for _, bad := range []string{"", "x", "11111111-1111-4111-8111-11111111111", "'; drop table boards;--"} {
		if ValidUUID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

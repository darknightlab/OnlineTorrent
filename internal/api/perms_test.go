package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

func TestRoutePermissions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{St: st}
	add := func(key, access string, def ...string) {
		s.perms.add(RouteSpec{Key: key, Access: access, DefaultRoles: def})
	}
	add("GET /pub", AccessPublic)
	add("GET /browse", AccessConfigurable, store.RoleGuest)
	add("POST /seed", AccessConfigurable, store.RoleMember)
	add("POST /vip", AccessConfigurable, store.RoleVIP)
	add("POST /report", AccessUser, store.RoleMember)
	add("GET /admin", AccessAdmin)
	ctx := context.Background()
	if err := s.loadPerms(ctx); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		role, key string
		want      bool
	}{
		{"guest", "GET /pub", true}, {"guest", "GET /browse", true}, {"guest", "POST /seed", false},
		{"member", "POST /seed", true}, {"vip", "POST /seed", true}, {"member", "POST /vip", false},
		{"vip", "POST /vip", true}, {"guest", "POST /report", false}, {"member", "POST /report", true},
		{"vip", "GET /admin", false}, {"admin", "GET /admin", true}, {"admin", "POST /vip", true},
		{"guest", "GET /unknown", false},
	}
	for _, c := range cases {
		if got := s.allowed(c.role, c.key); got != c.want {
			t.Errorf("%s %s: got %v want %v", c.role, c.key, got, c.want)
		}
	}
	// admin revokes guest browsing and grants seeding to guests; reload keeps the change
	if err := st.SetRouteRoles(ctx, "GET /browse", []string{store.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRouteRoles(ctx, "POST /seed", []string{store.RoleGuest}); err != nil {
		t.Fatal(err)
	}
	if err := s.loadPerms(ctx); err != nil {
		t.Fatal(err)
	}
	if s.allowed("guest", "GET /browse") || !s.allowed("member", "GET /browse") || !s.allowed("guest", "POST /seed") {
		t.Fatal("grants not applied")
	}
	// defaults are seeded only once: revoked grants are not re-added on restart
	s2 := &Server{St: st}
	s2.perms.add(RouteSpec{Key: "GET /browse", Access: AccessConfigurable, DefaultRoles: []string{store.RoleGuest}})
	if err := s2.loadPerms(ctx); err != nil {
		t.Fatal(err)
	}
	if s2.allowed("guest", "GET /browse") {
		t.Fatal("revoked default came back")
	}
	if got := s.permissionsFor("guest"); len(got) != 1 || got[0] != "POST /seed" {
		t.Fatalf("guest perms: %v", got)
	}
}

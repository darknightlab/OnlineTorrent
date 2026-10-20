package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestUpdateInvite(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	res, err := st.W.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,created_at) VALUES('adm','x','admin',?)`, Now())
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	one := 1
	inv, err := st.CreateInvite(ctx, uid, NewInvite{MaxUses: &one, ExpiresInDays: 7, Note: "a"})
	if err != nil {
		t.Fatal(err)
	}
	five, vip, days, never, note, off := 5, RoleVIP, 30, "", "批量", true
	got, err := st.UpdateInvite(ctx, inv.ID, InvitePatch{MaxUses: &five, Role: &vip, VIPDays: &days, ExpiresAt: &never, Note: &note, Disabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxUses != 5 || got.Role != RoleVIP || got.VIPDays != 30 || got.ExpiresAt != nil || got.Note != "批量" || !got.Disabled || got.Code != inv.Code {
		t.Fatalf("patched: %+v", got)
	}
	date := "2030-01-02"
	if got, err = st.UpdateInvite(ctx, inv.ID, InvitePatch{ExpiresAt: &date}); err != nil || got.ExpiresAt == nil || *got.ExpiresAt != "2030-01-02T23:59:59Z" || got.MaxUses != 5 {
		t.Fatalf("date: %+v %v", got, err)
	}
	neg := -1
	if _, err := st.UpdateInvite(ctx, inv.ID, InvitePatch{MaxUses: &neg}); err == nil {
		t.Fatal("negative max_uses accepted")
	}
	if _, err := st.UpdateInvite(ctx, 9999, InvitePatch{Note: &note}); err != ErrNotFound {
		t.Fatalf("missing invite: %v", err)
	}
}

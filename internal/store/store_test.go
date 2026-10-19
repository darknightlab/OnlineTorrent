package store

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestTrunc(t *testing.T) {
	s := strings.Repeat("字", 200) // 600 bytes
	got := Trunc(s, 301)
	if len(got) > 301 || !utf8.ValidString(got) || len(got) != 300 {
		t.Errorf("len=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if Trunc("abc", 10) != "abc" {
		t.Error("short")
	}
}

func TestTruncOnWrite(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ih := strings.Repeat("a", 40)
	long := strings.Repeat("x", 100000)
	st.AddPlay(ctx, Play{Infohash: ih, Title: long}, nil)
	if err := st.AddReview(ctx, ih, long, "report", long, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTorrent(ctx, TorrentRecord{Infohash: ih, UserTitle: long, Source: "user"}); err != nil {
		t.Fatal(err)
	}
	var a, b, c, d int
	if err := st.R.QueryRow(`SELECT (SELECT MAX(length(title)) FROM plays), (SELECT MAX(length(title)) FROM reviews), (SELECT MAX(length(reason)) FROM reviews), (SELECT length(user_title) FROM torrents)`).Scan(&a, &b, &c, &d); err != nil {
		t.Fatal(err)
	}
	if a > MaxTitleBytes || b > MaxTitleBytes || c > MaxReasonBytes || d > MaxTitleBytes {
		t.Errorf("not truncated: %d %d %d %d", a, b, c, d)
	}
	rec, _ := st.GetTorrent(ctx, ih)
	if rec.Title != "" || rec.UserTitle == "" {
		t.Errorf("user title must stay apart: %+v", rec)
	}
}

func TestRetention(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	old := time.Now().AddDate(0, 0, -100).UTC().Format(time.RFC3339)
	older := time.Now().AddDate(0, 0, -200).UTC().Format(time.RFC3339)
	mustExec := func(q string, args ...any) {
		if _, err := st.W.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO plays(infohash,action,created_at) VALUES('a','allow',?),('b','allow',?)`, old, Now())
	mustExec(`INSERT INTO audit(action,created_at) VALUES('x',?),('y',?)`, old, Now())
	mustExec(`INSERT INTO reviews(infohash,source,status,created_at,resolved_at) VALUES('a','report','resolved',?,?),('b','report','dismissed',?,?),('c','report','pending',?,NULL)`, older, older, old, old, older)
	n, err := st.Retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("deleted %d, want 3", n)
	}
}

func TestRedeemPermanentVIP(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	u, err := st.CreateUser(ctx, CreateUserParams{Username: "vipper", Password: "password1", Role: RoleVIP})
	if err != nil {
		t.Fatal(err)
	}
	codes, err := st.CreateRedeemCodes(ctx, u.ID, 1, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Redeem(ctx, u.ID, " "+strings.ToLower(codes[0].Code)+" "); !errors.Is(err, ErrPermanentVIP) {
		t.Fatalf("want ErrPermanentVIP, got %v", err)
	}
	// code is still unused and works for a member (case/space-insensitive)
	m, _ := st.CreateUser(ctx, CreateUserParams{Username: "member1", Password: "password1"})
	got, err := st.Redeem(ctx, m.ID, strings.ToLower(codes[0].Code))
	if err != nil || got.VIPUntil == nil {
		t.Fatalf("redeem: %v %+v", err, got)
	}
	if _, err := st.Redeem(ctx, m.ID, codes[0].Code); !errors.Is(err, ErrCodeInvalid) {
		t.Errorf("reuse: %v", err)
	}
	// admin sets role vip without vip_until -> permanent
	vip := RoleVIP
	got, err = st.AdminUpdateUser(ctx, m.ID, UpdateUser{Role: &vip})
	if err != nil || got.VIPUntil != nil {
		t.Errorf("role vip must clear vip_until: %v %+v", err, got)
	}
}

func TestInvites(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	admin, _ := st.CreateUser(ctx, CreateUserParams{Username: "admin", Password: "password1", Role: RoleAdmin})
	inv, err := st.CreateInvite(ctx, admin.ID, NewInvite{}) // max_uses omitted -> 1
	if err != nil {
		t.Fatal(err)
	}
	if inv.MaxUses != 1 || inv.Code != strings.ToUpper(inv.Code) {
		t.Errorf("default invite: %+v", inv)
	}
	if _, err := st.CreateInvite(ctx, admin.ID, NewInvite{VIPDays: -1}); err == nil {
		t.Error("negative vip_days accepted")
	}
	if _, err := st.CreateUser(ctx, CreateUserParams{Username: "u1", Password: "password1", InviteCode: " " + strings.ToLower(inv.Code), RequireInvite: true}); err != nil {
		t.Fatalf("use invite: %v", err)
	}
	if _, err := st.CreateUser(ctx, CreateUserParams{Username: "u2", Password: "password1", InviteCode: inv.Code, RequireInvite: true}); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("second use: %v", err)
	}
}

func TestRehashOnLogin(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	u, err := st.CreateUser(ctx, CreateUserParams{Username: "old", Password: "password1"})
	if err != nil {
		t.Fatal(err)
	}
	salt := []byte("0123456789abcdef")
	dk, _ := pbkdf2.Key(sha256.New, "password1", salt, 1000, 32)
	legacy := fmt.Sprintf("pbkdf2$1000$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(dk))
	if _, err := st.W.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, legacy, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Authenticate(ctx, "old", "password1"); err != nil {
		t.Fatal(err)
	}
	var h string
	_ = st.R.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&h)
	if !strings.HasPrefix(h, fmt.Sprintf("pbkdf2$%d$", pbkdf2Iter)) {
		t.Errorf("not rehashed: %s", h)
	}
	if _, err := st.Authenticate(ctx, "old", "password1"); err != nil {
		t.Errorf("login after rehash: %v", err)
	}
}

func TestResolveReview(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ih := strings.Repeat("b", 40)
	_ = st.AddReview(ctx, ih, "t", "report", "r", nil)
	_ = st.AddReview(ctx, ih, "t", "auto", "r", nil)
	if _, err := st.ResolveReview(ctx, nil, 1, "bogus", "", ""); err == nil {
		t.Error("bad action accepted")
	}
	got, err := st.ResolveReview(ctx, nil, 1, "block", "porn", "note")
	if err != nil || got != ih {
		t.Fatal(err)
	}
	v, _ := st.GetVerdict(ctx, ih)
	var pending int
	_ = st.R.QueryRow(`SELECT COUNT(*) FROM reviews WHERE status='pending'`).Scan(&pending)
	if v == nil || v.Action != "block" || pending != 0 {
		t.Errorf("verdict=%+v pending=%d", v, pending)
	}
}

func TestFirstPlay(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ih := "8d044029c825a9b881e3f6753f7093f8d256a85d"
	if _, _, ok := st.FirstPlay(ctx, ih); ok {
		t.Fatal("no plays yet")
	}
	u, err := st.CreateUser(ctx, CreateUserParams{Username: "alice", Email: "a@example.com", Password: "password-123", Role: RoleMember})
	if err != nil {
		t.Fatal(err)
	}
	st.AddPlay(ctx, Play{Infohash: ih, Action: "login"}, nil) // denied: not a submission
	st.AddPlay(ctx, Play{Infohash: ih, Action: "allow"}, &u.ID)
	st.AddPlay(ctx, Play{Infohash: ih, Action: "allow"}, nil)
	at, by, ok := st.FirstPlay(ctx, ih)
	if !ok || at == "" || by == nil || *by != u.ID {
		t.Errorf("first play %s %v %v", at, by, ok)
	}
}

// TestUserMagnetSettingRemoved: policy.user_magnet_action is gone from the schema and its
// stored row is deleted by migration 7.
func TestUserMagnetSettingRemoved(t *testing.T) {
	st := newStore(t)
	for _, sp := range SettingsSchema() {
		if sp.Key == "policy.user_magnet_action" {
			t.Fatal("still in the schema")
		}
	}
	var n int
	_ = st.R.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'policy.user_magnet_action'`).Scan(&n)
	if n != 0 {
		t.Errorf("stored row left: %d", n)
	}
	// an older database (migrations 1–6) with the row: migration 7 deletes it
	ctx := context.Background()
	st.W.Exec(`PRAGMA user_version = 6`)
	st.W.Exec(`ALTER TABLE metadata_jobs DROP COLUMN priority`)
	if _, err := st.W.Exec(`INSERT INTO settings(key, value) VALUES('policy.user_magnet_action', '"login"')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, st.W, migrations[:7]); err != nil { // up to migration 7 only (later ones already applied)
		t.Fatal(err)
	}
	_ = st.R.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'policy.user_magnet_action'`).Scan(&n)
	if n != 0 {
		t.Errorf("migration 7 left the row: %d", n)
	}
}

func TestRequestMetadataJobPriority(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.EnqueueMetadataJob(ctx, "aa", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestMetadataJob(ctx, "bb"); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.DueMetadataJobs(ctx, 10)
	if len(jobs) != 2 || jobs[0].Infohash != "bb" || jobs[0].Priority != 1 {
		t.Fatalf("interactive first: %+v", jobs)
	}
	// a failed crawl job is revived with priority
	_ = st.FinishMetadataJob(ctx, "aa", errors.New("x"), 0, 1)
	if err := st.RequestMetadataJob(ctx, "aa"); err != nil {
		t.Fatal(err)
	}
	if j, _ := st.GetMetadataJob(ctx, "aa"); j.Status != "pending" || j.Priority != 1 || j.Attempts != 0 {
		t.Errorf("revived %+v", j)
	}
}

func TestQBServerEdgeFields(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	id, err := st.SaveQBServer(ctx, QBServer{Name: "ams", URL: "http://qb", Enabled: true, EdgeURL: " https://edge.example/ ", EdgeAPI: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveQBServer(ctx, QBServer{Name: "x", URL: "http://qb", EdgeURL: "ftp://e"}); err == nil {
		t.Fatal("non-http edge URL accepted")
	}
	if err := st.SetQBEdgeToken(ctx, id, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQBEdgeToken(ctx, 999, "h2"); err == nil {
		t.Fatal("unknown server accepted")
	}
	got, ok, err := st.QBServerByEdgeToken(ctx, "h1")
	if err != nil || !ok || got != id {
		t.Fatalf("lookup: %d %v %v", got, ok, err)
	}
	if _, ok, _ := st.QBServerByEdgeToken(ctx, ""); ok {
		t.Fatal("empty hash matched")
	}
	// saving the server (e.g. from the admin form) keeps the token; rotating replaces it
	if _, err := st.SaveQBServer(ctx, QBServer{ID: id, Name: "ams", URL: "http://qb", EdgeURL: "https://edge.example"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SetQBEdgeToken(ctx, id, "h3")
	if _, ok, _ := st.QBServerByEdgeToken(ctx, "h1"); ok {
		t.Fatal("old token still valid after rotation")
	}
	ls, _ := st.ListQBServers(ctx)
	if len(ls) != 1 || ls[0].EdgeURL != "https://edge.example" || ls[0].EdgeAPI || !ls[0].EdgeBound() || ls[0].EdgeTokenHash != "h3" {
		t.Fatalf("list = %+v", ls)
	}
}

func TestWebseedProgressiveSettingRemoved(t *testing.T) {
	st := newStore(t)
	for _, sp := range SettingsSchema() {
		if sp.Key == "webseed.progressive" {
			t.Fatal("still in the schema")
		}
	}
	ctx := context.Background()
	st.W.Exec(`PRAGMA user_version = 8`)
	if _, err := st.W.Exec(`INSERT INTO settings(key, value) VALUES('webseed.progressive', 'false')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, st.W, migrations); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.R.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'webseed.progressive'`).Scan(&n)
	if n != 0 {
		t.Errorf("migration 9 left the row: %d", n)
	}
}

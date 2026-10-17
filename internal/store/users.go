package store

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Roles, ordered by privilege.
const (
	RoleGuest  = "guest"
	RoleMember = "member"
	RoleVIP    = "vip"
	RoleAdmin  = "admin"
)

// RoleRank orders roles for comparisons.
func RoleRank(role string) int {
	switch role {
	case RoleMember:
		return 1
	case RoleVIP:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// User is a registered account.
type User struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	Email       string  `json:"email"`
	Role        string  `json:"role"`
	VIPUntil    *string `json:"vip_until"`
	Disabled    bool    `json:"disabled"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt *string `json:"last_login_at,omitempty"`
	InvitedBy   *string `json:"invited_by,omitempty"`
}

// EffectiveRole resolves VIP expiry: a "vip" whose vip_until has passed acts as member,
// and a member with a valid vip_until acts as vip.
func (u *User) EffectiveRole() string {
	if u == nil {
		return RoleGuest
	}
	if u.Role == RoleAdmin {
		return RoleAdmin
	}
	vipValid := false
	if u.VIPUntil != nil {
		if t, err := time.Parse(time.RFC3339, *u.VIPUntil); err == nil && t.After(time.Now()) {
			vipValid = true
		}
	}
	if u.Role == RoleVIP && u.VIPUntil == nil {
		return RoleVIP // permanent vip
	}
	if vipValid {
		return RoleVIP
	}
	return RoleMember
}

var (
	ErrBadCredentials = errors.New("用户名或密码错误")
	ErrUserDisabled   = errors.New("账号已被禁用")
	ErrTaken          = errors.New("用户名或邮箱已被使用")
	ErrInviteInvalid  = errors.New("邀请码无效或已用完")
	ErrCodeInvalid    = errors.New("兑换码无效或已被使用")
)

var usernameRe = regexp.MustCompile(`^[\p{L}\p{N}_\-.]{2,32}$`)

// ValidateUsername checks the allowed username format.
func ValidateUsername(n string) error {
	if !usernameRe.MatchString(n) {
		return errors.New("用户名需为 2-32 位字母、数字、汉字或 _ - .")
	}
	return nil
}

// ValidatePassword checks the minimal password policy.
func ValidatePassword(p string) error {
	if len(p) < 8 || len(p) > 128 {
		return errors.New("密码长度需为 8-128 位")
	}
	return nil
}

const pbkdf2Iter = 210000

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdf2Iter, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(dk)), nil
}

func verifyPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, want) == 1
}

// RandomToken returns a URL-safe random token.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomCode returns a human-friendly uppercase code (base32, no padding).
func RandomCode(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
}

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

const userCols = `u.id, u.username, COALESCE(u.email,''), u.role, u.vip_until, u.disabled, u.created_at, u.last_login_at, (SELECT username FROM users i WHERE i.id = u.invited_by)`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var vip, last, inv sql.NullString
	var dis int
	if err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &vip, &dis, &u.CreatedAt, &last, &inv); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Disabled = dis != 0
	if vip.Valid {
		u.VIPUntil = &vip.String
	}
	if last.Valid {
		u.LastLoginAt = &last.String
	}
	if inv.Valid {
		u.InvitedBy = &inv.String
	}
	return &u, nil
}

// UserCount returns the number of users.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// GetUser loads a user by id.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.R.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = ?`, id))
}

// CreateUserParams are inputs to CreateUser.
type CreateUserParams struct {
	Username, Email, Password, Role string
	InviteCode                      string // consumed atomically if set
	RequireInvite                   bool
}

// CreateUser registers a user, consuming an invite if given/required.
func (s *Store) CreateUser(ctx context.Context, p CreateUserParams) (*User, error) {
	if err := ValidateUsername(p.Username); err != nil {
		return nil, err
	}
	if err := ValidatePassword(p.Password); err != nil {
		return nil, err
	}
	email := strings.TrimSpace(strings.ToLower(p.Email))
	if email != "" && !strings.Contains(email, "@") {
		return nil, errors.New("邮箱格式不正确")
	}
	hash, err := HashPassword(p.Password)
	if err != nil {
		return nil, err
	}
	role := p.Role
	if role == "" {
		role = RoleMember
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var invitedBy, inviteID sql.NullInt64
	var vipUntil sql.NullString
	code := strings.TrimSpace(p.InviteCode)
	if code == "" && p.RequireInvite {
		return nil, ErrInviteInvalid
	}
	if code != "" {
		var id int64
		var createdBy sql.NullInt64
		var maxUses, uses, vipDays int
		var invRole string
		var exp sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT id, created_by, max_uses, uses, role, vip_days, expires_at FROM invites WHERE code = ? AND disabled = 0`, code).
			Scan(&id, &createdBy, &maxUses, &uses, &invRole, &vipDays, &exp)
		if err != nil {
			return nil, ErrInviteInvalid
		}
		if (maxUses > 0 && uses >= maxUses) || (exp.Valid && exp.String < Now()) {
			return nil, ErrInviteInvalid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invites SET uses = uses + 1 WHERE id = ?`, id); err != nil {
			return nil, err
		}
		invitedBy, inviteID = createdBy, sql.NullInt64{Int64: id, Valid: true}
		if invRole == RoleVIP {
			if vipDays > 0 {
				vipUntil = sql.NullString{String: time.Now().UTC().AddDate(0, 0, vipDays).Format(time.RFC3339), Valid: true}
			} else {
				role = RoleVIP
			}
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO users(username,email,password_hash,role,vip_until,invited_by,invite_id,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		p.Username, nullStr(email), hash, role, vipUntil, invitedBy, inviteID, Now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrTaken
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getUserW(ctx, id)
}

func (s *Store) getUserW(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.W.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = ?`, id))
}

// Authenticate checks credentials (username or email).
func (s *Store) Authenticate(ctx context.Context, ident, password string) (*User, error) {
	var id int64
	var hash string
	var dis int
	err := s.R.QueryRowContext(ctx, `SELECT id, password_hash, disabled FROM users WHERE username = ? OR email = ?`, ident, strings.ToLower(ident)).Scan(&id, &hash, &dis)
	if err != nil {
		// burn similar time to avoid user enumeration by timing
		verifyPassword("pbkdf2$210000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		return nil, ErrBadCredentials
	}
	if !verifyPassword(hash, password) {
		return nil, ErrBadCredentials
	}
	if dis != 0 {
		return nil, ErrUserDisabled
	}
	_, _ = s.W.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, Now(), id)
	return s.getUserW(ctx, id)
}

// ChangePassword verifies the old password and sets a new one; other sessions are revoked.
func (s *Store) ChangePassword(ctx context.Context, id int64, oldPw, newPw string, keepSession string) error {
	if err := ValidatePassword(newPw); err != nil {
		return err
	}
	var hash string
	if err := s.R.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash); err != nil {
		return err
	}
	if !verifyPassword(hash, oldPw) {
		return errors.New("原密码错误")
	}
	return s.SetPassword(ctx, id, newPw, keepSession)
}

// SetPassword sets a password and revokes all sessions except keepSession.
func (s *Store) SetPassword(ctx context.Context, id int64, pw, keepSession string) error {
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, h, id); err != nil {
		return err
	}
	_, err = s.W.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, id, hashToken(keepSession))
	return err
}

// UpdateUser is the admin edit. nil fields are unchanged; VIPUntil "" clears it.
type UpdateUser struct {
	Role     *string `json:"role"`
	VIPUntil *string `json:"vip_until"`
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
}

func (s *Store) AdminUpdateUser(ctx context.Context, id int64, p UpdateUser) (*User, error) {
	if p.Role != nil {
		if RoleRank(*p.Role) == 0 {
			return nil, errors.New("无效角色")
		}
		if _, err := s.W.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, *p.Role, id); err != nil {
			return nil, err
		}
	}
	if p.VIPUntil != nil {
		var v any
		if *p.VIPUntil != "" {
			t, err := time.Parse(time.RFC3339, *p.VIPUntil)
			if err != nil {
				return nil, errors.New("vip_until 需为 RFC3339 时间")
			}
			v = t.UTC().Format(time.RFC3339)
		}
		if _, err := s.W.ExecContext(ctx, `UPDATE users SET vip_until = ? WHERE id = ?`, v, id); err != nil {
			return nil, err
		}
	}
	if p.Disabled != nil {
		if _, err := s.W.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, boolInt(*p.Disabled), id); err != nil {
			return nil, err
		}
		if *p.Disabled {
			_, _ = s.W.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		}
	}
	if p.Password != nil && *p.Password != "" {
		if err := ValidatePassword(*p.Password); err != nil {
			return nil, err
		}
		if err := s.SetPassword(ctx, id, *p.Password, ""); err != nil {
			return nil, err
		}
	}
	return s.getUserW(ctx, id)
}

// ListUsers searches users.
func (s *Store) ListUsers(ctx context.Context, q string, page, size int) ([]User, int, error) {
	where, args := "", []any{}
	if q != "" {
		where = ` WHERE u.username LIKE ? OR u.email LIKE ?`
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	var total int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT `+userCols+` FROM users u`+where+` ORDER BY u.id DESC LIMIT ? OFFSET ?`, append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *u)
	}
	return out, total, rows.Err()
}

// ---- sessions ----

// CreateSession returns a new opaque session token.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration, ip, ua string) (string, error) {
	tok := RandomToken(32)
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,created_at,expires_at,ip,user_agent) VALUES(?,?,?,?,?,?)`,
		hashToken(tok), userID, Now(), time.Now().UTC().Add(ttl).Format(time.RFC3339), ip, ua)
	return tok, err
}

// SessionUser resolves a session token to an enabled user.
func (s *Store) SessionUser(ctx context.Context, tok string) (*User, error) {
	if tok == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.R.QueryRowContext(ctx, `SELECT `+userCols+` FROM sessions x JOIN users u ON u.id = x.user_id
		WHERE x.token_hash = ? AND x.expires_at > ? AND u.disabled = 0`, hashToken(tok), Now()))
}

// DeleteSession logs out.
func (s *Store) DeleteSession(ctx context.Context, tok string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(tok))
	return err
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions(ctx context.Context) {
	_, _ = s.W.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, Now())
}

// ---- invites ----

type Invite struct {
	ID        int64   `json:"id"`
	Code      string  `json:"code"`
	URL       string  `json:"url"`
	MaxUses   int     `json:"max_uses"`
	Uses      int     `json:"uses"`
	Role      string  `json:"role"`
	VIPDays   int     `json:"vip_days"`
	ExpiresAt *string `json:"expires_at"`
	Note      string  `json:"note"`
	Disabled  bool    `json:"disabled"`
	CreatedAt string  `json:"created_at"`
	CreatedBy string  `json:"created_by"`
}

type NewInvite struct {
	MaxUses       int    `json:"max_uses"`
	Role          string `json:"role"`
	VIPDays       int    `json:"vip_days"`
	ExpiresInDays int    `json:"expires_in_days"`
	Note          string `json:"note"`
}

func (s *Store) CreateInvite(ctx context.Context, by int64, p NewInvite) (*Invite, error) {
	if p.Role == "" {
		p.Role = RoleMember
	}
	if p.Role != RoleMember && p.Role != RoleVIP {
		return nil, errors.New("邀请角色只能是 member 或 vip")
	}
	if p.MaxUses < 0 {
		p.MaxUses = 0
	}
	var exp any
	if p.ExpiresInDays > 0 {
		exp = time.Now().UTC().AddDate(0, 0, p.ExpiresInDays).Format(time.RFC3339)
	}
	code := RandomCode(10)
	res, err := s.W.ExecContext(ctx, `INSERT INTO invites(code,created_by,max_uses,role,vip_days,expires_at,note,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		code, by, p.MaxUses, p.Role, p.VIPDays, exp, p.Note, Now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	list, err := s.listInvites(ctx, s.W, `WHERE i.id = ?`, id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Store) ListInvites(ctx context.Context) ([]Invite, error) {
	return s.listInvites(ctx, s.R, ``)
}

func (s *Store) listInvites(ctx context.Context, db *sql.DB, where string, args ...any) ([]Invite, error) {
	rows, err := db.QueryContext(ctx, `SELECT i.id,i.code,i.max_uses,i.uses,i.role,i.vip_days,i.expires_at,i.note,i.disabled,i.created_at,COALESCE(u.username,'')
		FROM invites i LEFT JOIN users u ON u.id = i.created_by `+where+` ORDER BY i.id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var v Invite
		var exp sql.NullString
		var dis int
		if err := rows.Scan(&v.ID, &v.Code, &v.MaxUses, &v.Uses, &v.Role, &v.VIPDays, &exp, &v.Note, &dis, &v.CreatedAt, &v.CreatedBy); err != nil {
			return nil, err
		}
		if exp.Valid {
			v.ExpiresAt = &exp.String
		}
		v.Disabled = dis != 0
		v.URL = strings.TrimRight(s.Str(KeySiteBaseURL), "/") + "/register?invite=" + v.Code
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SetInviteDisabled(ctx context.Context, id int64, disabled bool) error {
	_, err := s.W.ExecContext(ctx, `UPDATE invites SET disabled = ? WHERE id = ?`, boolInt(disabled), id)
	return err
}

// ---- redeem codes ----

type RedeemCode struct {
	ID         int64   `json:"id"`
	Code       string  `json:"code"`
	VIPDays    int     `json:"vip_days"`
	Note       string  `json:"note"`
	CreatedAt  string  `json:"created_at"`
	UsedByName *string `json:"used_by_name"`
	UsedAt     *string `json:"used_at"`
}

func (s *Store) CreateRedeemCodes(ctx context.Context, by int64, count, days int, note string) ([]RedeemCode, error) {
	if count < 1 || count > 500 {
		return nil, errors.New("数量需为 1-500")
	}
	if days < 1 {
		return nil, errors.New("天数需 ≥ 1")
	}
	var ids []any
	for range count {
		res, err := s.W.ExecContext(ctx, `INSERT INTO redeem_codes(code,vip_days,note,created_by,created_at) VALUES(?,?,?,?,?)`, RandomCode(10), days, note, by, Now())
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	return s.listRedeem(ctx, s.W, `WHERE r.id IN (`+ph+`)`, ids...)
}

func (s *Store) ListRedeemCodes(ctx context.Context) ([]RedeemCode, error) {
	return s.listRedeem(ctx, s.R, ``)
}

func (s *Store) listRedeem(ctx context.Context, db *sql.DB, where string, args ...any) ([]RedeemCode, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.id,r.code,r.vip_days,r.note,r.created_at,u.username,r.used_at FROM redeem_codes r LEFT JOIN users u ON u.id = r.used_by `+where+` ORDER BY r.id DESC LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RedeemCode
	for rows.Next() {
		var v RedeemCode
		var un, ua sql.NullString
		if err := rows.Scan(&v.ID, &v.Code, &v.VIPDays, &v.Note, &v.CreatedAt, &un, &ua); err != nil {
			return nil, err
		}
		if un.Valid {
			v.UsedByName = &un.String
		}
		if ua.Valid {
			v.UsedAt = &ua.String
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Redeem applies a code: extends vip_until from max(now, current vip_until).
func (s *Store) Redeem(ctx context.Context, userID int64, code string) (*User, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	var days int
	if err := tx.QueryRowContext(ctx, `SELECT id, vip_days FROM redeem_codes WHERE code = ? AND used_by IS NULL`, strings.ToUpper(strings.TrimSpace(code))).Scan(&id, &days); err != nil {
		return nil, ErrCodeInvalid
	}
	var cur sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT vip_until FROM users WHERE id = ?`, userID).Scan(&cur); err != nil {
		return nil, err
	}
	base := time.Now().UTC()
	if cur.Valid {
		if t, err := time.Parse(time.RFC3339, cur.String); err == nil && t.After(base) {
			base = t
		}
	}
	until := base.AddDate(0, 0, days).Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `UPDATE users SET vip_until = ? WHERE id = ?`, until, userID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE redeem_codes SET used_by = ?, used_at = ? WHERE id = ?`, userID, Now(), id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getUserW(ctx, userID)
}

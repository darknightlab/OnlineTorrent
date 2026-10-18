package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// ---- qB servers ----

type QBServer struct {
	ID                  int64   `json:"id"`
	Name                string  `json:"name"`
	URL                 string  `json:"url"`
	Username            string  `json:"username"`
	Password            string  `json:"password,omitempty"`
	APIKey              string  `json:"api_key,omitempty"`
	Category            string  `json:"category"`
	Enabled             bool    `json:"enabled"`
	SeedingTimeLimitMin int     `json:"seeding_time_limit_min"`
	RatioLimit          float64 `json:"ratio_limit"`
	AddWSSTrackers      bool    `json:"add_wss_trackers"`
	CreatedAt           string  `json:"created_at"`
}

func (s *Store) ListQBServers(ctx context.Context) ([]QBServer, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id,name,url,username,password,api_key,category,enabled,seeding_time_limit_min,ratio_limit,add_wss_trackers,created_at FROM qb_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QBServer
	for rows.Next() {
		var q QBServer
		var en, wss int
		if err := rows.Scan(&q.ID, &q.Name, &q.URL, &q.Username, &q.Password, &q.APIKey, &q.Category, &en, &q.SeedingTimeLimitMin, &q.RatioLimit, &wss, &q.CreatedAt); err != nil {
			return nil, err
		}
		q.Enabled, q.AddWSSTrackers = en != 0, wss != 0
		out = append(out, q)
	}
	return out, rows.Err()
}

// SaveQBServer inserts (ID==0) or updates. Empty password/api_key on update = unchanged.
func (s *Store) SaveQBServer(ctx context.Context, q QBServer) (int64, error) {
	q.Name, q.URL = strings.TrimSpace(q.Name), strings.TrimRight(strings.TrimSpace(q.URL), "/")
	if q.Name == "" || !(strings.HasPrefix(q.URL, "http://") || strings.HasPrefix(q.URL, "https://")) {
		return 0, errors.New("名称不能为空，URL 需以 http(s):// 开头")
	}
	if q.Category == "" {
		q.Category = "projectk"
	}
	if q.ID == 0 {
		res, err := s.W.ExecContext(ctx, `INSERT INTO qb_servers(name,url,username,password,api_key,category,enabled,seeding_time_limit_min,ratio_limit,add_wss_trackers,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			q.Name, q.URL, q.Username, q.Password, q.APIKey, q.Category, boolInt(q.Enabled), q.SeedingTimeLimitMin, q.RatioLimit, boolInt(q.AddWSSTrackers), Now())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.W.ExecContext(ctx, `UPDATE qb_servers SET name=?,url=?,username=?,category=?,enabled=?,seeding_time_limit_min=?,ratio_limit=?,add_wss_trackers=?,
		password = CASE WHEN ?='' THEN password ELSE ? END, api_key = CASE WHEN ?='' THEN api_key ELSE ? END WHERE id=?`,
		q.Name, q.URL, q.Username, q.Category, boolInt(q.Enabled), q.SeedingTimeLimitMin, q.RatioLimit, boolInt(q.AddWSSTrackers),
		q.Password, q.Password, q.APIKey, q.APIKey, q.ID)
	return q.ID, err
}

func (s *Store) DeleteQBServer(ctx context.Context, id int64) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM qb_servers WHERE id = ?`, id)
	return err
}

// ---- rules ----

type Rule struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`
	Field     string `json:"-"`
	Op        string `json:"-"`
	Value     string `json:"-"`
	Action    string `json:"action"`
	Label     string `json:"label"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// RuleMatch is the JSON form of a rule's condition: {"field","op","value"}.
type RuleMatch struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type ruleJSON Rule

// MarshalJSON nests field/op/value under "match" (docs/api.md).
func (r Rule) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ruleJSON
		Match RuleMatch `json:"match"`
	}{ruleJSON(r), RuleMatch{r.Field, r.Op, r.Value}})
}

// UnmarshalJSON accepts the nested "match" form.
func (r *Rule) UnmarshalJSON(b []byte) error {
	var v struct {
		ruleJSON
		Match RuleMatch `json:"match"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*r = Rule(v.ruleJSON)
	r.Field, r.Op, r.Value = v.Match.Field, v.Match.Op, v.Match.Value
	return nil
}

// ValidateRule checks a rule before saving.
func ValidateRule(r *Rule) error {
	r.Name, r.Value = strings.TrimSpace(r.Name), strings.TrimSpace(r.Value)
	if r.Name == "" || r.Value == "" {
		return errors.New("名称和匹配值不能为空")
	}
	switch r.Field {
	case "title", "file", "infohash", "team", "tag", "any":
	default:
		return errors.New("无效字段")
	}
	switch r.Op {
	case "contains", "equals":
	case "regex":
		if _, err := regexp.Compile("(?i)" + r.Value); err != nil {
			return errors.New("正则表达式无效: " + err.Error())
		}
	default:
		return errors.New("无效匹配方式")
	}
	switch r.Action {
	case "allow", "login", "vip", "block":
	default:
		return errors.New("无效动作")
	}
	return nil
}

func (s *Store) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id,name,enabled,priority,field,op,value,action,label,note,created_at,updated_at FROM rules ORDER BY priority DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var en int
		if err := rows.Scan(&r.ID, &r.Name, &en, &r.Priority, &r.Field, &r.Op, &r.Value, &r.Action, &r.Label, &r.Note, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled = en != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SaveRule(ctx context.Context, r Rule) (int64, error) {
	if err := ValidateRule(&r); err != nil {
		return 0, err
	}
	now := Now()
	if r.ID == 0 {
		res, err := s.W.ExecContext(ctx, `INSERT INTO rules(name,enabled,priority,field,op,value,action,label,note,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			r.Name, boolInt(r.Enabled), r.Priority, r.Field, r.Op, r.Value, r.Action, r.Label, r.Note, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.W.ExecContext(ctx, `UPDATE rules SET name=?,enabled=?,priority=?,field=?,op=?,value=?,action=?,label=?,note=?,updated_at=? WHERE id=?`,
		r.Name, boolInt(r.Enabled), r.Priority, r.Field, r.Op, r.Value, r.Action, r.Label, r.Note, now, r.ID)
	return r.ID, err
}

func (s *Store) GetRule(ctx context.Context, id int64) (*Rule, error) {
	rules, err := s.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if rules[i].ID == id {
			return &rules[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id)
	return err
}

// ---- verdicts ----

type Verdict struct {
	Infohash  string `json:"infohash"`
	Action    string `json:"action"`
	Label     string `json:"label"`
	Note      string `json:"note"`
	DecidedAt string `json:"decided_at"`
}

func (s *Store) GetVerdict(ctx context.Context, ih string) (*Verdict, error) {
	var v Verdict
	err := s.R.QueryRowContext(ctx, `SELECT infohash,action,label,note,decided_at FROM verdicts WHERE infohash = ?`, ih).Scan(&v.Infohash, &v.Action, &v.Label, &v.Note, &v.DecidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (s *Store) SetVerdict(ctx context.Context, by *int64, v Verdict) error {
	return setVerdict(ctx, s.W, by, v)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func setVerdict(ctx context.Context, db execer, by *int64, v Verdict) error {
	_, err := db.ExecContext(ctx, `INSERT INTO verdicts(infohash,action,label,note,decided_by,decided_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(infohash) DO UPDATE SET action=excluded.action,label=excluded.label,note=excluded.note,decided_by=excluded.decided_by,decided_at=excluded.decided_at`,
		v.Infohash, v.Action, v.Label, Trunc(v.Note, MaxReasonBytes), by, Now())
	return err
}

// ---- reviews ----

type Review struct {
	ID           int64   `json:"id"`
	Infohash     string  `json:"infohash"`
	Title        string  `json:"title"`
	Source       string  `json:"source"`
	Reason       string  `json:"reason"`
	ReporterName *string `json:"reporter_name"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"created_at"`
	Verdict      *string `json:"verdict"`
}

// AddReview enqueues a pending review (deduplicated per infohash+source).
func (s *Store) AddReview(ctx context.Context, ih, title, source, reason string, reporter *int64) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO reviews(infohash,title,source,reason,reporter_id,created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT DO NOTHING`, ih, Trunc(title, MaxTitleBytes), source, Trunc(reason, MaxReasonBytes), reporter, Now())
	return err
}

func (s *Store) ListReviews(ctx context.Context, status string, page, size int) ([]Review, int, error) {
	var total int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM reviews WHERE status = ?`, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT r.id,r.infohash,r.title,r.source,r.reason,u.username,r.status,r.created_at,v.action
		FROM reviews r LEFT JOIN users u ON u.id = r.reporter_id LEFT JOIN verdicts v ON v.infohash = r.infohash
		WHERE r.status = ? ORDER BY r.id DESC LIMIT ? OFFSET ?`, status, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var r Review
		var un, vd sql.NullString
		if err := rows.Scan(&r.ID, &r.Infohash, &r.Title, &r.Source, &r.Reason, &un, &r.Status, &r.CreatedAt, &vd); err != nil {
			return nil, 0, err
		}
		if un.Valid {
			r.ReporterName = &un.String
		}
		if vd.Valid {
			r.Verdict = &vd.String
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ResolveReview closes a review; action "dismiss" leaves no verdict.
// The verdict and the review status change are written in one transaction.
func (s *Store) ResolveReview(ctx context.Context, by *int64, id int64, action, label, note string) (string, error) {
	status := "resolved"
	switch action {
	case "dismiss":
		status = "dismissed"
	case "allow", "login", "vip", "block":
	default:
		return "", errors.New("无效动作")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var ih string
	if err := tx.QueryRowContext(ctx, `SELECT infohash FROM reviews WHERE id = ?`, id).Scan(&ih); err != nil {
		return "", ErrNotFound
	}
	if action != "dismiss" {
		if err := setVerdict(ctx, tx, by, Verdict{Infohash: ih, Action: action, Label: label, Note: note}); err != nil {
			return "", err
		}
	}
	// resolve every pending review for the same torrent
	if _, err := tx.ExecContext(ctx, `UPDATE reviews SET status=?, resolved_by=?, resolved_at=? WHERE infohash = ? AND status = 'pending'`, status, by, Now(), ih); err != nil {
		return "", err
	}
	return ih, tx.Commit()
}

// ---- torrents / plays ----

type TorrentRecord struct {
	Infohash  string
	Title     string // trusted: qB metadata or catalogue
	UserTitle string // the dn= a user supplied; never trusted for display/policy decisions
	Size      int64
	Files     []string
	Team      string
	Tags      []string
	Source    string
	SourceID  string
	Trusted   bool // from a trusted crawl source (policy: no user-magnet floor)
}

func (s *Store) UpsertTorrent(ctx context.Context, t TorrentRecord) error {
	fl := t.Files
	if len(fl) > MaxFilesStored {
		fl = fl[:MaxFilesStored]
	}
	cut := make([]string, len(fl))
	for i, f := range fl {
		cut[i] = Trunc(f, MaxPathBytes)
	}
	tg := make([]string, 0, len(t.Tags))
	for i, g := range t.Tags {
		if i >= 50 {
			break
		}
		tg = append(tg, Trunc(g, 100))
	}
	files, _ := json.Marshal(cut)
	tags, _ := json.Marshal(tg)
	_, err := s.W.ExecContext(ctx, `INSERT INTO torrents(infohash,title,user_title,size_bytes,files_json,team,tags_json,source,source_id,trusted,first_seen,last_played) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(infohash) DO UPDATE SET
		  title = CASE WHEN excluded.title != '' THEN excluded.title ELSE title END,
		  user_title = CASE WHEN excluded.user_title != '' THEN excluded.user_title ELSE user_title END,
		  size_bytes = CASE WHEN excluded.size_bytes > 0 THEN excluded.size_bytes ELSE size_bytes END,
		  files_json = CASE WHEN excluded.files_json != '[]' AND excluded.files_json != 'null' THEN excluded.files_json ELSE files_json END,
		  team = CASE WHEN excluded.team != '' THEN excluded.team ELSE team END,
		  tags_json = CASE WHEN excluded.tags_json != '[]' AND excluded.tags_json != 'null' THEN excluded.tags_json ELSE tags_json END,
		  source = CASE WHEN excluded.source != 'user' THEN excluded.source ELSE source END,
		  source_id = CASE WHEN excluded.source_id != '' THEN excluded.source_id ELSE source_id END,
		  trusted = MAX(trusted, excluded.trusted),
		  last_played = excluded.last_played`,
		t.Infohash, Trunc(t.Title, MaxTitleBytes), Trunc(t.UserTitle, MaxTitleBytes), t.Size, string(files), Trunc(t.Team, 100), string(tags), t.Source, Trunc(t.SourceID, 64), boolInt(t.Trusted), Now(), Now())
	return err
}

func (s *Store) GetTorrent(ctx context.Context, ih string) (*TorrentRecord, error) {
	var t TorrentRecord
	var files, tags string
	err := s.R.QueryRowContext(ctx, `SELECT infohash,title,user_title,size_bytes,files_json,team,tags_json,source,source_id,trusted FROM torrents WHERE infohash = ?`, ih).
		Scan(&t.Infohash, &t.Title, &t.UserTitle, &t.Size, &files, &t.Team, &tags, &t.Source, &t.SourceID, &t.Trusted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(files), &t.Files)
	_ = json.Unmarshal([]byte(tags), &t.Tags)
	return &t, nil
}

type Play struct {
	ID        int64    `json:"id"`
	Infohash  string   `json:"infohash"`
	Title     string   `json:"name"`
	Username  *string  `json:"user"`
	IP        string   `json:"ip"`
	Action    string   `json:"action"`
	SeededBy  string   `json:"-"`
	Seeded    []string `json:"seeded_by"`
	CreatedAt string   `json:"created_at"`
}

func (s *Store) AddPlay(ctx context.Context, p Play, userID *int64) {
	_, _ = s.W.ExecContext(ctx, `INSERT INTO plays(infohash,title,user_id,ip,action,seeded_by,created_at) VALUES(?,?,?,?,?,?,?)`,
		p.Infohash, Trunc(p.Title, MaxTitleBytes), userID, Trunc(p.IP, 64), p.Action, p.SeededBy, Now())
}

func (s *Store) ListPlays(ctx context.Context, page, size int) ([]Play, int, error) {
	var total int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM plays`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT p.id,p.infohash,p.title,u.username,p.ip,p.action,p.seeded_by,p.created_at FROM plays p LEFT JOIN users u ON u.id = p.user_id ORDER BY p.id DESC LIMIT ? OFFSET ?`, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Play
	for rows.Next() {
		var p Play
		var un sql.NullString
		if err := rows.Scan(&p.ID, &p.Infohash, &p.Title, &un, &p.IP, &p.Action, &p.SeededBy, &p.CreatedAt); err != nil {
			return nil, 0, err
		}
		if un.Valid {
			p.Username = &un.String
		}
		p.Seeded = []string{}
		if p.SeededBy != "" {
			p.Seeded = strings.Split(p.SeededBy, ",")
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// ---- audit ----

type AuditEntry struct {
	ID        int64   `json:"id"`
	Actor     *string `json:"actor"`
	Action    string  `json:"action"`
	Target    string  `json:"target"`
	Detail    string  `json:"detail"`
	IP        string  `json:"ip"`
	CreatedAt string  `json:"created_at"`
}

func (s *Store) Audit(ctx context.Context, actor *int64, action, target, detail, ip string) {
	_, _ = s.W.ExecContext(ctx, `INSERT INTO audit(actor_id,action,target,detail,ip,created_at) VALUES(?,?,?,?,?,?)`, actor, action, Trunc(target, MaxTitleBytes), Trunc(detail, MaxReasonBytes), Trunc(ip, 64), Now())
}

func (s *Store) ListAudit(ctx context.Context, page, size int) ([]AuditEntry, int, error) {
	var total int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT a.id,u.username,a.action,a.target,a.detail,a.ip,a.created_at FROM audit a LEFT JOIN users u ON u.id = a.actor_id ORDER BY a.id DESC LIMIT ? OFFSET ?`, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var un sql.NullString
		if err := rows.Scan(&a.ID, &un, &a.Action, &a.Target, &a.Detail, &a.IP, &a.CreatedAt); err != nil {
			return nil, 0, err
		}
		if un.Valid {
			a.Actor = &un.String
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// Stats are dashboard counters.
type Stats struct {
	Users          int `json:"users"`
	Plays24h       int `json:"plays_24h"`
	PendingReviews int `json:"pending_reviews"`
	Rules          int `json:"rules"`
}

func (s *Store) Stats(ctx context.Context, since string) (Stats, error) {
	var st Stats
	err := s.R.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users), (SELECT COUNT(*) FROM plays WHERE created_at >= ?), (SELECT COUNT(*) FROM reviews WHERE status='pending'), (SELECT COUNT(*) FROM rules WHERE enabled=1)`, since).
		Scan(&st.Users, &st.Plays24h, &st.PendingReviews, &st.Rules)
	return st, err
}

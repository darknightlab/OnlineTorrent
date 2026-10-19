package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Per-role content scope for subjects (番剧条目), modelled on YotsubaNews
// store/content_scope.go. A scope restricts which subjects a role can see:
//
//   - tag_mode "all": every subject (minus exclude_tags);
//     "selected": only subjects matching the tag expression.
//   - tags + tag_operators: an expression "t0 op0 t1 op1 t2 …" where AND binds tighter
//     than OR — the terms are split into groups at every OR and a subject matches when it
//     has all tags of at least one group: 原创 AND 校园 OR 战斗 = (原创 ∧ 校园) ∨ 战斗.
//   - exclude_tags: a subject with ANY of these tags is hidden (also in "all" mode).
//
// A subject's tag set is the union of works.tags (bgm user tags) and works.meta_tags (bgm
// wiki tags) as stored; the scope is purely tag-based (adult content is excluded through
// real tags such as 里番 / R18 / 18禁). Works without any tag (e.g. bangumi-data-only entries, external search results) never
// match a "selected" expression, so they are hidden in that mode.
//
// admin is always unrestricted. Compiled plans are cached by the API server and replaced
// on every write.

const (
	ScopeAll       = "all"
	ScopeSelected  = "selected"
	TagOperatorAnd = "and"
	TagOperatorOr  = "or"

	// Admin input limits.
	MaxScopeTags    = 50
	MaxScopeTagRune = 60
)

// ScopeRoles are the roles that have a content scope (admin is always unrestricted).
var ScopeRoles = []string{RoleGuest, RoleMember, RoleVIP}

// ContentScope is the stored (and admin API) form of a role's scope.
type ContentScope struct {
	Role         string   `json:"role"`
	TagMode      string   `json:"tag_mode"`
	Tags         []string `json:"tags"`
	TagOperators []string `json:"tag_operators"`
	ExcludeTags  []string `json:"exclude_tags"`
}

// DefaultContentScope is the unrestricted scope every role starts with.
func DefaultContentScope(role string) ContentScope {
	return ContentScope{Role: role, TagMode: ScopeAll, Tags: []string{}, TagOperators: []string{}, ExcludeTags: []string{}}
}

// ScopeTarget is anything with a subject tag set (meta.Work, meta.WorkRef, schedule items).
type ScopeTarget interface {
	// ScopeFacts returns the bgm user tags and the bgm wiki tags.
	ScopeFacts() (tags, metaTags []string)
}

// ScopePlan is a compiled scope. A nil plan is unrestricted.
type ScopePlan struct {
	AllTags     bool
	Groups      [][]string // OR of AND-groups (deduplicated per group)
	ExcludeTags []string
	exclude     map[string]struct{}
}

// UnrestrictedScopePlan sees everything (admin, defaults).
func UnrestrictedScopePlan() *ScopePlan { return &ScopePlan{AllTags: true} }

// DenyAllScopePlan sees nothing (fail closed when scopes cannot be loaded).
func DenyAllScopePlan() *ScopePlan { return &ScopePlan{AllTags: false} }

// CompileScope turns a stored scope into a plan.
func CompileScope(scope ContentScope) *ScopePlan {
	p := &ScopePlan{AllTags: scope.TagMode != ScopeSelected, ExcludeTags: normalizeScopeValues(scope.ExcludeTags)}
	p.exclude = make(map[string]struct{}, len(p.ExcludeTags))
	for _, t := range p.ExcludeTags {
		p.exclude[t] = struct{}{}
	}
	if !p.AllTags {
		p.Groups = tagGroups(scope.Tags, scope.TagOperators)
	}
	return p
}

// Restricted reports whether the plan hides anything.
func (p *ScopePlan) Restricted() bool { return p != nil && (!p.AllTags || len(p.ExcludeTags) > 0) }

// DeniesAll reports a "selected" plan with an empty expression.
func (p *ScopePlan) DeniesAll() bool { return p != nil && !p.AllTags && len(p.Groups) == 0 }

// tagGroups splits the expression at OR operators (AND binds tighter).
func tagGroups(tags, operators []string) [][]string {
	operators = effectiveTagOperators(tags, operators, TagOperatorAnd)
	var groups [][]string
	var cur []string
	for i, tag := range tags {
		tag = strings.TrimSpace(tag)
		if i > 0 && operators[i-1] == TagOperatorOr && len(cur) > 0 {
			groups = append(groups, normalizeScopeValues(cur))
			cur = nil
		}
		if tag != "" {
			cur = append(cur, tag)
		}
	}
	if len(cur) > 0 {
		groups = append(groups, normalizeScopeValues(cur))
	}
	return groups
}

// SubjectTagSet is the tag union of a subject.
func SubjectTagSet(tags, metaTags []string) map[string]struct{} {
	set := make(map[string]struct{}, len(tags)+len(metaTags))
	for _, t := range tags {
		set[t] = struct{}{}
	}
	for _, t := range metaTags {
		set[t] = struct{}{}
	}
	return set
}

// Visible is the in-memory check for an already loaded subject; it must agree with
// WorkScopePredicate.
func (p *ScopePlan) Visible(t ScopeTarget) bool {
	if !p.Restricted() {
		return true
	}
	if p.DeniesAll() {
		return false
	}
	tags, metaTags := t.ScopeFacts()
	set := SubjectTagSet(tags, metaTags)
	for ex := range p.exclude {
		if _, ok := set[ex]; ok {
			return false
		}
	}
	if p.AllTags {
		return true
	}
	for _, g := range p.Groups {
		all := true
		for _, tag := range g {
			if _, ok := set[tag]; !ok {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// workTagsSQL is a subquery yielding the tag union of works row alias (column "value").
func workTagsSQL(alias string) string {
	j := func(col string) string {
		return `SELECT value FROM json_each(CASE WHEN json_valid(` + alias + `.` + col + `) THEN ` + alias + `.` + col + ` ELSE '[]' END)`
	}
	return `(` + j("tags") + ` UNION ALL ` + j("meta_tags") + `)`
}

// WorkScopePredicate is the SQL condition (meta DB, works table aliased alias) of a plan.
func WorkScopePredicate(alias string, scope *ScopePlan) (string, []any) {
	if !scope.Restricted() {
		return "1=1", nil
	}
	if scope.DeniesAll() {
		return "0=1", nil
	}
	var where []string
	var args []any
	if len(scope.ExcludeTags) > 0 {
		where = append(where, `NOT EXISTS (SELECT 1 FROM `+workTagsSQL(alias)+` WHERE value IN (`+inPlaceholders(len(scope.ExcludeTags))+`))`)
		args = append(args, toArgs(scope.ExcludeTags)...)
	}
	if !scope.AllTags {
		groups := make([]string, 0, len(scope.Groups))
		for _, g := range scope.Groups {
			if len(g) == 1 {
				groups = append(groups, `EXISTS (SELECT 1 FROM `+workTagsSQL(alias)+` WHERE value = ?)`)
				args = append(args, g[0])
				continue
			}
			groups = append(groups, `(SELECT COUNT(DISTINCT value) FROM `+workTagsSQL(alias)+` WHERE value IN (`+inPlaceholders(len(g))+`)) = ?`)
			args = append(args, toArgs(g)...)
			args = append(args, len(g))
		}
		where = append(where, "("+strings.Join(groups, " OR ")+")")
	}
	return strings.Join(where, " AND "), args
}

// TorrentScopePredicate hides torrents (meta DB torrents aliased alias) that have any
// non-rejected file mapped to a subject outside the scope; unmapped torrents stay visible.
func TorrentScopePredicate(alias string, scope *ScopePlan) (string, []any) {
	if !scope.Restricted() {
		return "1=1", nil
	}
	pred, args := WorkScopePredicate("scope_w", scope)
	return `NOT EXISTS (SELECT 1 FROM torrent_files scope_f JOIN works scope_w ON scope_w.key = scope_f.work_key
		WHERE scope_f.infohash = ` + alias + `.infohash AND scope_f.status != 'rejected' AND NOT (` + pred + `))`, args
}

// TagExpressionString renders "(原创 AND 校园) OR 战斗" (for logs / audit).
func TagExpressionString(tags, operators []string) string {
	groups := tagGroups(tags, operators)
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = strings.Join(g, " AND ")
		if len(g) > 1 && len(groups) > 1 {
			parts[i] = "(" + parts[i] + ")"
		}
	}
	return strings.Join(parts, " OR ")
}

func inPlaceholders(n int) string { return strings.TrimRight(strings.Repeat("?,", n), ",") }

func toArgs(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func normalizeScopeValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}

func validateScopeMode(mode string) bool { return mode == ScopeAll || mode == ScopeSelected }

func validTagOperator(operator string) bool {
	return operator == TagOperatorAnd || operator == TagOperatorOr
}

func effectiveTagOperators(tags, operators []string, fallback string) []string {
	if len(tags) < 2 {
		return []string{}
	}
	out := make([]string, len(tags)-1)
	for i := range out {
		if i < len(operators) && validTagOperator(operators[i]) {
			out[i] = operators[i]
		} else {
			out[i] = fallback
		}
	}
	return out
}

// ErrScopeInvalid wraps validation errors of NormalizeContentScope.
var ErrScopeInvalid = errors.New("invalid content scope")

func scopeErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrScopeInvalid, fmt.Sprintf(format, a...))
}

func validScopeTag(tag string) error {
	if tag == "" {
		return scopeErr("标签不能为空")
	}
	if utf8.RuneCountInString(tag) > MaxScopeTagRune {
		return scopeErr("标签过长（最多 %d 字）: %s", MaxScopeTagRune, tag)
	}
	return nil
}

// NormalizeContentScope validates admin input: tag mode, ≤ 50 tags per list, names
// ≤ 60 characters, one "and"/"or" operator between consecutive tags (may be omitted for
// a single tag), a non-empty expression in "selected" mode. "all" mode drops the
// expression; exclude tags are trimmed, deduplicated and sorted.
func NormalizeContentScope(scope ContentScope) (ContentScope, error) {
	if !validateScopeMode(scope.TagMode) {
		return scope, scopeErr("tag_mode 只能是 all 或 selected")
	}
	if len(scope.Tags) > MaxScopeTags || len(scope.ExcludeTags) > MaxScopeTags {
		return scope, scopeErr("每个列表最多 %d 个标签", MaxScopeTags)
	}
	tags := make([]string, len(scope.Tags))
	for i, t := range scope.Tags {
		tags[i] = strings.TrimSpace(t)
		if err := validScopeTag(tags[i]); err != nil {
			return scope, err
		}
	}
	ops := scope.TagOperators
	if ops == nil {
		ops = []string{}
	}
	if len(ops) != max(0, len(tags)-1) {
		return scope, scopeErr("tag_operators 数量须为标签数 - 1")
	}
	for _, op := range ops {
		if !validTagOperator(op) {
			return scope, scopeErr(`tag_operators 只能包含 "and" 或 "or"`)
		}
	}
	for i, t := range scope.ExcludeTags {
		if err := validScopeTag(strings.TrimSpace(t)); err != nil {
			return scope, err
		}
		scope.ExcludeTags[i] = strings.TrimSpace(t)
	}
	if scope.TagMode == ScopeSelected && len(tags) == 0 {
		return scope, scopeErr("「仅所选标签」模式至少需要一个标签")
	}
	scope.Tags, scope.TagOperators = tags, slices.Clone(ops)
	if scope.TagMode == ScopeAll {
		scope.Tags, scope.TagOperators = []string{}, []string{}
	}
	scope.ExcludeTags = normalizeScopeValues(scope.ExcludeTags)
	return scope, nil
}

// ContentScopes loads the scopes of every scope role (missing rows → default).
func (s *Store) ContentScopes(ctx context.Context) (map[string]ContentScope, error) {
	out := make(map[string]ContentScope, len(ScopeRoles))
	for _, role := range ScopeRoles {
		out[role] = DefaultContentScope(role)
	}
	rows, err := s.R.QueryContext(ctx, `SELECT role, tag_mode FROM role_content_scopes`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var role, mode string
		if err := rows.Scan(&role, &mode); err != nil {
			rows.Close()
			return nil, err
		}
		if sc, ok := out[role]; ok {
			sc.TagMode = mode
			out[role] = sc
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.R.QueryContext(ctx, `SELECT role, position, tag_name, operator FROM role_tag_expression_terms ORDER BY role, position`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var role, tag, op string
		var pos int
		if err := rows.Scan(&role, &pos, &tag, &op); err != nil {
			rows.Close()
			return nil, err
		}
		sc, ok := out[role]
		if !ok {
			continue
		}
		if len(sc.Tags) > 0 {
			sc.TagOperators = append(sc.TagOperators, op)
		}
		sc.Tags = append(sc.Tags, tag)
		out[role] = sc
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.R.QueryContext(ctx, `SELECT role, tag_name FROM role_excluded_tags ORDER BY role, tag_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role, tag string
		if err := rows.Scan(&role, &tag); err != nil {
			return nil, err
		}
		if sc, ok := out[role]; ok {
			sc.ExcludeTags = append(sc.ExcludeTags, tag)
			out[role] = sc
		}
	}
	return out, rows.Err()
}

// ContentScope returns one role's scope (admin: unrestricted; unknown role: ErrNotFound).
func (s *Store) ContentScope(ctx context.Context, role string) (ContentScope, error) {
	if role == RoleAdmin {
		return DefaultContentScope(role), nil
	}
	if !slices.Contains(ScopeRoles, role) {
		return ContentScope{}, ErrNotFound
	}
	all, err := s.ContentScopes(ctx)
	if err != nil {
		return ContentScope{}, err
	}
	return all[role], nil
}

// SetContentScope validates and stores a role's scope, returning the normalized value.
func (s *Store) SetContentScope(ctx context.Context, role string, scope ContentScope) (ContentScope, error) {
	if role == RoleAdmin {
		return ContentScope{}, scopeErr("管理员不受内容范围限制")
	}
	if !slices.Contains(ScopeRoles, role) {
		return ContentScope{}, ErrNotFound
	}
	scope, err := NormalizeContentScope(scope)
	if err != nil {
		return ContentScope{}, err
	}
	scope.Role = role
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return ContentScope{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO role_content_scopes(role, tag_mode) VALUES(?, ?)
		ON CONFLICT(role) DO UPDATE SET tag_mode = excluded.tag_mode`, role, scope.TagMode); err != nil {
		return ContentScope{}, err
	}
	for _, table := range []string{"role_tag_expression_terms", "role_excluded_tags"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE role = ?`, role); err != nil {
			return ContentScope{}, err
		}
	}
	for pos, tag := range scope.Tags {
		op := ""
		if pos > 0 {
			op = scope.TagOperators[pos-1]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_tag_expression_terms(role, position, tag_name, operator) VALUES(?,?,?,?)`, role, pos, tag, op); err != nil {
			return ContentScope{}, err
		}
	}
	for _, tag := range scope.ExcludeTags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_excluded_tags(role, tag_name) VALUES(?,?)`, role, tag); err != nil {
			return ContentScope{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ContentScope{}, err
	}
	return scope, nil
}

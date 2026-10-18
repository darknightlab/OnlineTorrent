// Package policy decides whether a user may play a torrent.
//
// Engine.Requirement computes what a subject needs (allow < login < vip < block); Decide
// then compares it to the user's effective role. Precedence:
//
//  1. Per-torrent verdict (infohash, set by an admin or by the automatic re-check) — final.
//     A verdict labelled "political"/"porn" is always block.
//  2. Hard-block rules: if ANY enabled rule labelled "political" or "porn" matches, the
//     result is block immediately, regardless of priorities.
//  3. Other rules: among matches the highest priority wins, ties are broken by the most
//     restrictive action. Without a match the configured default action applies.
//  4. Source floor: for untrusted subjects (user-pasted magnets) the result is raised to at
//     least the configured "user magnet" level. Only a verdict (1) or a winning rule on
//     field "infohash" may lower an untrusted subject below the floor — title/file rules
//     can't, because those fields are attacker-controlled (dn=) until metadata is known.
//
// The public reason (Decision.Reason / Access.Reason) is always derived from action and
// label; verdict notes and rule names are kept in Note/RuleName for admin views only.
// "block" denies everyone, including admins (admins can inspect via the policy test endpoint).
package policy

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Actions in increasing restrictiveness.
const (
	Allow = "allow"
	Login = "login"
	VIP   = "vip"
	Block = "block"
)

// Labels that force a block.
var hardBlockLabels = map[string]bool{"political": true, "porn": true}

func rank(a string) int {
	switch a {
	case Login:
		return 1
	case VIP:
		return 2
	case Block:
		return 3
	}
	return 0
}

// Subject is what we know about a torrent.
type Subject struct {
	Infohash string
	Title    string
	Files    []string
	Team     string
	Tags     []string
	Trusted  bool // from a crawl source marked trusted (e.g. bangumi.moe), not a user-pasted magnet
}

// Decision is the outcome.
type Decision struct {
	Action   string `json:"action"`            // what the subject requires: allow|login|vip|block
	Allowed  bool   `json:"allowed"`           // whether this user may play it
	Reason   string `json:"reason"`            // public, human readable: always reasonFor(Action, Label)
	Label    string `json:"label,omitempty"`   // political|porn|copyright|other
	RuleID   int64  `json:"rule_id,omitempty"` // matched rule
	RuleName string `json:"rule_name,omitempty"`
	Note     string `json:"note,omitempty"` // verdict note (admin only)
	Source   string `json:"source"`         // verdict|rule|source|default
}

// Access is the compact form returned with torrent listings.
type Access struct {
	Action string `json:"action"` // allow (playable by this user) | login | vip | block
	Reason string `json:"reason"`
}

// Access converts a decision into the per-user listing form.
func (d Decision) Access() Access {
	if d.Allowed {
		return Access{Action: Allow}
	}
	return Access{Action: d.Action, Reason: d.Reason}
}

type compiled struct {
	store.Rule
	re *regexp.Regexp
}

// Engine evaluates rules; it caches compiled rules until Invalidate is called.
type Engine struct {
	st *store.Store

	mu       sync.RWMutex
	rules    []compiled
	gen      uint64 // bumped by Invalidate
	cacheGen uint64 // generation the cached rules were loaded at
	valid    bool
}

// New creates an engine.
func New(st *store.Store) *Engine { return &Engine{st: st, gen: 1} }

// Invalidate drops the rule cache (call after rule or policy setting edits).
func (e *Engine) Invalidate() {
	e.mu.Lock()
	e.gen++
	e.valid = false
	e.mu.Unlock()
}

// Generation identifies the current rule set; it changes on every Invalidate.
func (e *Engine) Generation() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.gen
}

func (e *Engine) load(ctx context.Context) ([]compiled, error) {
	e.mu.RLock()
	if e.valid && e.cacheGen == e.gen {
		r := e.rules
		e.mu.RUnlock()
		return r, nil
	}
	gen := e.gen
	e.mu.RUnlock()
	rules, err := e.st.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	var out []compiled
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		c := compiled{Rule: r}
		if r.Op == "regex" {
			re, err := regexp.Compile("(?i)" + r.Value)
			if err != nil {
				continue
			}
			c.re = re
		}
		out = append(out, c)
	}
	e.mu.Lock()
	// only publish if no Invalidate happened while we were loading (else we'd cache stale rules)
	if e.gen == gen {
		e.rules, e.cacheGen, e.valid = out, gen, true
	}
	e.mu.Unlock()
	return out, nil
}

func (c compiled) matchOne(s string) bool {
	switch c.Op {
	case "equals":
		return strings.EqualFold(strings.TrimSpace(s), c.Value)
	case "contains":
		return strings.Contains(strings.ToLower(s), strings.ToLower(c.Value))
	case "regex":
		return c.re != nil && c.re.MatchString(s)
	}
	return false
}

func (c compiled) match(s Subject) bool {
	var fields []string
	switch c.Field {
	case "title":
		fields = []string{s.Title}
	case "file":
		fields = s.Files
	case "infohash":
		fields = []string{s.Infohash}
	case "team":
		fields = []string{s.Team}
	case "tag":
		fields = s.Tags
	case "any":
		fields = append(append([]string{s.Title, s.Team, s.Infohash}, s.Files...), s.Tags...)
	}
	for _, f := range fields {
		if f != "" && c.matchOne(f) {
			return true
		}
	}
	return false
}

// Requirement computes what the subject requires, independent of the user.
func (e *Engine) Requirement(ctx context.Context, s Subject) (Decision, error) {
	if s.Infohash != "" {
		v, err := e.st.GetVerdict(ctx, s.Infohash)
		if err != nil {
			return Decision{}, err
		}
		if v != nil {
			d := Decision{Action: v.Action, Label: v.Label, Source: "verdict", Note: v.Note}
			if hardBlockLabels[v.Label] {
				d.Action = Block
			}
			d.Reason = reasonFor(d.Action, d.Label)
			return d, nil
		}
	}
	rules, err := e.load(ctx)
	if err != nil {
		return Decision{}, err
	}
	var best *compiled
	bestAction := ""
	for i := range rules {
		r := &rules[i]
		if !r.match(s) {
			continue
		}
		if hardBlockLabels[r.Label] {
			return Decision{Action: Block, Label: r.Label, RuleID: r.ID, RuleName: r.Name, Source: "rule", Reason: reasonFor(Block, r.Label)}, nil
		}
		act := r.Action
		if best == nil || r.Priority > best.Priority || (r.Priority == best.Priority && rank(act) > rank(bestAction)) {
			best, bestAction = r, act
		}
	}
	var d Decision
	if best != nil {
		d = Decision{Action: bestAction, Label: best.Label, RuleID: best.ID, RuleName: best.Name, Source: "rule"}
	} else {
		d = Decision{Action: e.st.Str(store.KeyDefaultAction), Source: "default"}
		if rank(d.Action) == 0 {
			d.Action = Allow
		}
	}
	// untrusted subjects: only an infohash rule may go below the user-magnet floor
	if !s.Trusted && (best == nil || best.Field != "infohash") {
		if floor := e.st.Str(store.KeyUserMagnetAction); rank(floor) > rank(d.Action) {
			d.Action, d.Source = floor, "source"
		}
	}
	d.Reason = reasonFor(d.Action, d.Label)
	return d, nil
}

// Decide evaluates the subject for a user (nil = guest).
func (e *Engine) Decide(ctx context.Context, u *store.User, s Subject) (Decision, error) {
	d, err := e.Requirement(ctx, s)
	if err != nil {
		return d, err
	}
	d.Allowed = Permits(u.EffectiveRole(), d.Action)
	return d, nil
}

// Permits reports whether a role satisfies a required action.
func Permits(role, action string) bool {
	switch action {
	case Allow:
		return true
	case Login:
		return store.RoleRank(role) >= store.RoleRank(store.RoleMember)
	case VIP:
		return store.RoleRank(role) >= store.RoleRank(store.RoleVIP)
	}
	return false // block
}

func reasonFor(action, label string) string {
	switch action {
	case Block:
		switch label {
		case "political":
			return "该资源涉及敏感内容，已被屏蔽"
		case "porn":
			return "该资源含有色情内容，已被屏蔽"
		case "copyright":
			return "该资源因版权原因已被屏蔽"
		}
		return "该资源已被屏蔽"
	case VIP:
		return "该资源仅限大会员观看"
	case Login:
		return "登录后即可观看"
	}
	return ""
}

// Package policy decides whether a user may play a torrent.
//
// Decide is the single entry point. Precedence:
//
//  1. Per-torrent verdict set by an admin (infohash) — final.
//  2. Rules: every enabled rule is evaluated; among matches the highest priority wins,
//     ties are broken by the most restrictive action (block > vip > login > allow).
//     Rules labelled "political" or "porn" always act as block regardless of their action.
//  3. Source floor: torrents not from a trusted catalogue (user-submitted magnets) need at
//     least the configured "user magnet" level.
//  4. Default action.
//
// The resulting requirement is then compared to the user's effective role. "block" denies
// everyone, including admins (admins can inspect via the policy test endpoint).
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
	Trusted  bool // from a catalogue we index (bangumi.moe), not a user-pasted magnet
}

// Decision is the outcome.
type Decision struct {
	Action   string `json:"action"`            // what the subject requires: allow|login|vip|block
	Allowed  bool   `json:"allowed"`           // whether this user may play it
	Reason   string `json:"reason"`            // human readable
	Label    string `json:"label,omitempty"`   // political|porn|copyright|other
	RuleID   int64  `json:"rule_id,omitempty"` // matched rule
	RuleName string `json:"rule_name,omitempty"`
	Source   string `json:"source"` // verdict|rule|source|default
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

	mu    sync.RWMutex
	rules []compiled
	valid bool
}

// New creates an engine.
func New(st *store.Store) *Engine { return &Engine{st: st} }

// Invalidate drops the rule cache (call after rule edits).
func (e *Engine) Invalidate() {
	e.mu.Lock()
	e.valid = false
	e.mu.Unlock()
}

func (e *Engine) load(ctx context.Context) ([]compiled, error) {
	e.mu.RLock()
	if e.valid {
		r := e.rules
		e.mu.RUnlock()
		return r, nil
	}
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
	e.rules, e.valid = out, true
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
			d := Decision{Action: v.Action, Label: v.Label, Source: "verdict", Reason: v.Note}
			if hardBlockLabels[v.Label] {
				d.Action = Block
			}
			if d.Reason == "" {
				d.Reason = reasonFor(d.Action, d.Label)
			}
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
		act := r.Action
		if hardBlockLabels[r.Label] {
			act = Block
		}
		if best == nil || r.Priority > best.Priority || (r.Priority == best.Priority && rank(act) > rank(bestAction)) {
			best, bestAction = r, act
		}
	}
	if best != nil {
		d := Decision{Action: bestAction, Label: best.Label, RuleID: best.ID, RuleName: best.Name, Source: "rule", Reason: reasonFor(bestAction, best.Label)}
		// an explicit allow rule overrides the user-magnet floor only if it is not weaker than default
		return d, nil
	}
	d := Decision{Action: e.st.Str(store.KeyDefaultAction), Source: "default"}
	if d.Action == "" {
		d.Action = Allow
	}
	if !s.Trusted {
		if floor := e.st.Str(store.KeyUserMagnetAction); rank(floor) > rank(d.Action) {
			d.Action, d.Source = floor, "source"
		}
	}
	d.Reason = reasonFor(d.Action, "")
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

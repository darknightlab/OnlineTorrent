package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	adm := func(method, path, label string, h handler) {
		s.route(mux, method, path, AccessAdmin, "后台", label, nil, h)
	}
	adm("GET", "/api/v1/admin/stats", "概览 · 查看", s.adminStats)
	adm("GET", "/api/v1/admin/settings", "站点设置 · 查看", s.adminGetSettings)
	adm("PUT", "/api/v1/admin/settings", "站点设置 · 修改", s.adminPutSettings)
	adm("GET", "/api/v1/admin/qb-servers", "qB 服务器 · 查看", s.adminListQB)
	adm("POST", "/api/v1/admin/qb-servers", "qB 服务器 · 新建/操作", s.adminSaveQB)
	adm("PUT", "/api/v1/admin/qb-servers/id/{id}", "qB 服务器 · 修改", s.adminSaveQB)
	adm("DELETE", "/api/v1/admin/qb-servers/id/{id}", "qB 服务器 · 删除", s.adminDeleteQB)
	adm("POST", "/api/v1/admin/qb-servers/id/{id}/test", "qB 服务器 · 新建/操作", s.adminTestQB)
	adm("GET", "/api/v1/admin/users", "用户 · 查看", s.adminListUsers)
	adm("PUT", "/api/v1/admin/users/id/{id}", "用户 · 修改", s.adminUpdateUser)
	adm("GET", "/api/v1/admin/invites", "邀请码 · 查看", s.adminListInvites)
	adm("POST", "/api/v1/admin/invites", "邀请码 · 新建/操作", s.adminCreateInvite)
	adm("PUT", "/api/v1/admin/invites/id/{id}", "邀请码 · 修改", s.adminUpdateInvite)
	adm("GET", "/api/v1/admin/redeem-codes", "兑换码 · 查看", s.adminListRedeem)
	adm("POST", "/api/v1/admin/redeem-codes", "兑换码 · 新建/操作", s.adminCreateRedeem)
	adm("GET", "/api/v1/admin/rules", "策略规则 · 查看", s.adminListRules)
	adm("POST", "/api/v1/admin/rules", "策略规则 · 新建/操作", s.adminSaveRule)
	adm("PUT", "/api/v1/admin/rules/id/{id}", "策略规则 · 修改", s.adminSaveRule)
	adm("DELETE", "/api/v1/admin/rules/id/{id}", "策略规则 · 删除", s.adminDeleteRule)
	adm("POST", "/api/v1/admin/policy/test", "策略测试 · 新建/操作", s.adminPolicyTest)
	adm("GET", "/api/v1/admin/reviews", "审核队列 · 查看", s.adminListReviews)
	adm("PUT", "/api/v1/admin/reviews/id/{id}", "审核队列 · 修改", s.adminResolveReview)
	adm("GET", "/api/v1/admin/plays", "播放记录 · 查看", s.adminListPlays)
	adm("GET", "/api/v1/admin/audit", "审计日志 · 查看", s.adminListAudit)
	adm("GET", "/api/v1/admin/permissions", "接口权限 · 查看", s.adminGetPermissions)
	adm("PUT", "/api/v1/admin/permissions", "接口权限 · 修改", s.adminPutPermissions)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func list(w http.ResponseWriter, items any) {
	httpx.JSON(w, 200, map[string]any{"items": items})
}

func paged(w http.ResponseWriter, items any, page, total, size int) {
	httpx.JSON(w, 200, map[string]any{"items": items, "page": page, "page_count": httpx.PageCount(total, size), "total": total})
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) error {
	st, err := s.St.Stats(r.Context(), time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	on, total := s.Pool.OnlineCount()
	sched := s.Cat.Schedule()
	n := 0
	if sched != nil {
		n = len(sched.Items)
	}
	httpx.JSON(w, 200, map[string]any{"users": st.Users, "plays_24h": st.Plays24h, "pending_reviews": st.PendingReviews, "rules": st.Rules, "qb_online": on, "qb_total": total, "schedule_items": n})
	return nil
}

func (s *Server) adminGetSettings(w http.ResponseWriter, r *http.Request) error {
	list(w, s.St.SettingItems())
	return nil
}

func (s *Server) adminPutSettings(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := s.St.UpdateSettings(r.Context(), in.Values); err != nil {
		return httpx.Bad(err)
	}
	keys := make([]string, 0, len(in.Values))
	for k := range in.Values {
		keys = append(keys, k)
	}
	s.audit(r, "settings.update", strings.Join(keys, ","), "")
	s.Policy.Invalidate()
	list(w, s.St.SettingItems())
	return nil
}

type qbView struct {
	store.QBServer
	qb.Status
	HasPass   bool `json:"has_password"`
	HasAPIKey bool `json:"has_api_key"`
}

func (s *Server) qbViews(r *http.Request) ([]qbView, error) {
	cfgs, err := s.St.ListQBServers(r.Context())
	if err != nil {
		return nil, err
	}
	out := []qbView{}
	for _, c := range cfgs {
		v := qbView{QBServer: c, HasPass: c.Password != "", HasAPIKey: c.APIKey != ""}
		v.Password, v.APIKey = "", ""
		if sv := s.Pool.Get(c.ID); sv != nil {
			v.Status = sv.Status()
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) adminListQB(w http.ResponseWriter, r *http.Request) error {
	v, err := s.qbViews(r)
	if err != nil {
		return err
	}
	list(w, v)
	return nil
}

func (s *Server) adminSaveQB(w http.ResponseWriter, r *http.Request) error {
	var in store.QBServer
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.ID = pathID(r)
	id, err := s.St.SaveQBServer(r.Context(), in)
	if err != nil {
		return httpx.Bad(err)
	}
	s.audit(r, "qb.save", in.Name, in.URL)
	if err := s.Pool.Reload(r.Context()); err != nil {
		return err
	}
	if sv := s.Pool.Get(id); sv != nil {
		s.Pool.Check(r.Context(), sv)
	}
	return s.adminListQB(w, r)
}

func (s *Server) adminDeleteQB(w http.ResponseWriter, r *http.Request) error {
	if err := s.St.DeleteQBServer(r.Context(), pathID(r)); err != nil {
		return err
	}
	s.audit(r, "qb.delete", r.PathValue("id"), "")
	if err := s.Pool.Reload(r.Context()); err != nil {
		return err
	}
	return s.adminListQB(w, r)
}

func (s *Server) adminTestQB(w http.ResponseWriter, r *http.Request) error {
	sv := s.Pool.Get(pathID(r))
	if sv == nil {
		return httpx.E(404, "not_found", "服务器不存在")
	}
	httpx.JSON(w, 200, s.Pool.Check(r.Context(), sv))
	return nil
}

const pageSize = 50

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) error {
	page := httpx.Page(r)
	us, total, err := s.St.ListUsers(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), page, pageSize)
	if err != nil {
		return err
	}
	paged(w, nonNil(us), page, total, pageSize)
	return nil
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) error {
	var in store.UpdateUser
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	id := pathID(r)
	if me := userOf(r); me.ID == id && ((in.Role != nil && *in.Role != store.RoleAdmin) || (in.Disabled != nil && *in.Disabled)) {
		return httpx.E(400, "bad_request", "不能降级或禁用自己")
	}
	u, err := s.St.AdminUpdateUser(r.Context(), id, in)
	if err != nil {
		return httpx.Bad(err)
	}
	b, _ := json.Marshal(map[string]any{"role": in.Role, "vip_until": in.VIPUntil, "disabled": in.Disabled, "password_reset": in.Password != nil && *in.Password != ""})
	s.audit(r, "user.update", u.Username, string(b))
	httpx.JSON(w, 200, u)
	return nil
}

func (s *Server) adminListInvites(w http.ResponseWriter, r *http.Request) error {
	v, err := s.St.ListInvites(r.Context())
	if err != nil {
		return err
	}
	list(w, nonNil(v))
	return nil
}

func (s *Server) adminCreateInvite(w http.ResponseWriter, r *http.Request) error {
	var in store.NewInvite
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v, err := s.St.CreateInvite(r.Context(), userOf(r).ID, in)
	if err != nil {
		return httpx.Bad(err)
	}
	s.audit(r, "invite.create", v.Code, fmt.Sprintf("max_uses=%d role=%s vip_days=%d", v.MaxUses, v.Role, v.VIPDays))
	httpx.JSON(w, 200, v)
	return nil
}

func (s *Server) adminUpdateInvite(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Disabled bool `json:"disabled"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := s.St.SetInviteDisabled(r.Context(), pathID(r), in.Disabled); err != nil {
		return err
	}
	s.audit(r, "invite.update", r.PathValue("id"), fmt.Sprintf("disabled=%v", in.Disabled))
	return s.adminListInvites(w, r)
}

func (s *Server) adminListRedeem(w http.ResponseWriter, r *http.Request) error {
	v, err := s.St.ListRedeemCodes(r.Context())
	if err != nil {
		return err
	}
	list(w, nonNil(v))
	return nil
}

func (s *Server) adminCreateRedeem(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Count   int    `json:"count"`
		VIPDays int    `json:"vip_days"`
		Note    string `json:"note"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v, err := s.St.CreateRedeemCodes(r.Context(), userOf(r).ID, in.Count, in.VIPDays, in.Note)
	if err != nil {
		return httpx.Bad(err)
	}
	s.audit(r, "redeem.create", fmt.Sprintf("%d codes", len(v)), fmt.Sprintf("vip_days=%d", in.VIPDays))
	list(w, v)
	return nil
}

func (s *Server) adminListRules(w http.ResponseWriter, r *http.Request) error {
	v, err := s.St.ListRules(r.Context())
	if err != nil {
		return err
	}
	list(w, nonNil(v))
	return nil
}

func (s *Server) adminSaveRule(w http.ResponseWriter, r *http.Request) error {
	var in store.Rule
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.ID = pathID(r)
	if _, err := s.St.SaveRule(r.Context(), in); err != nil {
		return httpx.Bad(err)
	}
	s.Policy.Invalidate()
	s.audit(r, "rule.save", in.Name, fmt.Sprintf("%s %s %q → %s %s", in.Field, in.Op, in.Value, in.Action, in.Label))
	return s.adminListRules(w, r)
}

func (s *Server) adminDeleteRule(w http.ResponseWriter, r *http.Request) error {
	if err := s.St.DeleteRule(r.Context(), pathID(r)); err != nil {
		return err
	}
	s.Policy.Invalidate()
	s.audit(r, "rule.delete", r.PathValue("id"), "")
	return s.adminListRules(w, r)
}

func (s *Server) adminPolicyTest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Title    string   `json:"title"`
		Infohash string   `json:"infohash"`
		Files    []string `json:"files"`
		Tags     []string `json:"tags"`
		Team     string   `json:"team"`
		Trusted  bool     `json:"trusted"`
		Role     string   `json:"role"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	d, err := s.Policy.Requirement(r.Context(), policy.Subject{Infohash: strings.ToLower(in.Infohash), Title: in.Title, Files: in.Files, Tags: in.Tags, Team: in.Team, Trusted: in.Trusted})
	if err != nil {
		return err
	}
	roles := map[string]bool{}
	for _, role := range []string{store.RoleGuest, store.RoleMember, store.RoleVIP, store.RoleAdmin} {
		roles[role] = policy.Permits(role, d.Action)
	}
	var matched *store.Rule
	if d.RuleID != 0 {
		matched, _ = s.St.GetRule(r.Context(), d.RuleID)
	}
	httpx.JSON(w, 200, map[string]any{"decision": d, "access": policy.Access{Action: d.Action, Reason: d.Reason}, "matched_rule": matched, "allowed_for": roles})
	return nil
}

func (s *Server) adminListReviews(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	page := httpx.Page(r)
	v, total, err := s.St.ListReviews(r.Context(), status, page, pageSize)
	if err != nil {
		return err
	}
	paged(w, nonNil(v), page, total, pageSize)
	return nil
}

func (s *Server) adminResolveReview(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Action string `json:"action"`
		Label  string `json:"label"`
		Note   string `json:"note"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	me := userOf(r)
	ih, err := s.St.ResolveReview(r.Context(), &me.ID, pathID(r), in.Action, in.Label, in.Note)
	if err != nil {
		return httpx.Bad(err)
	}
	if in.Action != "dismiss" && (in.Action == policy.Block || in.Label == "political" || in.Label == "porn") {
		s.bg.Go(func() {
			ctx, cancel := context.WithTimeout(s.BaseCtx, time.Minute)
			defer cancel()
			s.Pool.Remove(ctx, ih)
		})
	}
	s.audit(r, "review.resolve", ih, in.Action+" "+in.Label)
	httpx.JSON(w, 200, map[string]any{})
	return nil
}

func (s *Server) adminListPlays(w http.ResponseWriter, r *http.Request) error {
	page := httpx.Page(r)
	v, total, err := s.St.ListPlays(r.Context(), page, pageSize)
	if err != nil {
		return err
	}
	paged(w, nonNil(v), page, total, pageSize)
	return nil
}

func (s *Server) adminListAudit(w http.ResponseWriter, r *http.Request) error {
	page := httpx.Page(r)
	v, total, err := s.St.ListAudit(r.Context(), page, pageSize)
	if err != nil {
		return err
	}
	paged(w, nonNil(v), page, total, pageSize)
	return nil
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/site", s.h(s.getSite))
	mux.HandleFunc("GET /api/v1/me", s.h(s.getMe))
	mux.HandleFunc("POST /api/v1/auth/register", s.h(s.register))
	mux.HandleFunc("POST /api/v1/auth/login", s.h(s.login))
	mux.HandleFunc("POST /api/v1/auth/logout", s.h(s.logout))
	mux.HandleFunc("POST /api/v1/me/password", s.authed(s.changePassword))
	mux.HandleFunc("POST /api/v1/me/redeem", s.authed(s.redeem))

	mux.HandleFunc("GET /api/v1/schedule", s.h(s.getSchedule))
	mux.HandleFunc("GET /api/v1/anime/{key}", s.h(s.getAnime))
	mux.HandleFunc("GET /api/v1/torrents/latest", s.h(s.getLatest))
	mux.HandleFunc("GET /api/v1/torrents/search", s.h(s.getSearch))
	mux.HandleFunc("POST /api/v1/play", s.h(s.play))
	mux.HandleFunc("POST /api/v1/report", s.h(s.report))

	s.adminRoutes(mux)
}

type siteResp struct {
	Name             string           `json:"name"`
	AnnouncementMD   string           `json:"announcement_md"`
	Registration     string           `json:"registration"`
	InviteRequired   bool             `json:"invite_required"`
	TurnstileSiteKey string           `json:"turnstile_site_key"`
	Trackers         []string         `json:"trackers"`
	ICEServers       []map[string]any `json:"ice_servers"`
	MaxAutoSizeBytes int64            `json:"max_auto_size_bytes"`
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) error {
	reg := s.St.Str(store.KeyRegistration)
	var ice []map[string]any
	for _, u := range s.St.List(store.KeyICEServers) {
		ice = append(ice, map[string]any{"urls": []string{u}})
	}
	if ice == nil {
		ice = []map[string]any{}
	}
	trackers := s.St.List(store.KeyTrackers)
	if trackers == nil {
		trackers = []string{}
	}
	httpx.JSON(w, 200, siteResp{
		Name: s.St.Str(store.KeySiteName), AnnouncementMD: s.St.Str(store.KeyAnnouncement),
		Registration: reg, InviteRequired: reg == "invite", TurnstileSiteKey: s.St.Str(store.KeyTurnstileSiteKey),
		Trackers: trackers, ICEServers: ice, MaxAutoSizeBytes: int64(s.St.Num(store.KeyMaxAutoSize)) << 20,
	})
	return nil
}

type meResp struct {
	User *meUser `json:"user"`
}

type meUser struct {
	*store.User
	EffectiveRole string `json:"effective_role"`
}

func meOf(u *store.User) meResp {
	if u == nil {
		return meResp{}
	}
	return meResp{User: &meUser{User: u, EffectiveRole: u.EffectiveRole()}}
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, meOf(userOf(r)))
	return nil
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, u *store.User) error {
	days := s.St.Num(store.KeySessionDays)
	if days < 1 {
		days = 30
	}
	ttl := time.Duration(days*24) * time.Hour
	tok, err := s.St.CreateSession(r.Context(), u.ID, ttl, httpx.ClientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || r.Header.Get("CF-Visitor") != "", MaxAge: int(ttl.Seconds())})
	return nil
}

func (s *Server) rateLimit(r *http.Request, name string) error {
	if !s.limiter.allow(name+":"+httpx.ClientIP(r), 10, 30*time.Second) {
		return httpx.E(http.StatusTooManyRequests, "rate_limited", "操作过于频繁，请稍后再试")
	}
	return nil
}

func (s *Server) verifyTurnstile(ctx context.Context, token, ip string) error {
	secret := s.St.Str(store.KeyTurnstileSecret)
	if s.St.Str(store.KeyTurnstileSiteKey) == "" || secret == "" {
		return nil
	}
	if token == "" {
		return httpx.E(400, "turnstile", "请完成人机验证")
	}
	form := url.Values{"secret": {secret}, "response": {token}, "remoteip": {ip}}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return httpx.E(502, "turnstile", "人机验证服务不可用")
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.Success {
		return httpx.E(400, "turnstile", "人机验证失败，请重试")
	}
	return nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) error {
	if err := s.rateLimit(r, "register"); err != nil {
		return err
	}
	var in struct {
		Username, Email, Password string
		InviteCode                string `json:"invite_code"`
		TurnstileToken            string `json:"turnstile_token"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	mode := s.St.Str(store.KeyRegistration)
	if mode == "closed" {
		return httpx.E(403, "registration_closed", "当前未开放注册")
	}
	if err := s.verifyTurnstile(r.Context(), in.TurnstileToken, httpx.ClientIP(r)); err != nil {
		return err
	}
	u, err := s.St.CreateUser(r.Context(), store.CreateUserParams{
		Username: strings.TrimSpace(in.Username), Email: in.Email, Password: in.Password,
		InviteCode: in.InviteCode, RequireInvite: mode == "invite",
	})
	if err != nil {
		if errors.Is(err, store.ErrTaken) {
			return httpx.E(409, "taken", err.Error())
		}
		var he *httpx.Error
		if errors.As(err, &he) {
			return err
		}
		if strings.Contains(err.Error(), "constraint") {
			return err
		}
		return httpx.Bad(err)
	}
	s.St.Audit(r.Context(), &u.ID, "user.register", u.Username, in.InviteCode, httpx.ClientIP(r))
	if err := s.setSession(w, r, u); err != nil {
		return err
	}
	httpx.JSON(w, 200, meOf(u))
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	if err := s.rateLimit(r, "login"); err != nil {
		return err
	}
	var in struct{ Identifier, Password string }
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := s.St.Authenticate(r.Context(), strings.TrimSpace(in.Identifier), in.Password)
	if err != nil {
		if errors.Is(err, store.ErrBadCredentials) || errors.Is(err, store.ErrUserDisabled) {
			return httpx.E(401, "bad_credentials", err.Error())
		}
		return err
	}
	if err := s.setSession(w, r, u); err != nil {
		return err
	}
	httpx.JSON(w, 200, meOf(u))
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.St.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	httpx.JSON(w, 200, map[string]any{})
	return nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	keep := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		keep = c.Value
	}
	if err := s.St.ChangePassword(r.Context(), userOf(r).ID, in.Old, in.New, keep); err != nil {
		return httpx.Bad(err)
	}
	httpx.JSON(w, 200, map[string]any{})
	return nil
}

func (s *Server) redeem(w http.ResponseWriter, r *http.Request) error {
	if err := s.rateLimit(r, "redeem"); err != nil {
		return err
	}
	var in struct{ Code string }
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := s.St.Redeem(r.Context(), userOf(r).ID, in.Code)
	if err != nil {
		if errors.Is(err, store.ErrCodeInvalid) {
			return httpx.Bad(err)
		}
		return err
	}
	s.audit(r, "user.redeem", u.Username, in.Code)
	httpx.JSON(w, 200, meOf(u))
	return nil
}

// ---- catalog ----

func (s *Server) withAccess(r *http.Request, ts []*catalog.Torrent) []catalog.Torrent {
	u := userOf(r)
	out := make([]catalog.Torrent, 0, len(ts))
	for _, t := range ts {
		c := *t
		d, err := s.Policy.Decide(r.Context(), u, c.Subject())
		if err == nil {
			a := d.Access()
			c.Access = &a
		}
		out = append(out, c)
	}
	return out
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) error {
	sc := s.Cat.Schedule()
	if sc == nil {
		httpx.JSON(w, 200, catalog.Schedule{GeneratedAt: store.Now(), Items: []*catalog.ScheduleItem{}})
		return nil
	}
	// attach per-user access to latest torrents
	out := catalog.Schedule{GeneratedAt: sc.GeneratedAt, Season: sc.Season, Items: make([]*catalog.ScheduleItem, 0, len(sc.Items))}
	u := userOf(r)
	for _, it := range sc.Items {
		c := *it
		if it.LatestTorrent != nil {
			t := *it.LatestTorrent
			if d, err := s.Policy.Decide(r.Context(), u, t.Subject()); err == nil {
				a := d.Access()
				t.Access = &a
			}
			c.LatestTorrent = &t
		}
		out.Items = append(out.Items, &c)
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	httpx.JSON(w, 200, out)
	return nil
}

func (s *Server) getLatest(w http.ResponseWriter, r *http.Request) error {
	page := httpx.Page(r)
	items, pc, err := s.Cat.Latest(r.Context(), page)
	if err != nil {
		return httpx.E(502, "upstream", "bangumi.moe 暂时无法访问")
	}
	httpx.JSON(w, 200, map[string]any{"page": page, "page_count": pc, "items": s.withAccess(r, items)})
	return nil
}

func (s *Server) getSearch(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return httpx.E(400, "bad_request", "请输入搜索关键词")
	}
	page := httpx.Page(r)
	items, pc, err := s.Cat.Search(r.Context(), q, page)
	if err != nil {
		return httpx.E(502, "upstream", "bangumi.moe 暂时无法访问")
	}
	httpx.JSON(w, 200, map[string]any{"page": page, "page_count": pc, "items": s.withAccess(r, items)})
	return nil
}

func (s *Server) getAnime(w http.ResponseWriter, r *http.Request) error {
	key := r.PathValue("key")
	work, err := s.Meta.GetWork(r.Context(), key)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.E(404, "not_found", "番剧不存在")
	}
	if err != nil {
		return err
	}
	if work.Summary == "" && work.External["bgm"] != "" {
		s.fetchBgmDetail(r.Context(), work)
	}
	item, _ := s.Cat.ItemFor(work, time.Now())
	type animeOut struct {
		*catalog.ScheduleItem
		Summary  string            `json:"summary"`
		Type     string            `json:"type"`
		Official string            `json:"official_site"`
		External map[string]string `json:"external"`
		Tags     []string          `json:"tags"`
	}
	a := animeOut{ScheduleItem: item, Summary: work.Summary, Type: work.Type, Official: work.Official, External: work.External, Tags: []string{}}
	if item == nil {
		a.ScheduleItem = &catalog.ScheduleItem{Key: work.Key, Title: work.Title, TitleCN: work.TitleCN, Cover: s.Cat.CoverFor(work)}
	}
	var list []*catalog.Torrent
	if id := work.External["bangumi_moe"]; id != "" {
		list, _, err = s.Cat.ByTag(r.Context(), id, 1)
	} else {
		q := work.TitleCN
		if q == "" {
			q = work.Title
		}
		list, _, err = s.Cat.Search(r.Context(), q, 1)
	}
	if err != nil {
		list = nil
	}
	httpx.JSON(w, 200, map[string]any{"anime": a, "torrents": s.withAccess(r, list)})
	return nil
}

// fetchBgmDetail fills summary/cover from bgm.tv (best effort, short timeout).
func (s *Server) fetchBgmDetail(ctx context.Context, work *meta.Work) {
	key := "bgm:" + work.External["bgm"]
	if !s.limiter.allow("bgmfetch:"+key, 1, time.Hour) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, "https://api.bgm.tv/v0/subjects/"+url.PathEscape(work.External["bgm"]), nil)
	req.Header.Set("User-Agent", s.St.Str(store.KeyBgmUserAgent))
	req.Header.Set("Accept", "application/json")
	resp, err := s.Cat.HTTPClient().Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var sub struct {
		Summary string `json:"summary"`
		Images  struct {
			Common string `json:"common"`
			Large  string `json:"large"`
		} `json:"images"`
	}
	if json.NewDecoder(resp.Body).Decode(&sub) != nil {
		return
	}
	cover := sub.Images.Common
	if cover == "" {
		cover = sub.Images.Large
	}
	if err := s.Meta.SetDetail(ctx, work.Key, sub.Summary, cover); err == nil {
		work.Summary = sub.Summary
		if work.Cover == "" {
			work.Cover = cover
		}
	}
}

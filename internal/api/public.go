package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func (s *Server) routes(mux *http.ServeMux) {
	pub := func(method, path, label string, h handler) {
		s.route(mux, method, path, AccessPublic, "账号", label, nil, h)
	}
	pub("GET", "/api/v1/site", "站点信息", s.getSite)
	pub("GET", "/api/v1/me", "当前用户与权限", s.getMe)
	pub("POST", "/api/v1/auth/register", "注册", s.register)
	pub("POST", "/api/v1/auth/login", "登录", s.login)
	pub("POST", "/api/v1/auth/logout", "退出登录", s.logout)
	s.route(mux, "POST", "/api/v1/me/password", AccessUser, "账号", "修改密码", []string{store.RoleMember}, s.changePassword)
	s.route(mux, "POST", "/api/v1/me/redeem", AccessUser, "账号", "使用兑换码", []string{store.RoleMember}, s.redeem)

	s.route(mux, "GET", "/api/v1/schedule", AccessConfigurable, "浏览", "新番时间表", []string{store.RoleGuest}, s.getSchedule)
	s.route(mux, "GET", "/api/v1/subjects/{provider}/{id}", AccessConfigurable, "浏览", "条目详情（单季）", []string{store.RoleGuest}, s.getSubject)
	s.route(mux, "GET", "/api/v1/subjects/{provider}/{id}/episodes", AccessConfigurable, "浏览", "选集列表（正片 + SP / NCOP / NCED / PV / CM）", []string{store.RoleGuest}, s.getEpisodes)
	s.route(mux, "GET", "/api/v1/subjects/{provider}/{id}/episodes/{n}", AccessConfigurable, "浏览", "单集资源列表", []string{store.RoleGuest}, s.getEpisode)
	s.route(mux, "GET", "/api/v1/search/{keyword}", AccessConfigurable, "浏览", "搜索（番剧 + 种子，本地库；本地无番剧时按需查询 bgm.tv / TMDB）", []string{store.RoleGuest}, s.getSearch)
	s.route(mux, "GET", "/api/v1/torrents/hash/{ih}", AccessConfigurable, "浏览", "种子收录状态与文件刮削结果", []string{store.RoleGuest}, s.getTorrentStatus)

	s.route(mux, "POST", "/api/v1/torrents/hash/{ih}/play", AccessConfigurable, "播放", "打开种子（按 infohash）", []string{store.RoleGuest}, s.playHash)
	s.route(mux, "POST", "/api/v1/torrents/moe/{id}/play", AccessConfigurable, "播放", "打开种子（按 bangumi.moe ID）", []string{store.RoleGuest}, s.playMoe)
	s.route(mux, "POST", "/api/v1/torrents/hash/{ih}/seed", AccessConfigurable, "播放", "让服务器下载做种（消耗服务器资源）", []string{store.RoleMember}, s.seed)
	s.route(mux, "GET", "/webseed/hash/{ih}/{path...}", AccessConfigurable, "播放", "WebSeed 文件下载（HTTP 加速，消耗服务器流量）", []string{store.RoleMember}, s.serveWebseed)
	s.route(mux, "GET", "/api/v1/torrents/hash/{ih}/webseed/events", AccessConfigurable, "播放", "等待 WebSeed 就绪（服务器推送）", []string{store.RoleMember}, s.webseedEvents)
	s.route(mux, "GET", "/api/v1/torrents/hash/{ih}/webseed/events/file/{idx}", AccessConfigurable, "播放", "等待指定文件的 WebSeed 就绪（服务器推送）", []string{store.RoleMember}, s.webseedEvents)
	s.route(mux, "POST", "/api/v1/torrents/hash/{ih}/report", AccessUser, "播放", "举报种子", []string{store.RoleMember}, s.report)

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
	Webseed          siteWebseed      `json:"webseed"`
}

type siteWebseed struct {
	Enabled  bool `json:"enabled"`
	DelaySec int  `json:"delay_sec"` // wait this long after playback starts (prefer P2P), then subscribe to /webseed/events
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
		Webseed: siteWebseed{Enabled: s.St.Bool(store.KeyWebseedEnabled), DelaySec: int(s.St.Num(store.KeyWebseedDelaySec))},
	})
	return nil
}

type meResp struct {
	User *meUser `json:"user"`
	// route permission keys this user may call, e.g. "POST /api/v1/torrents/{ih}/seed"
	Permissions []string `json:"permissions"`
}

type meUser struct {
	*store.User
	EffectiveRole string `json:"effective_role"`
}

func (s *Server) meOf(u *store.User) meResp {
	out := meResp{Permissions: s.permissionsFor(roleOf(u))}
	if u != nil {
		out.User = &meUser{User: u, EffectiveRole: u.EffectiveRole()}
	}
	return out
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, s.meOf(userOf(r)))
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

var errRateLimited = httpx.E(http.StatusTooManyRequests, "rate_limited", "操作过于频繁，请稍后再试")

// ipKey is the rate-limit key for the client (IPv6 grouped by /64).
func ipKey(r *http.Request) string { return httpx.RateKey(httpx.ClientIP(r)) }

func (s *Server) rateLimit(r *http.Request, name string) error {
	return s.rateLimitN(r, name, 10, 30*time.Second)
}

func (s *Server) rateLimitN(r *http.Request, name string, burst float64, every time.Duration) error {
	if !s.limiter.allow(name+":"+ipKey(r), burst, every) {
		return errRateLimited
	}
	return nil
}

// per-account login failures: 5 per 15 minutes
const (
	loginFailBurst = 5
	loginFailEvery = 3 * time.Minute
)

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
	if err := httpx.DecodeSmall(r, &in); err != nil {
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
	httpx.JSON(w, 200, s.meOf(u))
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	if err := s.rateLimit(r, "login"); err != nil {
		return err
	}
	var in struct{ Identifier, Password string }
	if err := httpx.DecodeSmall(r, &in); err != nil {
		return err
	}
	ident := strings.TrimSpace(in.Identifier)
	if ident == "" || len(ident) > 254 || len(in.Password) > 128 {
		return httpx.E(401, "bad_credentials", store.ErrBadCredentials.Error())
	}
	acct := "loginfail:" + strings.ToLower(ident)
	if !s.limiter.peek(acct, loginFailBurst, loginFailEvery) {
		return httpx.E(http.StatusTooManyRequests, "rate_limited", "该账号登录失败次数过多，请 15 分钟后再试")
	}
	u, err := s.St.Authenticate(r.Context(), ident, in.Password)
	if err != nil {
		if errors.Is(err, store.ErrBadCredentials) || errors.Is(err, store.ErrUserDisabled) {
			s.limiter.take(acct, loginFailBurst, loginFailEvery)
			return httpx.E(401, "bad_credentials", err.Error())
		}
		return err
	}
	if err := s.setSession(w, r, u); err != nil {
		return err
	}
	httpx.JSON(w, 200, s.meOf(u))
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.St.DeleteSession(r.Context(), c.Value)
		s.sessions.Delete(c.Value)
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
	if err := httpx.DecodeSmall(r, &in); err != nil {
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
	if err := httpx.DecodeSmall(r, &in); err != nil {
		return err
	}
	u, err := s.St.Redeem(r.Context(), userOf(r).ID, in.Code)
	if err != nil {
		if errors.Is(err, store.ErrCodeInvalid) || errors.Is(err, store.ErrPermanentVIP) {
			return httpx.Bad(err)
		}
		return err
	}
	s.audit(r, "user.redeem", u.Username, in.Code)
	httpx.JSON(w, 200, s.meOf(u))
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

// maxCatalogPage caps paging (deep pages are rarely useful).
const maxCatalogPage = 50

func catalogPage(r *http.Request) int { return min(httpx.Page(r), maxCatalogPage) }

const (
	searchPageSize    = 30
	searchMaxSubjects = 24
)

type searchResp struct {
	Subjects  []subjectOut      `json:"subjects"`
	Torrents  []catalog.Torrent `json:"torrents"`
	Page      int               `json:"page"`
	PageCount int               `json:"page_count"`
	Total     int               `json:"total"`
}

// GET /api/v1/search/{keyword}?page=
// subjects (page 1 only): local works by title/Chinese title/aliases; if none, bgm.tv (and
// TMDB with a key) are asked once and the hits stored as works. torrents: local catalogue
// titles + file names, newest first.
func (s *Server) getSearch(w http.ResponseWriter, r *http.Request) error {
	if err := s.rateLimitN(r, "catalog", 20, 3*time.Second); err != nil {
		return err
	}
	q := strings.TrimSpace(r.PathValue("keyword"))
	if q == "" {
		return httpx.E(400, "bad_request", "请输入搜索关键词")
	}
	if utf8.RuneCountInString(q) > 100 {
		return httpx.E(400, "bad_request", "搜索关键词过长（最多 100 字）")
	}
	ctx := r.Context()
	page := catalogPage(r)
	out := searchResp{Subjects: []subjectOut{}, Page: page}
	if page == 1 {
		works, err := s.Meta.SearchWorksFTS(ctx, q, searchMaxSubjects)
		if err != nil {
			return err
		}
		if len(works) == 0 && utf8.RuneCountInString(q) >= 2 && s.limiter.allow("extsearch:"+ipKey(r), 5, 30*time.Second) {
			if ext, err := s.Cat.SearchSubjectsExternal(ctx, q); err == nil {
				works = ext
			} else {
				s.Log.Info("external subject search failed", "q", q, "err", err)
			}
		}
		for _, wk := range works {
			out.Subjects = append(out.Subjects, s.subjectCard(r, wk, false))
		}
	}
	list, total, err := s.Meta.SearchTorrents(ctx, q, (page-1)*searchPageSize, searchPageSize)
	if err != nil {
		return err
	}
	out.Total = total
	out.PageCount = min(max(1, (total+searchPageSize-1)/searchPageSize), maxCatalogPage)
	out.Torrents = s.withAccess(r, catalog.FromCatalogList(list))
	httpx.JSON(w, 200, out)
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

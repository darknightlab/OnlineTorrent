package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
)

// danmaku collections started per client (stored danmaku is not limited)
const (
	danmakuFetchBurst = 10
	danmakuFetchEvery = 30 * time.Second
)

// sending danmaku, per user: at most one per 3 s, and 30 per 10 minutes
const (
	danmakuSendEvery     = 3 * time.Second
	danmakuSendWindowMax = 30
	danmakuSendWindow    = 10 * time.Minute
	danmakuMaxText       = 100         // runes after trim
	danmakuMaxDuration   = 6 * 60 * 60 // seconds: sanity bound of the reported video duration
	danmakuDurationSlack = 1.0         // seconds a sent time may exceed the reported duration
	danmakuBodyLimit     = 4 << 10     // request body bytes
)

// danmakuItem is one entry of GET …/danmaku: [time, mode, colour, text] plus a trailing 1 for
// the current user's own danmaku.
type danmakuItem struct {
	meta.Comment
	own bool
}

func (d danmakuItem) MarshalJSON() ([]byte, error) {
	if d.own {
		return json.Marshal([]any{d.Time, d.Mode, d.Color, d.Text, 1})
	}
	return d.Comment.MarshalJSON()
}

// GET /api/v1/subjects/{provider}/{id}/episodes/{n}/danmaku → {"items": [[time, mode, colour, text(, 1)], …]}
// (mode: 1 scroll, 4 bottom, 5 top; colour 0xRRGGBB; a trailing 1 marks the caller's own).
// The collected danmaku (official main episodes; collected now if none is stored yet, see
// internal/danmaku) merged with the ones users sent (any episode key, always read fresh).
// No source / platform / user information is returned.
func (s *Server) getDanmaku(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	key, ok := meta.ParseEpisodeKey(r.PathValue("n"))
	if !ok {
		return httpx.E(400, "bad_request", "无效集数")
	}
	key = s.Meta.CanonicalKey(r.Context(), work.Key, key)
	var collected []meta.Comment
	if s.Danmaku != nil {
		mayFetch := func() bool { return s.limiter.allow("danmaku:"+ipKey(r), danmakuFetchBurst, danmakuFetchEvery) }
		collected, err = s.Danmaku.Get(r.Context(), s.BaseCtx, work, key, mayFetch)
		if err != nil {
			return err
		}
	}
	sent, err := s.Meta.UserDanmaku(r.Context(), work.Key, key)
	if err != nil {
		return err
	}
	var me int64 = -1
	if u := userOf(r); u != nil {
		me = u.ID
	}
	items := make([]danmakuItem, 0, len(collected)+len(sent))
	for _, c := range collected {
		items = append(items, danmakuItem{Comment: c})
	}
	for _, u := range sent {
		items = append(items, danmakuItem{Comment: u.Comment, own: u.UserID == me})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Time < items[j].Time })
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len(items) == 0 {
		w.Header().Set("Cache-Control", "no-store") // may be collected later
	} else {
		// the content only changes when danmaku are collected or sent: revalidate by content
		// (private: it depends on the permission and marks the caller's own danmaku)
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:12]) + `"`
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	_, err = w.Write(body)
	return err
}

type sendDanmakuReq struct {
	Time     *float64 `json:"time"`     // seconds in the video
	Duration float64  `json:"duration"` // the video's duration as the client sees it (sanity bound)
	Mode     int      `json:"mode"`     // 1 scroll (default), 4 bottom, 5 top
	Color    *int     `json:"color"`    // 0xRRGGBB, default white
	Text     string   `json:"text"`
}

// validDanmakuText: the trimmed text, or "" when it is empty, too long or has control
// characters.
func validDanmakuText(s string) (string, string) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", "弹幕不能为空"
	}
	if !utf8.ValidString(t) {
		return "", "弹幕包含无效字符"
	}
	if utf8.RuneCountInString(t) > danmakuMaxText {
		return "", "弹幕最多 100 个字"
	}
	for _, r := range t {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", "弹幕不能包含换行或控制字符"
		}
	}
	return t, ""
}

// POST /api/v1/subjects/{provider}/{id}/episodes/{n}/danmaku {time, duration, mode?, color?, text}
// → 201 {"item": [time, mode, colour, text, 1]}. Requires login (route permission, members by
// default). The episode must exist (official or with mapped files).
func (s *Server) postDanmaku(w http.ResponseWriter, r *http.Request) error {
	u := userOf(r)
	if u == nil {
		return httpx.E(http.StatusUnauthorized, "login_required", "请先登录")
	}
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	key, ok := meta.ParseEpisodeKey(r.PathValue("n"))
	if !ok {
		return httpx.E(400, "bad_request", "无效集数")
	}
	key = s.Meta.CanonicalKey(r.Context(), work.Key, key)
	var in sendDanmakuReq
	if err := httpx.DecodeLimit(r, &in, danmakuBodyLimit); err != nil {
		return err
	}
	text, msg := validDanmakuText(in.Text)
	if msg != "" {
		return httpx.E(400, "bad_request", msg)
	}
	if in.Time == nil || math.IsNaN(*in.Time) || math.IsInf(*in.Time, 0) || *in.Time < 0 ||
		math.IsNaN(in.Duration) || in.Duration <= 0 || in.Duration > danmakuMaxDuration || *in.Time > in.Duration+danmakuDurationSlack {
		return httpx.E(400, "bad_request", "弹幕时间无效")
	}
	if in.Mode == 0 {
		in.Mode = 1
	}
	if in.Mode != 1 && in.Mode != 4 && in.Mode != 5 {
		return httpx.E(400, "bad_request", "弹幕模式无效")
	}
	color := 0xFFFFFF
	if in.Color != nil {
		if *in.Color < 0 || *in.Color > 0xFFFFFF {
			return httpx.E(400, "bad_request", "弹幕颜色无效")
		}
		color = *in.Color
	}
	if ok, err := s.episodeExists(r, work, key); err != nil {
		return err
	} else if !ok {
		return httpx.E(404, "not_found", "该集不存在")
	}
	// per user: both limits must have room; then both are charged
	user := "danmaku-send:" + strconv.FormatInt(u.ID, 10)
	window := "danmaku-send-window:" + strconv.FormatInt(u.ID, 10)
	windowEvery := danmakuSendWindow / danmakuSendWindowMax
	if !s.limiter.allow2(user, 1, danmakuSendEvery, window, danmakuSendWindowMax, windowEvery) {
		return httpx.E(http.StatusTooManyRequests, "rate_limited", "发送太频繁，请稍后再试")
	}
	c := meta.Comment{Time: math.Round(*in.Time*100) / 100, Mode: in.Mode, Color: color, Text: text}
	if _, err := s.Meta.AddUserDanmaku(r.Context(), work.Key, key, u.ID, c); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"item": danmakuItem{Comment: c, own: true}})
	return nil
}

// episodeExists: an official episode of the work, or one with mapped files.
func (s *Server) episodeExists(r *http.Request, work *meta.Work, key meta.EpisodeKey) (bool, error) {
	official, err := s.Meta.OfficialEpisodes(r.Context(), work.Key)
	if err != nil {
		return false, err
	}
	for _, o := range official {
		if o.Key == key {
			return true, nil
		}
	}
	files, err := s.Meta.EpisodeFiles(r.Context(), work.Key, key)
	return len(files) > 0, err
}

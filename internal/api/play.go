package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

type playReq struct {
	Magnet    string `json:"magnet"`
	Infohash  string `json:"infohash"`
	TorrentID string `json:"torrent_id"`
}

type playMeta struct {
	AnimeKey string   `json:"anime_key"`
	Title    string   `json:"title"`
	Episode  *float64 `json:"episode"`
}

type playResp struct {
	Access   policy.Access `json:"access"`
	Infohash string        `json:"infohash"`
	Name     string        `json:"name"`
	Magnet   string        `json:"magnet"`
	SeededBy []string      `json:"seeded_by"`
	Webseeds []string      `json:"webseeds"`
	Meta     *playMeta     `json:"meta"`
}

func denyError(d policy.Decision, u *store.User) error {
	switch d.Action {
	case policy.Login:
		return httpx.E(401, "login_required", d.Reason)
	case policy.VIP:
		if u == nil {
			return httpx.E(401, "login_required", "该资源仅限大会员观看，请先登录")
		}
		return httpx.E(402, "vip_required", d.Reason)
	default:
		return httpx.E(451, "blocked", d.Reason)
	}
}

func (s *Server) play(w http.ResponseWriter, r *http.Request) error {
	var in playReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	u := userOf(r)
	if !s.limiter.allow("play:"+httpx.ClientIP(r), 30, 6*time.Second) {
		return httpx.E(429, "rate_limited", "操作过于频繁，请稍后再试")
	}

	var t *catalog.Torrent
	var extraTrackers []string
	switch {
	case in.TorrentID != "":
		var err error
		t, err = s.Cat.Detail(ctx, in.TorrentID)
		if err != nil {
			return httpx.E(404, "not_found", "找不到该种子")
		}
	case in.Magnet != "" || in.Infohash != "":
		src := in.Magnet
		if src == "" {
			src = in.Infohash
		}
		m, err := magnet.Parse(src)
		if err != nil {
			return httpx.Bad(err)
		}
		for _, tr := range m.Trackers {
			if !magnet.IsWebSocket(tr) {
				extraTrackers = append(extraTrackers, tr)
			}
		}
		t = &catalog.Torrent{Infohash: m.Infohash, Title: m.Name, Source: "user"}
		if rec, err := s.St.GetTorrent(ctx, m.Infohash); err == nil && rec != nil {
			if rec.Title != "" {
				t.Title = rec.Title
			}
			t.FileList, t.Source, t.ID = rec.Files, rec.Source, rec.SourceID
			if rec.Team != "" {
				t.Team = &catalog.Ref{Name: rec.Team}
			}
			for _, g := range rec.Tags {
				t.Tags = append(t.Tags, catalog.Ref{Name: g})
			}
		}
	default:
		return httpx.E(400, "bad_request", "需要 magnet、infohash 或 torrent_id")
	}

	d, err := s.Policy.Decide(ctx, u, t.Subject())
	if err != nil {
		return err
	}
	if !d.Allowed {
		s.St.AddPlay(ctx, store.Play{Infohash: t.Infohash, Title: t.Title, IP: httpx.ClientIP(r), Action: d.Action}, uid(u))
		return denyError(d, u)
	}

	var tags []string
	for _, g := range t.Tags {
		tags = append(tags, g.Name)
	}
	team := ""
	if t.Team != nil {
		team = t.Team.Name
	}
	_ = s.St.UpsertTorrent(ctx, store.TorrentRecord{Infohash: t.Infohash, Title: t.Title, Size: t.SizeBytes, Files: t.FileList, Team: team, Tags: tags, Source: t.Source, SourceID: t.ID})

	trackers := s.St.List(store.KeyTrackers)
	timeout := time.Duration(s.St.Num(store.KeyQBAddTimeoutSec)) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	push := s.Pool.Push(ctx, t.Infohash, t.Title, append(append([]string{}, trackers...), extraTrackers...), timeout)
	s.St.AddPlay(ctx, store.Play{Infohash: t.Infohash, Title: t.Title, IP: httpx.ClientIP(r), Action: policy.Allow, SeededBy: strings.Join(push.SeededBy, ",")}, uid(u))
	if len(push.Errors) > 0 {
		s.Log.Warn("push errors", "ih", t.Infohash, "errors", push.Errors)
	}
	// user-submitted torrents: re-check once the file list is known
	if t.Source != "bangumi.moe" && len(t.FileList) == 0 && len(push.SeededBy) > 0 {
		go s.postCheck(t.Infohash, uid(u))
	}

	resp := playResp{
		Access: d.Access(), Infohash: t.Infohash, Name: t.Title,
		Magnet: magnet.Build(t.Infohash, t.Title, trackers), SeededBy: push.SeededBy, Webseeds: []string{},
	}
	if tm, err := s.Meta.GetTorrentMap(ctx, t.Infohash); err == nil {
		for _, f := range tm.Files {
			if f.Work != nil && f.Kind == "episode" {
				title := f.Work.TitleCN
				if title == "" {
					title = f.Work.Title
				}
				resp.Meta = &playMeta{AnimeKey: f.Work.Key, Title: title, Episode: f.Episode}
				break
			}
		}
	}
	httpx.JSON(w, 200, resp)
	return nil
}

// postCheck waits for metadata from qB, records the files, re-evaluates policy and
// removes the torrent if it turns out to be blocked.
func (s *Server) postCheck(ih string, by *int64) {
	ctx, cancel := context.WithTimeout(s.BaseCtx, 3*time.Minute)
	defer cancel()
	var name string
	var files []string
	var sizes []int64
	for i := 0; i < 36; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		n, fs, ok := s.Pool.Metadata(ctx, ih)
		if ok {
			name = n
			for _, f := range fs {
				files = append(files, f.Name)
				sizes = append(sizes, f.Size)
			}
			break
		}
	}
	if len(files) == 0 {
		return
	}
	var total int64
	for _, sz := range sizes {
		total += sz
	}
	_ = s.St.UpsertTorrent(ctx, store.TorrentRecord{Infohash: ih, Title: name, Size: total, Files: files, Source: "user"})
	in := meta.TorrentInput{Infohash: ih, Title: name, Source: "user"}
	for i, f := range files {
		in.Files = append(in.Files, meta.FileInput{Index: i, Path: f, Size: sizes[i]})
	}
	s.Cat.MatchTorrent(in)

	d, err := s.Policy.Requirement(ctx, policy.Subject{Infohash: ih, Title: name, Files: files})
	if err != nil {
		return
	}
	if d.Action == policy.Block {
		s.Log.Info("post-check blocked", "ih", ih, "rule", d.RuleName)
		s.Pool.Remove(ctx, ih)
		_ = s.St.SetVerdict(ctx, nil, store.Verdict{Infohash: ih, Action: policy.Block, Label: d.Label, Note: "自动：" + d.RuleName})
		s.St.Audit(ctx, nil, "policy.autoblock", ih, d.RuleName, "")
		return
	}
	if s.St.Bool(store.KeyAutoReviewUnknown) {
		_ = s.St.AddReview(ctx, ih, name, "auto", "用户提交的种子", by)
	}
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) error {
	if err := s.rateLimit(r, "report"); err != nil {
		return err
	}
	var in struct {
		Infohash string `json:"infohash"`
		Reason   string `json:"reason"`
		Title    string `json:"title"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ih, ok := magnet.NormalizeInfohash(in.Infohash)
	if !ok {
		return httpx.E(400, "bad_request", "infohash 无效")
	}
	reason := strings.TrimSpace(in.Reason)
	if len([]rune(reason)) > 500 {
		reason = string([]rune(reason)[:500])
	}
	title := in.Title
	if rec, err := s.St.GetTorrent(r.Context(), ih); err == nil && rec != nil && rec.Title != "" {
		title = rec.Title
	}
	if err := s.St.AddReview(r.Context(), ih, title, "report", reason, uid(userOf(r))); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	httpx.JSON(w, 200, map[string]any{})
	return nil
}

package meta

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Danmaku storage (migration 13). The fetching lives in internal/danmaku; this file only
// stores the platform ids from bangumi-data (bd_sites) and the comment sets.

// Comment is one danmaku comment. JSON: [time, mode, colour, text].
type Comment struct {
	Time  float64 // seconds from the start of the episode
	Mode  int     // 1 scroll, 4 bottom, 5 top (bilibili codes)
	Color int     // 0xRRGGBB
	Text  string
}

func (c Comment) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{c.Time, c.Mode, c.Color, c.Text})
}

func (c *Comment) UnmarshalJSON(b []byte) error {
	var a []json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 4 {
		return fmt.Errorf("danmaku comment: want [time, mode, colour, text]")
	}
	if err := json.Unmarshal(a[0], &c.Time); err != nil {
		return err
	}
	if err := json.Unmarshal(a[1], &c.Mode); err != nil {
		return err
	}
	if err := json.Unmarshal(a[2], &c.Color); err != nil {
		return err
	}
	return json.Unmarshal(a[3], &c.Text)
}

// DanmakuSet is the stored comments of one episode from one platform.
type DanmakuSet struct {
	Platform  string
	Ref       string
	Comments  []Comment
	FetchedAt string
}

// SiteID is a streaming-site id of a bangumi subject (bangumi-data).
type SiteID struct {
	Site  string // bilibili | bilibili_hk_mo_tw | bilibili_hk_mo | bilibili_tw | gamer | gamer_hk
	ExtID string
}

// SiteIDs lists the danmaku-relevant site ids of a bangumi subject.
func (s *Store) SiteIDs(ctx context.Context, bgmID int64) ([]SiteID, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT site, ext_id FROM bd_sites WHERE bgm_id = ? ORDER BY site, ext_id`, bgmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteID
	for rows.Next() {
		var x SiteID
		if err := rows.Scan(&x.Site, &x.ExtID); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SubjectsWithSite lists the bangumi subjects whose site id (any of sites) is extID: several
// subjects can share one platform listing (e.g. both cours of a split-cour show on one
// Bahamut page).
func (s *Store) SubjectsWithSite(ctx context.Context, sites []string, extID string) ([]int64, error) {
	if len(sites) == 0 {
		return nil, nil
	}
	args := []any{extID}
	for _, x := range sites {
		args = append(args, x)
	}
	rows, err := s.R.QueryContext(ctx, `SELECT DISTINCT bgm_id FROM bd_sites WHERE ext_id = ? AND site IN (`+strings.TrimSuffix(strings.Repeat("?,", len(sites)), ",")+`) ORDER BY bgm_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// OfficialMainSorts lists the official main-episode numbers (bangumi sort) of a work.
func (s *Store) OfficialMainSorts(ctx context.Context, workKey string) ([]float64, error) {
	eps, err := s.OfficialEpisodes(ctx, workKey)
	if err != nil {
		return nil, err
	}
	var out []float64
	for _, e := range eps {
		if e.Key.Kind == KeyMain {
			out = append(out, e.Key.Number)
		}
	}
	return out, nil
}

// BgmID returns the bangumi subject id of a work ("bgm:{id}" keys), 0 if none.
func BgmID(w *Work) int64 {
	if id, ok := strings.CutPrefix(w.Key, "bgm:"); ok {
		n, _ := strconv.ParseInt(id, 10, 64)
		return n
	}
	n, _ := strconv.ParseInt(w.External["bgm"], 10, 64)
	return n
}

// Danmaku returns the stored comment sets of an episode (any platform).
func (s *Store) Danmaku(ctx context.Context, workKey string, ep EpisodeKey) ([]DanmakuSet, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT platform, ref, data, fetched_at FROM danmaku WHERE work_key = ? AND episode = ? ORDER BY platform`, workKey, ep.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DanmakuSet
	for rows.Next() {
		var d DanmakuSet
		var blob []byte
		if err := rows.Scan(&d.Platform, &d.Ref, &blob, &d.FetchedAt); err != nil {
			return nil, err
		}
		zr, err := gzip.NewReader(bytes.NewReader(blob))
		if err != nil {
			return nil, fmt.Errorf("danmaku %s %s %s: %w", workKey, ep, d.Platform, err)
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("danmaku %s %s %s: %w", workKey, ep, d.Platform, err)
		}
		if err := json.Unmarshal(raw, &d.Comments); err != nil {
			return nil, fmt.Errorf("danmaku %s %s %s: %w", workKey, ep, d.Platform, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// HasDanmaku reports whether an episode has any stored comments.
func (s *Store) HasDanmaku(ctx context.Context, workKey string, ep EpisodeKey) (bool, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM danmaku WHERE work_key = ? AND episode = ?`, workKey, ep.String()).Scan(&n)
	return n > 0, err
}

// SaveDanmaku stores the comments of an episode from one platform. A stored set is never
// replaced (false = one was already there). Empty sets are refused (a failed fetch).
func (s *Store) SaveDanmaku(ctx context.Context, workKey string, ep EpisodeKey, platform, ref string, comments []Comment) (bool, error) {
	if len(comments) == 0 {
		return false, fmt.Errorf("danmaku: refusing to store an empty set")
	}
	raw, err := json.Marshal(comments)
	if err != nil {
		return false, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return false, err
	}
	if err := zw.Close(); err != nil {
		return false, err
	}
	res, err := s.W.ExecContext(ctx, `INSERT INTO danmaku(work_key,episode,platform,ref,count,data,fetched_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		workKey, ep.String(), platform, ref, len(comments), buf.Bytes(), store.Now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UserComment is a danmaku sent by a user of the site.
type UserComment struct {
	Comment
	ID        int64
	UserID    int64
	CreatedAt string
}

// AddUserDanmaku stores a danmaku sent by a user (validated by the caller).
func (s *Store) AddUserDanmaku(ctx context.Context, workKey string, ep EpisodeKey, userID int64, c Comment) (int64, error) {
	res, err := s.W.ExecContext(ctx, `INSERT INTO user_danmaku(work_key,episode,user_id,time,mode,color,text,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		workKey, ep.String(), userID, c.Time, c.Mode, c.Color, c.Text, store.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UserDanmaku lists the danmaku users sent for an episode (oldest first).
func (s *Store) UserDanmaku(ctx context.Context, workKey string, ep EpisodeKey) ([]UserComment, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id,user_id,time,mode,color,text,created_at FROM user_danmaku WHERE work_key = ? AND episode = ? ORDER BY id`, workKey, ep.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserComment
	for rows.Next() {
		var u UserComment
		if err := rows.Scan(&u.ID, &u.UserID, &u.Time, &u.Mode, &u.Color, &u.Text, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

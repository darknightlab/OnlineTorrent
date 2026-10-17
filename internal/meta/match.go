package meta

import (
	"context"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Match records a torrent and (re)computes automatic mappings for its video files.
// Rows with status verified/rejected are never overwritten.
func (s *Store) Match(ctx context.Context, t TorrentInput) error {
	if t.Infohash == "" {
		return nil
	}
	now := store.Now()
	if _, err := s.W.ExecContext(ctx, `INSERT INTO torrents(infohash,title,source,source_id,team,published_at,updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(infohash) DO UPDATE SET title=CASE WHEN excluded.title!='' THEN excluded.title ELSE title END,
		source=CASE WHEN excluded.source!='' THEN excluded.source ELSE source END,
		source_id=CASE WHEN excluded.source_id!='' THEN excluded.source_id ELSE source_id END,
		team=CASE WHEN excluded.team!='' THEN excluded.team ELSE team END,
		published_at=CASE WHEN excluded.published_at!='' THEN excluded.published_at ELSE published_at END,
		updated_at=excluded.updated_at`,
		t.Infohash, t.Title, t.Source, t.SourceID, t.Team, t.PublishedAt, now); err != nil {
		return err
	}

	work, method, wconf := s.resolveWork(ctx, t)

	var videos []FileInput
	for _, f := range t.Files {
		if IsVideo(f.Path) {
			videos = append(videos, f)
		}
	}
	for _, f := range videos {
		p := ParseEpisode(f.Path)
		if p.Episode == nil && len(videos) == 1 && p.Kind == "other" {
			p = ParseEpisode(t.Title + ".mkv")
		}
		kind := p.Kind
		if kind == "other" {
			kind = "episode"
			if p.Episode == nil && len(videos) == 1 && work != nil && strings.EqualFold(work.Type, "movie") {
				one := 1.0
				p.Episode, p.Confidence = &one, 0.7
			}
		}
		var wk any
		conf := 0.0
		m := method
		if work != nil {
			wk = work.Key
			conf = wconf
			if p.Episode != nil {
				conf = wconf * max(p.Confidence, 0.5)
				m += "+name"
			}
		}
		if _, err := s.W.ExecContext(ctx, `INSERT INTO torrent_files(infohash,file_index,path,size_bytes,kind,work_key,episode,confidence,status,method,updated_at) VALUES(?,?,?,?,?,?,?,?, 'auto', ?, ?)
			ON CONFLICT(infohash,file_index) DO UPDATE SET path=excluded.path, size_bytes=CASE WHEN excluded.size_bytes>0 THEN excluded.size_bytes ELSE size_bytes END,
			kind=excluded.kind, work_key=excluded.work_key, episode=excluded.episode, confidence=excluded.confidence, method=excluded.method, updated_at=excluded.updated_at
			WHERE torrent_files.status = 'auto'`,
			t.Infohash, f.Index, f.Path, f.Size, kind, wk, p.Episode, conf, m, now); err != nil {
			return err
		}
	}
	return nil
}

// resolveWork finds the work: bangumi.moe bangumi tag (via bangumi-data) first,
// then a conservative alias substring match among recent works.
func (s *Store) resolveWork(ctx context.Context, t TorrentInput) (*Work, string, float64) {
	for _, id := range t.MoeTagIDs {
		if w, err := s.WorkByExternal(ctx, "bangumi_moe", id); err == nil {
			return w, "moe_tag", 0.95
		}
	}
	title := strings.ToLower(t.Title)
	if title == "" {
		return nil, "", 0
	}
	from := time.Now().AddDate(-1, -6, 0).UTC().Format(time.RFC3339)
	to := time.Now().AddDate(0, 1, 0).UTC().Format(time.RFC3339)
	works, err := s.WorksBetween(ctx, from, to)
	if err != nil {
		return nil, "", 0
	}
	var best *Work
	bestLen := 0
	for _, w := range works {
		for _, a := range append([]string{w.Title, w.TitleCN}, w.Aliases...) {
			a = strings.ToLower(strings.TrimSpace(a))
			if len([]rune(a)) < 4 || len(a) <= bestLen {
				continue
			}
			if strings.Contains(title, a) {
				best, bestLen = w, len(a)
			}
		}
	}
	if best != nil {
		return best, "alias", 0.7
	}
	return nil, "", 0
}

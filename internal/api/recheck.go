package api

import (
	"context"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// RecheckInterval is how often every torrent in our qB category is re-evaluated.
const RecheckInterval = 10 * time.Minute

// RunRecheck periodically re-evaluates every torrent in our qB categories against the
// policy using its real name and file list (from qB metadata), removing blocked or
// oversized ones (size = the files qB downloads, not the whole torrent). Blocks until ctx is
// done.
func (s *Server) RunRecheck(ctx context.Context) {
	first := time.NewTimer(time.Minute) // let the pool health check run first
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	t := time.NewTicker(RecheckInterval)
	defer t.Stop()
	for {
		s.recheck(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) recheck(ctx context.Context) {
	gen := s.Policy.Generation()
	seen := map[string]bool{}
	for _, sv := range s.Pool.Servers() {
		if !sv.Cfg.Enabled || !sv.Status().Online {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ts, err := sv.Client.Info(cctx, sv.Cfg.Category)
		cancel()
		if err != nil {
			s.Log.Warn("recheck: list", "server", sv.Cfg.Name, "err", err)
			continue
		}
		for _, t := range ts {
			if ctx.Err() != nil {
				return
			}
			if !t.HasMetadata || seen[t.Hash] || t.Category != sv.Cfg.Category {
				continue
			}
			seen[t.Hash] = true
			s.recheckMu.Lock()
			g, done := s.checked[t.Hash]
			s.recheckMu.Unlock()
			if done && g == gen && !s.oversized(ctx, t) {
				continue
			}
			if _, busy := s.postChecks.Load(t.Hash); busy {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			files, err := sv.Client.Files(cctx, t.Hash)
			if err == nil && len(files) > 0 {
				s.checkSeeded(cctx, gen, t.Hash, t.Name, files, nil, false)
			}
			cancel()
		}
	}
	// forget torrents that are gone from qB
	s.recheckMu.Lock()
	for ih := range s.checked {
		if !seen[ih] {
			delete(s.checked, ih)
		}
	}
	s.recheckMu.Unlock()
}

// oversized reports whether an untrusted torrent exceeds the push size limit (the limit is a
// setting, so this is checked every cycle, not cached by policy generation). qB's size is
// the wanted files' size; a torrent whose file selection is still pending isn't judged yet.
func (s *Server) oversized(ctx context.Context, t qb.Torrent) bool {
	limit := s.maxPushBytes()
	if limit <= 0 || t.Size <= limit || t.HasTag(qb.PendingTag) {
		return false
	}
	rec, _ := s.St.GetTorrent(ctx, t.Hash)
	return rec == nil || !rec.Trusted
}

// checkSeeded evaluates a torrent that qB has metadata for: records the real name/files,
// queues mapping, and removes it from the pool if policy blocks it (recording an automatic
// verdict) or if it's an untrusted torrent whose wanted files (the ones some server
// downloads, Pool.Wanted) exceed the size limit. gen is the policy generation read before
// evaluation; the result is cached under it.
func (s *Server) checkSeeded(ctx context.Context, gen uint64, ih, name string, files []qb.File, by *int64, review bool) {
	defer func() {
		s.recheckMu.Lock()
		s.checked[ih] = gen
		s.recheckMu.Unlock()
	}()
	paths := make([]string, len(files))
	var total int64
	for i, f := range files {
		paths[i] = f.Name
		total += f.Size
	}
	rec, _ := s.St.GetTorrent(ctx, ih)
	trusted := rec != nil && rec.Trusted

	sub := policy.Subject{Infohash: ih, Title: name, Files: paths}
	if rec != nil {
		sub.Team, sub.Tags = rec.Team, rec.Tags
		if trusted && rec.Title != "" {
			sub.Title = rec.Title
		}
	}
	if !trusted {
		// the qB metadata name is the trusted title for user torrents (H3)
		_ = s.St.UpsertTorrent(ctx, store.TorrentRecord{Infohash: ih, Title: name, Size: total, Files: paths, Source: "user"})
	}
	// ingest + scrape into the catalogue (no-op when it already has the file list)
	if s.Crawl != nil {
		if err := s.Crawl.IngestQBMetadata(ctx, ih, name, files); err != nil {
			s.Log.Warn("ingest qB metadata", "ih", ih, "err", err)
		}
	}

	d, err := s.Policy.Requirement(ctx, sub)
	if err != nil {
		return
	}
	if d.Action == policy.Block {
		s.Log.Info("recheck: blocked, removing", "ih", ih, "rule", d.RuleName, "source", d.Source)
		s.Pool.Remove(ctx, ih)
		if d.Source != "verdict" {
			_ = s.St.SetVerdict(ctx, nil, store.Verdict{Infohash: ih, Action: policy.Block, Label: d.Label, Note: "自动屏蔽: " + d.RuleName})
			s.St.Audit(ctx, nil, "policy.autoblock", ih, d.RuleName, "")
		}
		return
	}
	if limit := s.maxPushBytes(); !trusted && limit > 0 && total > limit && s.wantedFilesSize(ctx, ih, files) > limit {
		s.Log.Info("recheck: too large, removing", "ih", ih, "size", total)
		s.Pool.Remove(ctx, ih)
		s.St.Audit(ctx, nil, "qb.too_large", ih, name, "")
		return
	}
	if review && !trusted && s.St.Bool(store.KeyAutoReviewUnknown) {
		_ = s.St.AddReview(ctx, ih, name, "auto", "用户提交的种子", by)
	}
	// keep the .torrent (HTTP metadata for browsers) so it survives qB deletion
	if s.Crawl != nil {
		s.Crawl.SaveExport(ctx, ih)
	}
}

// wantedFilesSize is the size of the files of ih that some server downloads (or is about to).
func (s *Server) wantedFilesSize(ctx context.Context, ih string, files []qb.File) int64 {
	wanted := s.Pool.Wanted(ctx, ih)
	sf := make([]seedFile, len(files))
	for i, f := range files {
		sf[i] = seedFile{Path: f.Name, Size: f.Size}
	}
	return wantedSize(sf, wanted)
}

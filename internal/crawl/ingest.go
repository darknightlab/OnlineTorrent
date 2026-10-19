package crawl

import (
	"context"
	"errors"
	"fmt"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// ingestItems handles the unseen items of one run. It returns how many torrents were newly
// added to the catalogue (ready or queued for metadata), whether every item was handled,
// and the joined item errors.
func (c *Crawler) ingestItems(ctx context.Context, src store.Source, items []FeedItem) (int, bool, error) {
	guids := make([]string, len(items))
	for i, it := range items {
		guids[i] = it.GUID
	}
	seen, err := c.St.SeenItems(ctx, src.ID, guids)
	if err != nil {
		return 0, false, err
	}
	n, downloads := 0, 0
	complete := true
	var errs []error
	for _, it := range items {
		if ctx.Err() != nil {
			return n, false, errors.Join(append(errs, ctx.Err())...)
		}
		if seen[it.GUID] {
			continue
		}
		if it.Infohash == "" && it.TorrentURL != "" && downloads >= maxDownloadsPerRun {
			complete = false
			continue
		}
		added, ih, downloaded, err := c.ingestItem(ctx, src, it)
		if downloaded {
			downloads++
		}
		if ih != "" && (it.Seeders != nil || it.Leechers != nil) {
			if err := c.Meta.PutSwarmInitial(ctx, ih, it.Seeders, it.Leechers, it.Completed); err != nil {
				c.Log.Warn("crawl: initial swarm stats", "ih", ih, "err", err)
			}
		}
		if err != nil {
			complete = false
			if len(errs) < 5 {
				errs = append(errs, fmt.Errorf("%s: %w", trunc(it.Title, 60), err))
			}
			continue
		}
		if added {
			n++
		}
		if err := c.St.MarkItemSeen(ctx, src.ID, it.GUID, ih); err != nil {
			return n, false, err
		}
	}
	return n, complete, errors.Join(errs...)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// baseInput is the catalogue info a feed item contributes.
func baseInput(src store.Source, it FeedItem) meta.TorrentInput {
	if it.moe != nil {
		in := it.moe.MatchInput()
		in.Source, in.SourceKind, in.Trusted = src.Name, src.Kind, true
		return in
	}
	return meta.TorrentInput{Infohash: it.Infohash, Title: store.Trunc(it.Title, store.MaxTitleBytes), Source: src.Name, SourceKind: src.Kind,
		SourceID: store.Trunc(it.GUID, 200), Trusted: true, Team: it.Team, PublishedAt: it.PublishedAt, PageURL: it.PageURL, Size: it.Size}
}

// ingestItem adds one feed item to the catalogue: from the source's own file list
// (bangumi.moe), from the downloaded .torrent, or — magnet only — as a pending torrent
// with a qB metadata job. Already known infohashes are skipped, except that a bangumi.moe
// bangumi.moe item still enriches an existing entry (tags, team, trust) once.
func (c *Crawler) ingestItem(ctx context.Context, src store.Source, it FeedItem) (added bool, ih string, downloaded bool, err error) {
	in := baseInput(src, it)
	ih = it.Infohash
	if ih != "" {
		status, known := c.Meta.KnownTorrent(ctx, ih)
		if known && it.moe == nil {
			return false, ih, false, nil
		}
		if it.moe != nil && len(in.Files) > 0 {
			return !known, ih, false, c.Meta.Match(ctx, in)
		}
		if known && status == "ready" {
			// moe item without files: just merge the listing info
			return false, ih, false, c.Meta.UpsertPending(ctx, in)
		}
	}
	if it.TorrentURL != "" && (ih == "" || it.moe == nil) {
		mi, raw, derr := c.downloadTorrent(ctx, src, it.TorrentURL)
		downloaded = true
		if derr == nil {
			if ih != "" && mi.Infohash != ih {
				c.Log.Warn("crawl: feed infohash differs from .torrent", "source", src.Name, "feed", ih, "torrent", mi.Infohash)
			}
			ih = mi.Infohash
			// keep the raw file: browsers fetch it as metadata (magnet xs=)
			if err := c.Meta.PutMetainfo(ctx, ih, raw, meta.MetainfoCrawl); err != nil {
				c.Log.Warn("crawl: store .torrent", "source", src.Name, "ih", ih, "err", err)
			}
			if status, known := c.Meta.KnownTorrent(ctx, ih); known && status == "ready" {
				return false, ih, true, nil
			}
			in.Infohash = ih
			if in.Title == "" {
				in.Title = mi.Name
			}
			in.Files = in.Files[:0]
			for i, f := range mi.Files {
				in.Files = append(in.Files, meta.FileInput{Index: i, Path: f.Path, Size: f.Size})
			}
			in.Size = mi.Size
			return true, ih, true, c.Meta.Match(ctx, in)
		}
		if ih == "" {
			return false, "", true, derr
		}
		c.Log.Info("crawl: .torrent download failed, using the metadata queue", "source", src.Name, "ih", ih, "err", derr)
	}
	if ih == "" {
		return false, "", downloaded, errors.New("no infohash")
	}
	// magnet only: pending entry + qB metadata job
	in.Infohash = ih
	in.Files = nil
	if err := c.Meta.UpsertPending(ctx, in); err != nil {
		return false, ih, downloaded, err
	}
	sid := src.ID
	if _, err := c.St.EnqueueMetadataJob(ctx, ih, &sid, it.Trackers); err != nil {
		return false, ih, downloaded, err
	}
	c.queue.wake()
	return true, ih, downloaded, nil
}

// downloadTorrent fetches and parses a .torrent, returning the raw bytes too.
func (c *Crawler) downloadTorrent(ctx context.Context, src store.Source, u string) (*Metainfo, []byte, error) {
	resp, err := c.Fetch.Get(ctx, u, GetOptions{MaxBytes: MaxTorrentBytes, ProxyDisabled: src.ProxyDisabled, Accept: "application/x-bittorrent, */*;q=0.5"})
	if err != nil {
		return nil, nil, fmt.Errorf("download .torrent: %w", err)
	}
	mi, err := ParseTorrent(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("parse .torrent: %w", err)
	}
	return mi, resp.Body, nil
}

// IngestQBMetadata records the real file list qB reported for a torrent (seed watcher /
// metadata queue). An existing catalogue entry keeps its crawl info; unknown torrents
// become user submissions. No-op if the entry is already complete.
func (c *Crawler) IngestQBMetadata(ctx context.Context, ih, name string, files []qb.File) error {
	in, ok := c.Meta.GetTorrentInput(ctx, ih)
	if ok {
		if status, _ := c.Meta.KnownTorrent(ctx, ih); status == "ready" {
			return nil
		}
	} else {
		in = meta.TorrentInput{Infohash: ih, Title: name, Source: "user", SourceKind: "user"}
	}
	if in.Title == "" {
		in.Title = name
	}
	in.Files = nil
	for _, f := range files {
		in.Files = append(in.Files, meta.FileInput{Index: f.Index, Path: f.Name, Size: f.Size})
	}
	if len(in.Files) == 0 {
		return nil
	}
	return c.Meta.Match(ctx, in)
}

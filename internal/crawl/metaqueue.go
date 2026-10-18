package crawl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// qB metadata queue: a magnet-only feed item is added to one qB server in the category
// "<category>-meta" with stopCondition=MetadataReceived (qB stops it as soon as the
// metadata arrives, nothing is downloaded), its file list is read and ingested, then the
// torrent is deleted again. Jobs live in SQLite (metadata_jobs) so a restart resumes them.
//
// Deletion safety: we only delete a torrent we added ourselves (job.added on that server)
// and only while it is still in the meta category — a member's seed request in the meantime
// moves it to the seeding category (qb.Pool.Push → adoptMeta), and torrents that were in qB
// before (any category) are only read, never touched.

const (
	queueConcurrency = 3
	jobTimeout       = 2 * time.Minute
	jobMaxAttempts   = 3
	jobPoll          = 3 * time.Second
	queueTick        = 10 * time.Second
	noServerDelay    = 5 * time.Minute
)

type metaQueue struct {
	wakeCh  chan struct{}
	sem     chan struct{}
	running map[string]bool
}

func (q *metaQueue) init() {
	q.wakeCh = make(chan struct{}, 1)
	q.sem = make(chan struct{}, queueConcurrency)
	q.running = map[string]bool{}
}

func (q *metaQueue) wake() {
	select {
	case q.wakeCh <- struct{}{}:
	default:
	}
}

// JobActive reports whether a metadata job for ih is pending or running.
func (c *Crawler) JobActive(ctx context.Context, ih string) bool {
	j, err := c.St.GetMetadataJob(ctx, ih)
	return err == nil && j != nil && (j.Status == "pending" || j.Status == "running")
}

func (c *Crawler) runQueue(ctx context.Context) {
	if err := c.St.ResetRunningJobs(ctx); err != nil {
		c.Log.Warn("metadata queue: reset", "err", err)
	}
	// give the qB health check a moment, then sweep leftovers once an hour
	sweepAt := time.Now().Add(time.Minute)
	t := time.NewTicker(queueTick)
	defer t.Stop()
	for {
		if time.Now().After(sweepAt) {
			c.sweepMeta(ctx)
			sweepAt = time.Now().Add(time.Hour)
		}
		c.dispatchJobs(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.queue.wakeCh:
		}
	}
}

func (c *Crawler) dispatchJobs(ctx context.Context) {
	free := cap(c.queue.sem) - len(c.queue.sem)
	if free <= 0 {
		return
	}
	jobs, err := c.St.DueMetadataJobs(ctx, free+len(c.queue.sem))
	if err != nil {
		return
	}
	for _, j := range jobs {
		c.mu.Lock()
		busy := c.queue.running[j.Infohash]
		if !busy {
			c.queue.running[j.Infohash] = true
		}
		c.mu.Unlock()
		if busy {
			continue
		}
		select {
		case c.queue.sem <- struct{}{}:
		default:
			c.mu.Lock()
			delete(c.queue.running, j.Infohash)
			c.mu.Unlock()
			return
		}
		c.wg.Go(func() {
			defer func() {
				<-c.queue.sem
				c.mu.Lock()
				delete(c.queue.running, j.Infohash)
				c.mu.Unlock()
				c.queue.wake()
			}()
			c.processJob(ctx, j)
		})
	}
}

var errNoServer = errors.New("没有在线的 qBittorrent 服务器")

// pickServer prefers the server the job already used (its torrent may still be there).
func (c *Crawler) pickServer(j store.MetadataJob) *qb.Server {
	var first *qb.Server
	for _, s := range c.Pool.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		if j.ServerID != nil && s.Cfg.ID == *j.ServerID {
			return s
		}
		if first == nil {
			first = s
		}
	}
	return first
}

func (c *Crawler) processJob(ctx context.Context, j store.MetadataJob) {
	ih := j.Infohash
	if status, ok := c.Meta.KnownTorrent(ctx, ih); ok && status == "ready" {
		// metadata arrived another way (seed watcher, a .torrent from another source)
		_ = c.St.FinishMetadataJob(ctx, ih, nil, 0, jobMaxAttempts)
		c.cleanupJob(ctx, j)
		return
	}
	sv := c.pickServer(j)
	if sv == nil {
		// not the torrent's fault: retry later without spending an attempt
		_ = c.St.DeferMetadataJob(ctx, ih, noServerDelay, errNoServer.Error())
		return
	}
	_ = c.St.StartMetadataJob(ctx, ih)
	jctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	added := j.Added && j.ServerID != nil && *j.ServerID == sv.Cfg.ID
	err := c.fetchMetadata(jctx, sv, j, &added)
	if ctx.Err() != nil {
		return // shutting down: the job stays "running" and is reset on the next start
	}
	if added {
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		c.deleteIfOurs(dctx, sv, ih)
		dcancel()
	}
	if err != nil {
		retry := time.Duration(j.Attempts+1) * 5 * time.Minute
		c.Log.Info("metadata job failed", "ih", ih, "server", sv.Cfg.Name, "attempt", j.Attempts+1, "err", err)
		_ = c.St.FinishMetadataJob(context.WithoutCancel(ctx), ih, err, retry, jobMaxAttempts)
		return
	}
	c.Log.Info("metadata job done", "ih", ih, "server", sv.Cfg.Name)
	_ = c.St.FinishMetadataJob(context.WithoutCancel(ctx), ih, nil, 0, jobMaxAttempts)
	if c.Cat != nil {
		c.Cat.RefreshLatest(ctx)
	}
}

// fetchMetadata adds the torrent (unless present), waits for the metadata and ingests it.
func (c *Crawler) fetchMetadata(ctx context.Context, sv *qb.Server, j store.MetadataJob, added *bool) error {
	ih := j.Infohash
	ts, err := sv.Client.Info(ctx, "", ih)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		name := ""
		if in, ok := c.Meta.GetTorrentInput(ctx, ih); ok {
			name = in.Title
		}
		uri := magnet.Build(ih, name, j.Trackers)
		if err := sv.Client.Add(ctx, qb.AddParams{URLs: uri, Category: sv.MetaCategory(), StopCondition: "MetadataReceived"}); err != nil {
			return fmt.Errorf("qB add: %w", err)
		}
		*added = true
		if err := c.St.SetMetadataJobServer(ctx, ih, sv.Cfg.ID, true); err != nil {
			return err
		}
	}
	for {
		ts, err := sv.Client.Info(ctx, "", ih)
		if err == nil && len(ts) > 0 && ts[0].HasMetadata {
			files, err := sv.Client.Files(ctx, ih)
			if err == nil && len(files) > 0 {
				if err := c.IngestQBMetadata(ctx, ih, ts[0].Name, files); err != nil {
					return err
				}
				// keep the .torrent before the job deletes the torrent from qB
				c.saveExport(ctx, sv.Client, ih)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("等待元数据超时")
			}
			return ctx.Err()
		case <-time.After(jobPoll):
		}
	}
}

// deleteIfOurs removes ih from sv only if it is still in the meta category.
func (c *Crawler) deleteIfOurs(ctx context.Context, sv *qb.Server, ih string) {
	ts, err := sv.Client.Info(ctx, "", ih)
	if err != nil || len(ts) == 0 || ts[0].Category != sv.MetaCategory() {
		return
	}
	if err := sv.Client.Delete(ctx, []string{ih}); err != nil {
		c.Log.Warn("metadata job: delete", "ih", ih, "server", sv.Cfg.Name, "err", err)
	}
}

func (c *Crawler) cleanupJob(ctx context.Context, j store.MetadataJob) {
	if !j.Added || j.ServerID == nil {
		return
	}
	if sv := c.Pool.Get(*j.ServerID); sv != nil && sv.Status().Online {
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		c.deleteIfOurs(dctx, sv, j.Infohash)
	}
}

// sweepMeta deletes leftovers in the meta categories: torrents a finished job added on
// that server (e.g. a delete failed or the process died between add and delete).
func (c *Crawler) sweepMeta(ctx context.Context) {
	for _, sv := range c.Pool.Servers() {
		if !sv.Cfg.Enabled || !sv.Status().Online {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ts, err := sv.Client.Info(cctx, sv.MetaCategory())
		cancel()
		if err != nil {
			continue
		}
		for _, t := range ts {
			if t.Category != sv.MetaCategory() || !c.St.MetadataJobAdded(ctx, t.Hash, sv.Cfg.ID) {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := sv.Client.Delete(cctx, []string{t.Hash}); err == nil {
				c.Log.Info("metadata queue: removed leftover", "ih", t.Hash, "server", sv.Cfg.Name)
			}
			cancel()
		}
	}
}

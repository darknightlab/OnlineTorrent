// Command otd runs the onlinetorrent server.
//
//	otd [serve] -db /data/otd.db -meta-db /data/meta.db -addr 0.0.0.0:80
//	otd user -db /data/otd.db -username NAME -password PW [-role admin]   (create or reset)
//	otd edge                                                              (edge node, configured by env, see edgeCmd)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/api"
	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/crawl"
	"github.com/darknightlab/onlinetorrent/internal/danmaku"
	"github.com/darknightlab/onlinetorrent/internal/edge"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/swarm"
	"github.com/darknightlab/onlinetorrent/web"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "user":
		err = userCmd(args)
	case "edge":
		err = edgeCmd(args)
	default:
		err = fmt.Errorf("unknown command %q (serve|user|edge)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "otd.db", "main SQLite database")
	metaPath := fs.String("meta-db", "meta.db", "mapping (meta) SQLite database")
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	_ = fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ms, err := meta.Open(*metaPath)
	if err != nil {
		return err
	}
	defer ms.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// every background goroutine is tracked so the DBs are only closed after they exit
	var wg sync.WaitGroup
	// one-off: re-classify stored files (specials keep their number, see meta.Reclassify)
	wg.Add(1)
	go func() { defer wg.Done(); ms.ReclassifyOnce(ctx, log) }()
	pool := qb.NewPool(st, log)
	if err := pool.Reload(ctx); err != nil {
		return err
	}
	wg.Go(func() { pool.Run(ctx) })

	cat := catalog.New(st, ms, log)
	cat.Run(ctx)

	// crawl sources (RSS / bangumi.moe) + the qB metadata queue fill the local catalogue
	cr := crawl.New(st, ms, cat, pool, log)
	wg.Go(func() { cr.Run(ctx) })

	wg.Go(func() { maintenance(ctx, st, log) })

	// tracker health + swarm statistics (scrapes; on-demand refresh from the API)
	sw := swarm.New(st, ms, pool, log)
	sw.Run(ctx)

	if n, _ := st.UserCount(ctx); n == 0 {
		log.Warn("no users yet; create an admin with: otd user -db <db> -username admin -password <pw> -role admin")
	}

	// decision model (Cloudflare Clef) for files the rules cannot map with certainty
	cl := &clef.Service{St: st, Meta: ms, Log: log}
	wg.Go(func() { cl.Run(ctx) })

	// danmaku: collected on demand by the danmu_api sidecar (setting danmaku.api_url); the
	// platform episode lists use the upstream client
	dm := &danmaku.Service{St: st, Meta: ms, Log: log, HTTP: cat.HTTPClient}

	srv := &api.Server{St: st, Meta: ms, Cat: cat, Crawl: cr, Pool: pool, Swarm: sw, Clef: cl, Danmaku: dm, Policy: policy.New(st), Log: log, WebFS: web.FS(), BaseCtx: ctx}
	// edge nodes connect to /api/v1/edge/connect (token-signing key is created on first start)
	if _, err := srv.NewEdgeHub(); err != nil {
		return err
	}
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
	wg.Go(func() { srv.RunRecheck(ctx) })

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hs.Shutdown(sctx); err != nil {
			log.Warn("http shutdown", "err", err)
		}
	}()
	log.Info("listening", "addr", *addr, "frontend", srv.WebFS != nil)
	serveErr := hs.ListenAndServe()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	// stop everything (also when ListenAndServe failed) and wait before the deferred DB closes
	stop()
	<-shutdownDone
	wg.Wait()
	cat.Wait()
	cr.Wait()
	sw.Wait()
	srv.Wait()
	log.Info("shutdown complete")
	return serveErr
}

// maintenance prunes expired sessions hourly and applies log retention daily.
func maintenance(ctx context.Context, st *store.Store, log *slog.Logger) {
	retention := func() {
		if n, err := st.Retention(ctx); err != nil {
			log.Warn("retention", "err", err)
		} else if n > 0 {
			log.Info("retention: pruned old rows", "rows", n)
		}
	}
	retention()
	hourly := time.NewTicker(time.Hour)
	daily := time.NewTicker(24 * time.Hour)
	defer hourly.Stop()
	defer daily.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hourly.C:
			st.PruneSessions(ctx)
		case <-daily.C:
			retention()
		}
	}
}

func userCmd(args []string) error {
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dbPath := fs.String("db", "otd.db", "main SQLite database")
	name := fs.String("username", "", "username")
	pw := fs.String("password", "", "password (≥ 8 chars)")
	role := fs.String("role", "admin", "member|vip|admin")
	_ = fs.Parse(args)
	if *name == "" || *pw == "" {
		return errors.New("-username and -password are required")
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	u, err := st.CreateUser(ctx, store.CreateUserParams{Username: *name, Password: *pw, Role: *role})
	if errors.Is(err, store.ErrTaken) {
		users, _, lerr := st.ListUsers(ctx, *name, 1, 50)
		if lerr != nil {
			return lerr
		}
		for _, x := range users {
			if x.Username == *name {
				if _, err := st.AdminUpdateUser(ctx, x.ID, store.UpdateUser{Role: role, Password: pw}); err != nil {
					return err
				}
				fmt.Printf("updated user %s (id %d) role=%s\n", x.Username, x.ID, *role)
				return nil
			}
		}
		return err
	}
	if err != nil {
		return err
	}
	fmt.Printf("created user %s (id %d) role=%s\n", u.Username, u.ID, u.Role)
	return nil
}

// edgeCmd runs an edge node next to a qB instance (see internal/edge and docs/deploy.md).
//
//	OTD_MAIN          wss://www.projectk.org/api/v1/edge/connect
//	OTD_EDGE_TOKEN    token generated in 后台 → qB 服务器 → 边缘节点
//	QB_URL            http://127.0.0.1:8888 (qB WebUI, same host)
//	QB_USER, QB_PASS  qB WebUI login (unless qB bypasses auth for localhost)
//	DATA_ROOT         only files below are served (default /Data/Torrent, mounted read-only)
//	DATA_MAP          optional "qbPrefix=localPrefix[,…]" when qB's paths differ inside the edge
//	EDGE_LISTEN       HTTP listen address (default :8890; TLS terminated by Cloudflare)
//	EDGE_TRUST_PROXY  1 = client IP from CF-Connecting-IP (rate limiting / logs)
//	EDGE_ALLOW_ORIGIN optional CORS origin override (default: the site origin from the main server)
//	EDGE_VERIFY       0 = don't SHA-1 check whole pieces before serving them (default on)
func edgeCmd(args []string) error {
	fs := flag.NewFlagSet("edge", flag.ExitOnError)
	_ = fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	env := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	var pathMap [][2]string
	for _, m := range strings.Split(env("DATA_MAP", ""), ",") {
		if from, to, ok := strings.Cut(strings.TrimSpace(m), "="); ok && from != "" && to != "" {
			pathMap = append(pathMap, [2]string{from, to})
		}
	}
	host, _ := os.Hostname()
	cfg := edge.Config{
		Main: env("OTD_MAIN", ""), Token: env("OTD_EDGE_TOKEN", ""),
		QBURL: env("QB_URL", "http://127.0.0.1:8888"), QBUser: env("QB_USER", ""), QBPass: env("QB_PASS", ""),
		DataRoot: env("DATA_ROOT", "/Data/Torrent"), PathMap: pathMap, Verify: env("EDGE_VERIFY", "1") != "0",
		Listen: env("EDGE_LISTEN", ":8890"), TrustProxy: env("EDGE_TRUST_PROXY", "0") == "1", AllowOrigin: env("EDGE_ALLOW_ORIGIN", ""),
		Host: host, Log: log,
	}
	a, err := edge.New(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("edge starting", "main", cfg.Main, "qb", cfg.QBURL, "data_root", cfg.DataRoot, "listen", cfg.Listen, "version", edge.BuildVersion())
	return a.Run(ctx)
}

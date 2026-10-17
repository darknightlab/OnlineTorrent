// Command otd runs the onlinetorrent server.
//
//	otd [serve] -db /data/otd.db -meta-db /data/meta.db -addr 0.0.0.0:80
//	otd user -db /data/otd.db -username NAME -password PW [-role admin]   (create or reset)
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
	"sync"
	"syscall"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/api"
	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
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
	default:
		err = fmt.Errorf("unknown command %q (serve|user)", cmd)
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
	pool := qb.NewPool(st, log)
	if err := pool.Reload(ctx); err != nil {
		return err
	}
	wg.Go(func() { pool.Run(ctx) })

	cat := catalog.New(st, ms, log)
	cat.Run(ctx)

	wg.Go(func() { maintenance(ctx, st, log) })

	if n, _ := st.UserCount(ctx); n == 0 {
		log.Warn("no users yet; create an admin with: otd user -db <db> -username admin -password <pw> -role admin")
	}

	srv := &api.Server{St: st, Meta: ms, Cat: cat, Pool: pool, Policy: policy.New(st), Log: log, WebFS: web.FS(), BaseCtx: ctx}
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

// Command metad serves the mapping API (/meta/v1) standalone, e.g. for Sonarr-side tools.
// Writes require -admin-token (sent as "Authorization: Bearer <token>").
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func main() {
	dbPath := flag.String("db", "meta.db", "meta SQLite database")
	addr := flag.String("addr", "127.0.0.1:8081", "listen address")
	token := flag.String("admin-token", os.Getenv("METAD_ADMIN_TOKEN"), "token for write endpoints")
	bdURL := flag.String("bangumi-data", "https://cdn.jsdelivr.net/npm/bangumi-data@0.3/dist/data.json", "bangumi-data URL (imported every 12h)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ms, err := meta.Open(*dbPath)
	if err != nil {
		log.Error("open", "err", err)
		os.Exit(1)
	}
	defer ms.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go ms.ReclassifyOnce(ctx, log)
	go func() {
		for {
			hc := &http.Client{Timeout: 3 * time.Minute}
			if n, err := ms.ImportBangumiData(ctx, hc, *bdURL, 3); err != nil {
				log.Warn("bangumi-data", "err", err)
			} else {
				log.Info("bangumi-data imported", "works", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(12 * time.Hour):
			}
		}
	}()
	h := ms.Handler(func(r *http.Request) (string, bool) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		return "token", *token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(*token)) == 1
	})
	hs := &http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = hs.Shutdown(context.Background()) }()
	log.Info("metad listening", "addr", *addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
	}
}

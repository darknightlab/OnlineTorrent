package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// HTTP WebSeed (BEP 19) backed by qBittorrent's torrents/downloadFile (qB ≥ 5.3).
//
// URL: /webseed/{exp}.{sig}/{infohash}/{file path...}
// sig = base64url(HMAC-SHA256(secret, infohash "|" exp))[:22]. URLs are handed out by /play
// only to users whose role satisfies webseed.min_role and only after the policy allowed
// the torrent, so the webseed endpoint itself only checks the signature and expiry.
// Only files that a qB server has fully downloaded are served.

const webseedTTL = 6 * time.Hour

func (s *Server) webseedSig(ih string, exp int64) string {
	m := hmac.New(sha256.New, []byte(s.St.Str(store.KeyWebseedSecret)))
	m.Write([]byte(ih + "|" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:22]
}

// webseedURLs returns signed webseed URLs for a user, or nil if not eligible.
func (s *Server) webseedURLs(r *http.Request, u *store.User, ih string) []string {
	if !s.St.Bool(store.KeyWebseedEnabled) || s.St.Str(store.KeyWebseedSecret) == "" {
		return nil
	}
	if store.RoleRank(u.EffectiveRole()) < store.RoleRank(s.St.Str(store.KeyWebseedMinRole)) {
		return nil
	}
	exp := time.Now().Add(webseedTTL).Unix()
	base := strings.TrimRight(s.St.Str(store.KeySiteBaseURL), "/")
	if base == "" {
		base = "https://" + r.Host
	}
	// trailing slash: webtorrent appends the file path for multi-file torrents
	return []string{base + "/webseed/" + strconv.FormatInt(exp, 10) + "." + s.webseedSig(ih, exp) + "/" + ih + "/"}
}

func (s *Server) serveWebseed(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, msg string) { http.Error(w, msg, code) }
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.St.Bool(store.KeyWebseedEnabled) {
		fail(http.StatusNotFound, "webseed disabled")
		return
	}
	// /webseed/{exp}.{sig}/{ih}/{path...}
	rest := strings.TrimPrefix(r.URL.Path, "/webseed/")
	tok, rest, _ := strings.Cut(rest, "/")
	ihRaw, filePath, _ := strings.Cut(rest, "/")
	expS, sig, ok := strings.Cut(tok, ".")
	ih, ihOK := magnet.NormalizeInfohash(ihRaw)
	exp, err := strconv.ParseInt(expS, 10, 64)
	if !ok || !ihOK || err != nil || time.Now().Unix() > exp || !hmac.Equal([]byte(sig), []byte(s.webseedSig(ih, exp))) {
		fail(http.StatusForbidden, "invalid or expired webseed url")
		return
	}
	if !s.limiter.allow("webseed:"+httpx.ClientIP(r), 600, 50*time.Millisecond) {
		fail(http.StatusTooManyRequests, "rate limited")
		return
	}
	srv, f, found := s.Pool.FindFile(r.Context(), ih, filePath)
	if !found {
		fail(http.StatusNotFound, "file not available on any seed server yet")
		return
	}
	resp, err := srv.Client.DownloadFile(r.Context(), ih, f.Index, r.Header.Get("Range"))
	if err != nil {
		fail(http.StatusBadGateway, "upstream error")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		fail(http.StatusBadGateway, "upstream status "+strconv.Itoa(resp.StatusCode))
		return
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

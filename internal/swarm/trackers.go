// Package swarm manages trackers and swarm statistics: it scrapes the configured
// BitTorrent (UDP BEP 15 / HTTP) and WebTorrent (wss) trackers plus each torrent's own
// announce list, stores seeder / leecher counts per torrent (meta.db swarm_stats) and
// keeps a health record per configured tracker.
package swarm

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Tracker kinds.
const (
	KindUDP  = "udp"
	KindHTTP = "http"
	KindWSS  = "wss"
)

// KindOf classifies a tracker URL ("" = unsupported).
func KindOf(u string) string {
	l := strings.ToLower(strings.TrimSpace(u))
	switch {
	case strings.HasPrefix(l, "udp://"):
		return KindUDP
	case strings.HasPrefix(l, "http://"), strings.HasPrefix(l, "https://"):
		return KindHTTP
	case strings.HasPrefix(l, "wss://"), strings.HasPrefix(l, "ws://"):
		return KindWSS
	}
	return ""
}

// MaxPushTrackers caps the trackers given to qB for one torrent.
const MaxPushTrackers = 30

// Merge concatenates tracker lists in order, dropping blanks, unsupported schemes,
// overlong URLs and duplicates, and keeps at most limit entries (≤ 0: no limit).
func Merge(limit int, lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, t := range l {
			t = strings.TrimSpace(t)
			if t == "" || len(t) > 300 || KindOf(t) == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
			if limit > 0 && len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// PushTrackers is the tracker list for adding ih to qB: the WebTorrent trackers, the
// configured BT trackers, the torrent's own announce list (stored .torrent) and extra
// (e.g. the trackers of a feed magnet), deduplicated and capped at MaxPushTrackers.
func PushTrackers(ctx context.Context, st *store.Store, ms *meta.Store, ih string, extra []string) []string {
	var own []string
	if ms != nil {
		own = ms.Announce(ctx, ih)
	}
	return Merge(MaxPushTrackers, st.List(store.KeyTrackers), st.List(store.KeyBTTrackers), own, extra)
}

// scrapeURL derives the HTTP scrape URL of an announce URL (BEP 48 convention: the last
// path segment must start with "announce"). ok is false if the tracker can't scrape.
func scrapeURL(announce string) (string, bool) {
	u, err := url.Parse(announce)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	i := strings.LastIndex(u.Path, "/")
	if i < 0 || !strings.HasPrefix(u.Path[i+1:], "announce") {
		return "", false
	}
	u.Path = u.Path[:i+1] + "scrape" + strings.TrimPrefix(u.Path[i+1:], "announce")
	u.RawPath = ""
	return u.String(), true
}

// publicHost reports whether every address of host is a public unicast address (trackers
// from third-party .torrent files must not make us send packets into private networks).
func publicHost(ctx context.Context, host string) bool {
	if a, err := netip.ParseAddr(host); err == nil {
		return publicAddr(a)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !publicAddr(a) {
			return false
		}
	}
	return true
}

func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) && !netip.MustParsePrefix("fc00::/7").Contains(a)
}

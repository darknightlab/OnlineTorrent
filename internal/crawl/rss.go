package crawl

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
)

// FeedItem is one torrent announced by a feed (RSS/Atom or bangumi.moe JSON), normalized.
// At least one of Infohash, Magnet or TorrentURL is set.
type FeedItem struct {
	GUID        string   `json:"guid"`
	Title       string   `json:"title"`
	PageURL     string   `json:"url,omitempty"`
	TorrentURL  string   `json:"torrent_url,omitempty"`
	Magnet      string   `json:"magnet,omitempty"`
	Infohash    string   `json:"infohash,omitempty"` // known before downloading anything (nyaa:infoHash, magnet, hash in URL)
	Trackers    []string `json:"-"`                  // udp/http(s) trackers of the item's magnet
	Size        int64    `json:"size_bytes,omitempty"`
	PublishedAt string   `json:"published_at,omitempty"`
	Team        string   `json:"team,omitempty"`
	Category    string   `json:"category,omitempty"`

	moe *catalog.Torrent // bangumi.moe item (tags, team, file list)
}

// FeedInfo describes a parsed feed (for the admin probe).
type FeedInfo struct {
	Title  string
	Link   string
	Format string
}

var (
	reHex40    = regexp.MustCompile(`(?i)(?:^|[^0-9a-f])([0-9a-f]{40})(?:[^0-9a-f]|$)`)
	reTeamHead = regexp.MustCompile(`^\s*[\[【]([^\]】]{1,60})[\]】]`)

	reCustomPub = regexp.MustCompile(`<pubDate>\s*([^<]{10,40})\s*</pubDate>`)
	reCustomLen = regexp.MustCompile(`<contentLength>\s*(\d{1,15})\s*</contentLength>`)
)

// ParseFeed parses an RSS/Atom document of a torrent site. Items without any way to
// obtain a torrent (no .torrent link, magnet or infohash) are dropped.
func ParseFeed(body []byte) (FeedInfo, []FeedItem, error) {
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return FeedInfo{}, nil, err
	}
	info := FeedInfo{Title: feed.Title, Link: feed.Link, Format: feed.FeedType}
	if feed.FeedVersion != "" {
		info.Format += " " + feed.FeedVersion
	}
	out := make([]FeedItem, 0, len(feed.Items))
	for _, it := range feed.Items {
		if fi, ok := feedItem(it); ok {
			out = append(out, fi)
		}
	}
	return info, out, nil
}

func feedItem(it *gofeed.Item) (FeedItem, bool) {
	fi := FeedItem{Title: strings.TrimSpace(it.Title)}
	if it.PublishedParsed != nil {
		fi.PublishedAt = it.PublishedParsed.UTC().Format(time.RFC3339)
	} else if it.UpdatedParsed != nil {
		fi.PublishedAt = it.UpdatedParsed.UTC().Format(time.RFC3339)
	}
	if len(it.Categories) > 0 {
		fi.Category = it.Categories[0]
	}
	consider := func(u string, typ string) {
		u = strings.TrimSpace(u)
		switch {
		case u == "":
		case strings.HasPrefix(strings.ToLower(u), "magnet:?"):
			if fi.Magnet == "" {
				fi.Magnet = u
			}
		case typ == "application/x-bittorrent" || isTorrentURL(u):
			if fi.TorrentURL == "" && isHTTP(u) {
				fi.TorrentURL = u
			}
		}
	}
	for _, e := range it.Enclosures {
		if e == nil {
			continue
		}
		consider(e.URL, strings.ToLower(e.Type))
		if fi.Size == 0 {
			fi.Size, _ = strconv.ParseInt(strings.TrimSpace(e.Length), 10, 64)
		}
	}
	for _, l := range append([]string{it.Link}, it.Links...) {
		consider(l, "")
	}
	if isHTTP(it.Link) && !isTorrentURL(it.Link) {
		fi.PageURL = it.Link
	}
	if fi.PageURL == "" && isHTTP(it.GUID) && !isTorrentURL(it.GUID) {
		fi.PageURL = it.GUID // nyaa: <guid isPermaLink="true">…/view/123</guid>
	}

	// extensions: nyaa:infoHash / nyaa:size, torrent:contentLength (ACG.RIP), Mikan's
	// <torrent><contentLength>, media:content url=….torrent
	for ns, names := range it.Extensions {
		for name, vals := range names {
			for _, v := range vals {
				walkExt(ns, name, v, &fi, consider)
			}
		}
	}

	// default-namespace extras land in Custom as inner XML (Mikan's <torrent xmlns=…>)
	for _, raw := range it.Custom {
		if m := reCustomPub.FindStringSubmatch(raw); m != nil && fi.PublishedAt == "" {
			fi.PublishedAt = parseLooseTime(m[1])
		}
		if m := reCustomLen.FindStringSubmatch(raw); m != nil && fi.Size == 0 {
			fi.Size, _ = strconv.ParseInt(m[1], 10, 64)
		}
	}

	if fi.Magnet != "" {
		if m, err := magnet.Parse(fi.Magnet); err == nil {
			if fi.Infohash == "" {
				fi.Infohash = m.Infohash
			}
			fi.Trackers = filterTrackers(m.Trackers)
			if fi.Title == "" {
				fi.Title = m.Name
			}
		} else {
			fi.Magnet = ""
		}
	}
	// a 40-hex infohash in the .torrent / page URL (Mikan: /Download/…/{ih}.torrent, /Episode/{ih})
	if fi.Infohash == "" {
		for _, u := range []string{fi.TorrentURL, fi.PageURL} {
			if m := reHex40.FindStringSubmatch(urlPath(u)); m != nil {
				fi.Infohash = strings.ToLower(m[1])
				break
			}
		}
	}
	if fi.Infohash == "" && fi.TorrentURL == "" {
		return fi, false
	}
	switch {
	case it.GUID != "":
		fi.GUID = it.GUID
	case fi.PageURL != "":
		fi.GUID = fi.PageURL
	case fi.TorrentURL != "":
		fi.GUID = fi.TorrentURL
	default:
		fi.GUID = "ih:" + fi.Infohash
	}
	if m := reTeamHead.FindStringSubmatch(fi.Title); m != nil {
		fi.Team = strings.TrimSpace(m[1])
	}
	return fi, true
}

func walkExt(ns, name string, v ext.Extension, fi *FeedItem, consider func(string, string)) {
	lname := strings.ToLower(name)
	val := strings.TrimSpace(v.Value)
	switch {
	case lname == "infohash":
		if ih, ok := magnet.NormalizeInfohash(val); ok {
			fi.Infohash = ih
		}
	case lname == "contentlength" && fi.Size == 0:
		fi.Size, _ = strconv.ParseInt(val, 10, 64)
	case lname == "size" && ns == "nyaa" && fi.Size == 0:
		fi.Size = ParseSize(val)
	case lname == "pubdate" && fi.PublishedAt == "":
		// Mikan: <torrent><pubDate>2026-10-18T18:39:54.598</pubDate> (local time, no zone)
		fi.PublishedAt = parseLooseTime(val)
	case lname == "magneturi":
		consider(val, "")
	case lname == "content" || lname == "link":
		if u := v.Attrs["url"]; u != "" {
			consider(u, strings.ToLower(v.Attrs["type"]))
		}
	}
	for cn, children := range v.Children {
		for _, c := range children {
			walkExt(ns, cn, c, fi, consider)
		}
	}
}

var cst = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

// parseLooseTime accepts RFC3339 or a zone-less ISO time (taken as Asia/Shanghai, which is
// what the Chinese sites mean).
func parseLooseTime(s string) string {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", s, cst); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

func isHTTP(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

func isTorrentURL(u string) bool {
	return isHTTP(u) && strings.HasSuffix(strings.ToLower(urlPath(u)), ".torrent")
}

func urlPath(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Path
	}
	return u
}

// filterTrackers keeps at most 10 udp/http(s) trackers (used only to help qB fetch metadata).
func filterTrackers(in []string) []string {
	var out []string
	for _, t := range in {
		l := strings.ToLower(strings.TrimSpace(t))
		if strings.HasPrefix(l, "udp://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
			if len(t) <= 300 {
				out = append(out, strings.TrimSpace(t))
			}
		}
		if len(out) == 10 {
			break
		}
	}
	return out
}

// ParseSize converts "810.4 MiB" / "746.77 MB" to bytes (0 if unknown).
func ParseSize(s string) int64 {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) != 2 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	mult := map[string]float64{"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40, "KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "TIB": 1 << 40}[strings.ToUpper(f[1])]
	return int64(v * mult)
}

// feedError formats a parse error without leaking URL credentials or query secrets
// (same idea as YotsubaNews' feedParseError).
func feedError(endpoint string, err error) error {
	safe := endpointForError(endpoint)
	msg := strings.ReplaceAll(err.Error(), endpoint, safe)
	return fmt.Errorf("%s: %s", safe, msg)
}

func endpointForError(endpoint string) string {
	p, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "[invalid URL]"
	}
	p.User = nil
	p.RawQuery = ""
	p.ForceQuery = false
	p.Fragment = ""
	return p.String()
}

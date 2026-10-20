package qb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Torrent ids. otd identifies torrents by their v1 infohash everywhere. qBittorrent (libtorrent 2)
// identifies a hybrid v1+v2 torrent by its v2 infohash truncated to 40 hex chars — also when it
// was added by a v1 magnet: the id switches once the metadata arrives (verified with qB 5.2.4).
// Requests with the v1 hash then miss (torrents/info → [], torrents/files → 404).
//
// The client therefore translates hash parameters v1 → qB id (learned from every
// torrents/info response, which carries infohash_v1; refreshed on a miss at most once a
// minute) and reports Torrent.Hash as the v1 infohash.

type idMap struct {
	mu        sync.Mutex
	v1ToID    map[string]string
	refreshAt time.Time // next full (category) listing
	recentAt  time.Time // next listing of the most recently added torrents
	// cats limits the refresh listing (empty = all torrents)
	cats []string
}

const (
	idRefreshEvery = time.Minute
	idRecentEvery  = 3 * time.Second // ids switch right after metadata arrives: recent adds
)

// SetIDCategories limits the miss-triggered torrent listing to these categories (the pool
// sets its own categories; the edge lists everything).
func (c *Client) SetIDCategories(cats ...string) {
	c.ids.mu.Lock()
	c.ids.cats = cats
	c.ids.mu.Unlock()
}

func (c *Client) learn(ts []Torrent) {
	c.ids.mu.Lock()
	defer c.ids.mu.Unlock()
	for _, t := range ts {
		if t.InfohashV1 != "" && !strings.EqualFold(t.InfohashV1, t.Hash) {
			if c.ids.v1ToID == nil {
				c.ids.v1ToID = map[string]string{}
			}
			if len(c.ids.v1ToID) > 50_000 {
				clear(c.ids.v1ToID)
			}
			c.ids.v1ToID[strings.ToLower(t.InfohashV1)] = strings.ToLower(t.Hash)
		}
	}
}

// qid maps one (v1) hash to qB's id.
func (c *Client) qid(h string) string {
	c.ids.mu.Lock()
	defer c.ids.mu.Unlock()
	if id, ok := c.ids.v1ToID[strings.ToLower(h)]; ok {
		return id
	}
	return h
}

// translate rewrites the hash / hashes parameters of form (a copy) to qB ids.
func (c *Client) translate(form url.Values) (url.Values, bool) {
	changed := false
	var out url.Values
	for _, k := range []string{"hash", "hashes"} {
		v := form.Get(k)
		if v == "" || v == "all" {
			continue
		}
		parts := strings.Split(v, "|")
		for i, p := range parts {
			if id := c.qid(p); id != p {
				parts[i], changed = id, true
			}
		}
		if changed {
			if out == nil {
				out = url.Values{}
				for kk, vv := range form {
					out[kk] = vv
				}
			}
			out.Set(k, strings.Join(parts, "|"))
		}
	}
	if out == nil {
		return form, false
	}
	return out, true
}

// refreshIDs learns v1 → id mappings after a miss: the 50 most recently added torrents
// (cheap; at most every 3 s), and the configured categories (at most once a minute). Reports
// whether anything was listed.
func (c *Client) refreshIDs(ctx context.Context) bool {
	now := time.Now()
	c.ids.mu.Lock()
	recent, full := !now.Before(c.ids.recentAt), !now.Before(c.ids.refreshAt)
	if recent {
		c.ids.recentAt = now.Add(idRecentEvery)
	}
	if full {
		c.ids.refreshAt = now.Add(idRefreshEvery)
	}
	cats := c.ids.cats
	c.ids.mu.Unlock()
	var lists []url.Values
	if recent {
		lists = append(lists, url.Values{"filter": {"all"}, "sort": {"added_on"}, "reverse": {"true"}, "limit": {"50"}})
	}
	if full {
		if len(cats) == 0 {
			cats = []string{""}
		}
		for _, cat := range cats {
			f := url.Values{"filter": {"all"}}
			if cat != "" {
				f.Set("category", cat)
			}
			lists = append(lists, f)
		}
	}
	for _, f := range lists {
		b, _, err := c.doRaw(ctx, http.MethodGet, "torrents/info", f, nil)
		if err != nil {
			continue
		}
		var ts []Torrent
		if json.Unmarshal(b, &ts) == nil {
			c.learn(ts)
		}
	}
	return len(lists) > 0
}

// LearnRecent lists the most recently added torrents now (no rate limit): call right after
// adding one, whose qB id may already differ from its v1 infohash (a hybrid .torrent).
func (c *Client) LearnRecent(ctx context.Context) {
	b, _, err := c.doRaw(ctx, http.MethodGet, "torrents/info", url.Values{"filter": {"all"}, "sort": {"added_on"}, "reverse": {"true"}, "limit": {"50"}}, nil)
	if err != nil {
		return
	}
	var ts []Torrent
	if json.Unmarshal(b, &ts) == nil {
		c.learn(ts)
	}
}

// missed reports whether a response means "no such torrent" for a hash-addressed request.
func missed(path string, status int, body []byte) bool {
	if status == http.StatusNotFound {
		return true
	}
	return path == "torrents/info" && status == http.StatusOK && strings.TrimSpace(string(body)) == "[]"
}

func hasHashParam(form url.Values) bool {
	for _, k := range []string{"hash", "hashes"} {
		if v := form.Get(k); v != "" && v != "all" {
			return true
		}
	}
	return false
}

// normalize reports torrents by their v1 infohash.
func normalize(ts []Torrent) {
	for i := range ts {
		if ts[i].InfohashV1 != "" {
			ts[i].ID = ts[i].Hash
			ts[i].Hash = strings.ToLower(ts[i].InfohashV1)
		}
		if ts[i].ID == "" {
			ts[i].ID = ts[i].Hash
		}
	}
}

package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

const (
	stateTrending    = "bgm_trending"
	trendingTTL      = 24 * time.Hour
	trendingBackoff  = 15 * time.Minute
	defaultTrendsURL = "https://bgm.tv/anime"
)

type trendingCache struct {
	GeneratedAt string   `json:"generated_at"`
	IDs         []string `json:"ids"`
}

type trendingState struct {
	mu       sync.Mutex
	failAt   time.Time
	lastErr  error
	override string // for unit tests
}

var reSubjectHref = regexp.MustCompile(`^/subject/([1-9]\d*)$`)

// ParseBgmTrendingIDs extracts the ordered, deduplicated subject IDs from the
// "注目动画" (/anime/browser/?sort=trends) featured block on https://bgm.tv/anime,
// ignoring other sections on the page (blogs, discussions, sidebar).
func ParseBgmTrendingIDs(r io.Reader) ([]string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	block := findTrendingBlock(doc)
	if block == nil {
		return nil, errors.New("bgm trending section not found")
	}
	var ids []string
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					if m := reSubjectHref.FindStringSubmatch(strings.TrimSpace(a.Val)); m != nil {
						id := m[1]
						if !seen[id] {
							seen[id] = true
							ids = append(ids, id)
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(block)
	if len(ids) == 0 {
		return nil, errors.New("bgm trending section has no subjects")
	}
	return ids, nil
}

func findTrendingBlock(root *html.Node) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "li" && hasClass(n, "single") && parentHasClass(n, "ul", "featuredItems") {
			if hasTrendsHeading(n) {
				found = n
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

func hasTrendsHeading(li *html.Node) bool {
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "h2" {
			var ok bool
			var check func(*html.Node)
			check = func(n *html.Node) {
				if n.Type == html.ElementNode && n.Data == "a" {
					for _, a := range n.Attr {
						if a.Key == "href" && strings.Contains(a.Val, "/anime/browser") && strings.Contains(a.Val, "sort=trends") {
							ok = true
							return
						}
					}
				}
				for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
					check(ch)
				}
			}
			check(c)
			if ok {
				return true
			}
		}
	}
	return false
}

func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, cl := range strings.Fields(a.Val) {
				if cl == want {
					return true
				}
			}
		}
	}
	return false
}

func parentHasClass(n *html.Node, tag, cl string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == tag && hasClass(p, cl) {
			return true
		}
	}
	return false
}

// SetTrendingURL overrides the upstream URL used to fetch trending anime (for tests).
func (c *Catalog) SetTrendingURL(u string) {
	c.trending.mu.Lock()
	c.trending.override = u
	c.trending.failAt = time.Time{}
	c.trending.lastErr = nil
	c.trending.mu.Unlock()
}

// TrendingWorks returns the ordered list of trending works and the cache generation timestamp.
// The ordered Bangumi subject IDs are cached in SQLite (meta_state) for 24 hours; concurrent
// refreshes are deduplicated via singleflight, and upstream failures retain stale cached IDs
// with a 15-minute retry backoff.
func (c *Catalog) TrendingWorks(ctx context.Context) ([]*meta.Work, string, error) {
	now := time.Now()
	var cached trendingCache
	hasCache := c.meta.GetState(ctx, stateTrending, &cached) && len(cached.IDs) > 0
	if hasCache {
		if genAt, err := time.Parse(time.RFC3339, cached.GeneratedAt); err == nil && now.Sub(genAt) < trendingTTL {
			works, err := c.meta.WorksByBgmIDs(ctx, cached.IDs)
			return works, cached.GeneratedAt, err
		}
	}

	c.trending.mu.Lock()
	inBackoff := !c.trending.failAt.IsZero() && now.Sub(c.trending.failAt) < trendingBackoff
	lastErr := c.trending.lastErr
	c.trending.mu.Unlock()

	if inBackoff {
		if hasCache {
			works, err := c.meta.WorksByBgmIDs(ctx, cached.IDs)
			return works, cached.GeneratedAt, err
		}
		if lastErr != nil {
			return nil, "", lastErr
		}
		return nil, "", errors.New("bgm trending temporarily unavailable")
	}

	ch := c.sf.DoChan("bgm:trending", func() (any, error) {
		var cur trendingCache
		if c.meta.GetState(context.Background(), stateTrending, &cur) && len(cur.IDs) > 0 {
			if genAt, err := time.Parse(time.RFC3339, cur.GeneratedAt); err == nil && time.Since(genAt) < trendingTTL {
				return cur, nil
			}
		}
		fctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ids, err := c.fetchBgmTrendingIDs(fctx)
		if err != nil {
			c.trending.mu.Lock()
			c.trending.failAt = time.Now()
			c.trending.lastErr = err
			c.trending.mu.Unlock()
			return nil, err
		}
		next := trendingCache{GeneratedAt: store.Now(), IDs: ids}
		_ = c.meta.SetState(fctx, stateTrending, next)
		c.trending.mu.Lock()
		c.trending.failAt = time.Time{}
		c.trending.lastErr = nil
		c.trending.mu.Unlock()
		return next, nil
	})

	var res singleflightResult
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case r := <-ch:
		res = singleflightResult{val: r.Val, err: r.Err}
	}

	if res.err != nil {
		c.log.Warn("bgm trending refresh failed", "err", res.err)
		if hasCache {
			works, err := c.meta.WorksByBgmIDs(ctx, cached.IDs)
			return works, cached.GeneratedAt, err
		}
		return nil, "", res.err
	}
	fresh := res.val.(trendingCache)
	works, err := c.meta.WorksByBgmIDs(ctx, fresh.IDs)
	return works, fresh.GeneratedAt, err
}

type singleflightResult struct {
	val any
	err error
}

func (c *Catalog) fetchBgmTrendingIDs(ctx context.Context) ([]string, error) {
	c.trending.mu.Lock()
	u := c.trending.override
	c.trending.mu.Unlock()
	if u == "" {
		u = defaultTrendsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.st.Str(store.KeyBgmUserAgent))
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bgm trending: HTTP %d", resp.StatusCode)
	}
	return ParseBgmTrendingIDs(io.LimitReader(resp.Body, 2<<20))
}

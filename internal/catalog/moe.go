// Package catalog aggregates upstream sources (bangumi.moe, bangumi-data via meta)
// into the torrent listings and the airing schedule served to the frontend.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// moeTorrent is the raw bangumi.moe torrent (v1 and v2 shapes).
type moeTorrent struct {
	ID          string     `json:"_id"`
	Title       string     `json:"title"`
	InfoHash    string     `json:"infoHash"`
	Size        string     `json:"size"`
	TeamID      string     `json:"team_id"`
	TagIDs      []string   `json:"tag_ids"`
	PublishTime string     `json:"publish_time"`
	Content     [][]string `json:"content"`
	Seeders     int        `json:"seeders"`
	Leechers    int        `json:"leechers"`
	Downloads   int        `json:"downloads"`
	Team        *struct {
		ID   string `json:"_id"`
		Name string `json:"name"`
	} `json:"team"`
	Tags []moeTag `json:"tags"`
}

type moeTag struct {
	ID     string            `json:"_id"`
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Locale map[string]string `json:"locale"`
}

// DisplayName prefers zh_cn.
func (t moeTag) DisplayName() string {
	if v := t.Locale["zh_cn"]; v != "" {
		return v
	}
	return t.Name
}

type moeBangumi struct {
	ID        string `json:"_id"`
	Name      string `json:"name"`
	TagID     string `json:"tag_id"`
	Cover     string `json:"cover"`
	Icon      string `json:"icon"`
	ShowOn    int    `json:"showOn"`
	StartDate int64  `json:"startDate"`
}

type moeClient struct {
	base func() string
	hc   func() *http.Client
	lim  *pacer
}

// pacer is a global token bucket for upstream requests: `rate` per second with a burst
// worth `window`. wait blocks (honoring ctx) instead of failing.
type pacer struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
	window   time.Duration
}

func newPacer(perSec float64, window time.Duration) *pacer {
	return &pacer{interval: time.Duration(float64(time.Second) / perSec), window: window}
}

func (p *pacer) wait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	now := time.Now()
	if floor := now.Add(-p.window); p.next.Before(floor) {
		p.next = floor
	}
	slot := p.next
	p.next = slot.Add(p.interval)
	p.mu.Unlock()
	d := time.Until(slot)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *moeClient) get(ctx context.Context, path string, out any) error {
	return c.req(ctx, http.MethodGet, path, nil, out)
}

func (c *moeClient) post(ctx context.Context, path string, body any, out any) error {
	return c.req(ctx, http.MethodPost, path, body, out)
}

func (c *moeClient) req(ctx context.Context, method, path string, body any, out any) error {
	if err := c.lim.wait(ctx); err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.base(), "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "onlinetorrent/2 (+https://www.projectk.org)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("bangumi.moe %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

type moePage struct {
	PageCount int          `json:"page_count"`
	Count     int          `json:"count"`
	Torrents  []moeTorrent `json:"torrents"`
}

func (c *moeClient) latest(ctx context.Context, page int) (moePage, error) {
	var p moePage
	err := c.get(ctx, "/api/torrent/page/"+strconv.Itoa(page), &p)
	return p, err
}

func (c *moeClient) search(ctx context.Context, q string, page int) (moePage, error) {
	var p moePage
	err := c.get(ctx, "/api/v2/torrent/search?query="+url.QueryEscape(q)+"&p="+strconv.Itoa(page), &p)
	return p, err
}

func (c *moeClient) byTag(ctx context.Context, tagIDs []string, page int) (moePage, error) {
	var p moePage
	err := c.post(ctx, "/api/torrent/search", map[string]any{"tag_id": tagIDs, "p": page}, &p)
	return p, err
}

func (c *moeClient) detail(ctx context.Context, id string) (moeTorrent, error) {
	var t moeTorrent
	err := c.get(ctx, "/api/v2/torrent/"+url.PathEscape(id), &t)
	return t, err
}

func (c *moeClient) teams(ctx context.Context, ids []string) ([]struct {
	ID   string `json:"_id"`
	Name string `json:"name"`
}, error) {
	var out []struct {
		ID   string `json:"_id"`
		Name string `json:"name"`
	}
	err := c.post(ctx, "/api/team/fetch", map[string]any{"_ids": ids}, &out)
	return out, err
}

func (c *moeClient) tags(ctx context.Context, ids []string) ([]moeTag, error) {
	var out []moeTag
	err := c.post(ctx, "/api/tag/fetch", map[string]any{"_ids": ids}, &out)
	return out, err
}

func (c *moeClient) current(ctx context.Context) ([]moeBangumi, error) {
	var out []moeBangumi
	err := c.get(ctx, "/api/bangumi/current", &out)
	return out, err
}

// parseSize converts "746.77 MB" to bytes.
func parseSize(s string) int64 {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) != 2 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40, "KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "TIB": 1 << 40}[strings.ToUpper(f[1])]
	return int64(v * mult)
}

func moeTime(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}

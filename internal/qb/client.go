// Package qb is a minimal qBittorrent WebAPI v2 client (5.x) plus a server pool.
package qb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to one qBittorrent instance. It authenticates with an API key
// (Authorization: Bearer, qB ≥ 5.2) when set, otherwise with a login cookie that is
// reused and refreshed on 403.
type Client struct {
	base, user, pass, apiKey string
	hc                       *http.Client

	mu       sync.Mutex
	loggedIn bool
}

// NewClient creates a client.
func NewClient(base, user, pass, apiKey string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		base: strings.TrimRight(base, "/"), user: user, pass: pass, apiKey: apiKey,
		hc: &http.Client{Jar: jar, Timeout: 20 * time.Second},
	}
}

// ErrAuth means credentials were rejected.
var ErrAuth = errors.New("qBittorrent 认证失败")

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	// qB < 5.2: 200 "Ok." / "Fails."; qB ≥ 5.2: 204 on success, 401 on failure.
	ok := resp.StatusCode == http.StatusNoContent || (resp.StatusCode == 200 && strings.Contains(string(body), "Ok"))
	if !ok {
		return fmt.Errorf("%w (HTTP %d)", ErrAuth, resp.StatusCode)
	}
	c.loggedIn = true
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values) ([]byte, int, error) {
	try := func() (*http.Response, error) {
		var body io.Reader
		if form != nil && method == http.MethodPost {
			body = strings.NewReader(form.Encode())
		}
		u := c.base + "/api/v2/" + path
		if form != nil && method == http.MethodGet {
			u += "?" + form.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Referer", c.base)
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		return c.hc.Do(req)
	}
	if c.apiKey == "" {
		c.mu.Lock()
		if !c.loggedIn {
			if err := c.login(ctx); err != nil {
				c.mu.Unlock()
				return nil, 0, err
			}
		}
		c.mu.Unlock()
	}
	resp, err := try()
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusForbidden && c.apiKey == "" {
		resp.Body.Close()
		c.mu.Lock()
		c.loggedIn = false
		err := c.login(ctx)
		c.mu.Unlock()
		if err != nil {
			return nil, 0, err
		}
		if resp, err = try(); err != nil {
			return nil, 0, err
		}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return b, resp.StatusCode, fmt.Errorf("qBittorrent %s: HTTP %d %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, resp.StatusCode, nil
}

// Version returns the app version, e.g. "v5.3.0beta1".
func (c *Client) Version(ctx context.Context) (string, error) {
	b, _, err := c.do(ctx, http.MethodGet, "app/version", nil)
	return strings.TrimSpace(string(b)), err
}

// BuildInfo returns e.g. {"libtorrent":"2.1.1.0",...}.
func (c *Client) BuildInfo(ctx context.Context) (map[string]any, error) {
	b, _, err := c.do(ctx, http.MethodGet, "app/buildInfo", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

// AddParams for torrents/add.
type AddParams struct {
	URLs                string
	Category            string
	SeedingTimeLimitMin int
	RatioLimit          float64 // -2 global, -1 unlimited
}

// Add adds magnet(s). qB 5.x returns 200/202 with JSON or "Ok."; 409 when all failed (often "already exists").
func (c *Client) Add(ctx context.Context, p AddParams) error {
	f := url.Values{
		"urls":               {p.URLs},
		"category":           {p.Category},
		"sequentialDownload": {"true"},
		"firstLastPiecePrio": {"true"},
		"stopped":            {"false"},
		"paused":             {"false"},
		"seedingTimeLimit":   {strconv.Itoa(p.SeedingTimeLimitMin)},
		"ratioLimit":         {strconv.FormatFloat(p.RatioLimit, 'f', -1, 64)},
	}
	if p.SeedingTimeLimitMin == 0 {
		f.Set("seedingTimeLimit", "-2")
	}
	_, status, err := c.do(ctx, http.MethodPost, "torrents/add", f)
	if status == http.StatusConflict {
		return nil // already present
	}
	return err
}

// AddTrackers adds trackers to an existing torrent (idempotent in qB).
func (c *Client) AddTrackers(ctx context.Context, hash string, trackers []string) error {
	_, _, err := c.do(ctx, http.MethodPost, "torrents/addTrackers", url.Values{"hash": {hash}, "urls": {strings.Join(trackers, "\n")}})
	return err
}

// Delete removes torrents with files.
func (c *Client) Delete(ctx context.Context, hashes []string) error {
	_, _, err := c.do(ctx, http.MethodPost, "torrents/delete", url.Values{"hashes": {strings.Join(hashes, "|")}, "deleteFiles": {"true"}})
	return err
}

// Torrent is a subset of torrents/info.
type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	Size        int64   `json:"size"`
	AddedOn     int64   `json:"added_on"`
	HasMetadata bool    `json:"has_metadata"`
	NumSeeds    int     `json:"num_seeds"`
	NumLeechs   int     `json:"num_leechs"`
	Upspeed     int64   `json:"upspeed"`
	Dlspeed     int64   `json:"dlspeed"`
	Category    string  `json:"category"`
}

// Info lists torrents in a category (and optionally given hashes).
func (c *Client) Info(ctx context.Context, category string, hashes ...string) ([]Torrent, error) {
	f := url.Values{"filter": {"all"}}
	if category != "" {
		f.Set("category", category)
	}
	if len(hashes) > 0 {
		f.Set("hashes", strings.Join(hashes, "|"))
	}
	b, _, err := c.do(ctx, http.MethodGet, "torrents/info", f)
	if err != nil {
		return nil, err
	}
	var out []Torrent
	return out, json.Unmarshal(b, &out)
}

// File is a torrents/files entry.
type File struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
}

// Files lists a torrent's files (empty until metadata is fetched).
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	b, _, err := c.do(ctx, http.MethodGet, "torrents/files", url.Values{"hash": {hash}})
	if err != nil {
		return nil, err
	}
	var out []File
	return out, json.Unmarshal(b, &out)
}

// CreateCategory ensures a category exists (409 if it already does).
func (c *Client) CreateCategory(ctx context.Context, name string) error {
	_, status, err := c.do(ctx, http.MethodPost, "torrents/createCategory", url.Values{"category": {name}})
	if status == http.StatusConflict {
		return nil
	}
	return err
}

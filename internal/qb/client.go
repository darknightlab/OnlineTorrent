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
// reused and refreshed on 401/403.
//
// After a rejected login the client backs off (1m doubling to 30m) without contacting
// the server, so a wrong password doesn't get our IP banned by the WebUI.
type Client struct {
	base, user, pass, apiKey string
	hc                       *http.Client
	hcStream                 *http.Client

	mu        sync.Mutex
	loggedIn  bool
	loginGen  uint64 // bumped on every successful login
	authUntil time.Time
	authWait  time.Duration
}

const (
	authBackoffMin = time.Minute
	authBackoffMax = 30 * time.Minute
)

// NewClient creates a client.
func NewClient(base, user, pass, apiKey string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		base: strings.TrimRight(base, "/"), user: user, pass: pass, apiKey: apiKey,
		hc:       &http.Client{Jar: jar, Timeout: 20 * time.Second},
		hcStream: &http.Client{Jar: jar},
	}
}

// ErrAuth means credentials were rejected.
var ErrAuth = errors.New("qBittorrent 认证失败")

// ensureLogin logs in unless a login newer than gen already happened. Caller holds c.mu.
func (c *Client) ensureLogin(ctx context.Context, gen uint64, force bool) error {
	if c.loggedIn && (!force || c.loginGen != gen) {
		return nil // someone else already (re)logged in
	}
	if wait := time.Until(c.authUntil); wait > 0 {
		return fmt.Errorf("%w，%s 后重试", ErrAuth, wait.Round(time.Second))
	}
	c.loggedIn = false
	err := c.login(ctx)
	if errors.Is(err, ErrAuth) {
		c.authWait = min(max(c.authWait*2, authBackoffMin), authBackoffMax)
		c.authUntil = time.Now().Add(c.authWait)
	} else if err == nil {
		c.authWait, c.authUntil = 0, time.Time{}
		c.loginGen++
	}
	return err
}

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

// raw performs an authenticated request (re-logging in once on 401/403) and returns the
// open response; the caller must close the body. stream selects the client without a timeout.
func (c *Client) raw(ctx context.Context, method, path string, form url.Values, hdr http.Header, stream bool) (*http.Response, error) {
	hc := c.hc
	if stream {
		hc = c.hcStream
	}
	try := func() (*http.Response, error) {
		var body io.Reader
		if form != nil && method == http.MethodPost {
			body = strings.NewReader(form.Encode())
		}
		u := c.base + "/api/v2/" + path
		if form != nil && method != http.MethodPost {
			u += "?" + form.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Referer", c.base)
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		return hc.Do(req)
	}
	var gen uint64
	if c.apiKey == "" {
		c.mu.Lock()
		err := c.ensureLogin(ctx, c.loginGen, false)
		gen = c.loginGen
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	resp, err := try()
	if err != nil {
		return nil, err
	}
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) && c.apiKey == "" {
		resp.Body.Close()
		c.mu.Lock()
		err := c.ensureLogin(ctx, gen, true)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return try()
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values) ([]byte, int, error) {
	resp, err := c.raw(ctx, method, path, form, nil, false)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(b[:min(len(b), 256)]))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return b, resp.StatusCode, fmt.Errorf("%w: %s HTTP %d %s", ErrAuth, path, resp.StatusCode, msg)
		}
		return b, resp.StatusCode, fmt.Errorf("qBittorrent %s: HTTP %d %s", path, resp.StatusCode, msg)
	}
	return b, resp.StatusCode, nil
}

// DownloadFile streams one file of a torrent (qB ≥ 5.3, WebAPI 2.16.0: torrents/downloadFile),
// forwarding the Range header. The caller must close the body.
func (c *Client) DownloadFile(ctx context.Context, hash string, index int, rangeHdr string) (*http.Response, error) {
	h := http.Header{}
	if rangeHdr != "" {
		h.Set("Range", rangeHdr)
	}
	return c.raw(ctx, http.MethodGet, "torrents/downloadFile", url.Values{"hash": {hash}, "file": {strconv.Itoa(index)}}, h, true)
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
	// StopCondition "MetadataReceived" stops the torrent once metadata arrives (qB ≥ 4.5;
	// metadata-only fetches). Empty = qB default (None).
	StopCondition string
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
	if p.StopCondition != "" {
		f.Set("stopCondition", p.StopCondition)
		f.Del("sequentialDownload")
		f.Del("firstLastPiecePrio")
	}
	_, status, err := c.do(ctx, http.MethodPost, "torrents/add", f)
	if status == http.StatusConflict {
		return nil // already present
	}
	return err
}

// SetCategory moves torrents to a category.
func (c *Client) SetCategory(ctx context.Context, hash, category string) error {
	_, _, err := c.do(ctx, http.MethodPost, "torrents/setCategory", url.Values{"hashes": {hash}, "category": {category}})
	return err
}

// Start resumes torrents (qB 5: torrents/start; qB 4: torrents/resume).
func (c *Client) Start(ctx context.Context, hash string) error {
	_, status, err := c.do(ctx, http.MethodPost, "torrents/start", url.Values{"hashes": {hash}})
	if status == http.StatusNotFound {
		_, _, err = c.do(ctx, http.MethodPost, "torrents/resume", url.Values{"hashes": {hash}})
	}
	return err
}

// SetShareLimits sets ratio / seeding time limits (-2 = global, -1 = unlimited).
func (c *Client) SetShareLimits(ctx context.Context, hash string, ratio float64, seedingTimeMin int) error {
	if seedingTimeMin == 0 {
		seedingTimeMin = -2
	}
	_, _, err := c.do(ctx, http.MethodPost, "torrents/setShareLimits", url.Values{"hashes": {hash},
		"ratioLimit": {strconv.FormatFloat(ratio, 'f', -1, 64)}, "seedingTimeLimit": {strconv.Itoa(seedingTimeMin)}, "inactiveSeedingTimeLimit": {"-2"}})
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
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
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

package crawl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HTTP fetching for crawl sources and .torrent downloads, modelled on YotsubaNews'
// sources/http_client.go: bounded timeouts and body sizes, a redirect cap, credentials
// dropped on cross-origin redirects, and only public addresses are dialled (feed content
// is third-party, so a .torrent link must not reach internal services). On top of that:
// conditional GET (ETag / If-Modified-Since) and per-host serialization with a small gap
// between requests, so we stay polite to each site.

const (
	fetchTimeout      = 45 * time.Second
	connectTimeout    = 10 * time.Second
	headerTimeout     = 20 * time.Second
	maxRedirects      = 5
	MaxFeedBytes      = 10 << 20 // dmhy's RSS is ~2.5MB
	hostGap           = 1500 * time.Millisecond
	defaultUserAgent  = "onlinetorrent/2 (+https://www.projectk.org)"
	maxErrorBodyBytes = 512
)

// envProxy selects the environment's proxy settings (upstream.proxy empty).
const envProxy = "env"

// GetOptions for one request.
type GetOptions struct {
	ETag          string
	LastModified  string
	MaxBytes      int64
	ProxyDisabled bool
	Accept        string
}

// Response is a fully read response.
type Response struct {
	Status       int
	NotModified  bool
	Body         []byte
	ETag         string
	LastModified string
	FinalURL     string
}

// Fetcher performs polite HTTP GETs.
type Fetcher struct {
	Proxy     func() string // upstream proxy URL ("" = direct)
	UserAgent func() string

	mu      sync.Mutex
	clients map[string]*http.Client // by proxy URL
	gates   map[string]*hostGate

	// allowPrivate disables the public-address guard (tests with httptest servers).
	allowPrivate bool
	gap          time.Duration // minimum pause between two requests to one host
}

type hostGate struct {
	sem  chan struct{}
	last time.Time
}

// NewFetcher creates a fetcher.
func NewFetcher(proxy, ua func() string) *Fetcher {
	return &Fetcher{Proxy: proxy, UserAgent: ua, clients: map[string]*http.Client{}, gates: map[string]*hostGate{}, gap: hostGap}
}

func (f *Fetcher) gate(host string) *hostGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.gates[host]
	if g == nil {
		g = &hostGate{sem: make(chan struct{}, 1)}
		f.gates[host] = g
	}
	return g
}

func (f *Fetcher) client(proxy string) (*http.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.clients[proxy]; c != nil {
		return c, nil
	}
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: time.Second,
	}
	switch proxy {
	case "":
		tr.DialContext = f.safeDial(dialer)
	case envProxy:
		// HTTP(S)_PROXY / NO_PROXY from the environment (like the rest of otd's clients)
		tr.Proxy = http.ProxyFromEnvironment
		tr.DialContext = dialer.DialContext
	default:
		pu, err := url.Parse(proxy)
		if err != nil || pu.Host == "" {
			return nil, errors.New("invalid upstream proxy URL")
		}
		tr.Proxy = http.ProxyURL(pu)
		tr.DialContext = dialer.DialContext
	}
	c := &http.Client{Transport: tr, Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-http URL")
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Host != via[len(via)-1].URL.Host {
				req.Header.Del("Authorization")
				req.Header.Del("Cookie")
			}
			return nil
		}}
	f.clients[proxy] = c
	return c, nil
}

// safeDial refuses non-public addresses (SSRF guard for direct connections).
func (f *Fetcher) safeDial(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		var errs []error
		for _, a := range addrs {
			if !f.allowPrivate && !isPublic(a) {
				return nil, fmt.Errorf("host %q resolves to non-public address %s", host, a)
			}
		}
		for _, a := range addrs {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return c, nil
			}
			errs = append(errs, err)
		}
		return nil, errors.Join(errs...)
	}
}

func resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %q: no addresses", host)
	}
	return addrs, nil
}

func isPublic(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) && !netip.MustParsePrefix("198.18.0.0/15").Contains(a) &&
		!netip.MustParsePrefix("fc00::/7").Contains(a)
}

// Get fetches rawURL (serialized per host). A 304 yields NotModified; other non-2xx
// statuses are errors.
func (f *Fetcher) Get(ctx context.Context, rawURL string, opt GetOptions) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("invalid URL")
	}
	proxy := ""
	if !opt.ProxyDisabled {
		if f.Proxy != nil {
			proxy = f.Proxy()
		}
		if proxy == "" && !f.allowPrivate {
			proxy = envProxy
		}
	}
	if proxy != "" && !f.allowPrivate {
		// through a proxy the dial guard can't see the target: check it resolves publicly
		addrs, err := resolve(ctx, u.Hostname())
		if err == nil {
			for _, a := range addrs {
				if !isPublic(a) {
					return nil, fmt.Errorf("host %q resolves to non-public address", u.Hostname())
				}
			}
		}
	}
	hc, err := f.client(proxy)
	if err != nil {
		return nil, err
	}
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = MaxFeedBytes
	}

	g := f.gate(strings.ToLower(u.Host))
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() {
		g.last = time.Now()
		<-g.sem
	}()
	if wait := f.gap - time.Since(g.last); wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	ua := defaultUserAgent
	if f.UserAgent != nil {
		if v := f.UserAgent(); v != "" {
			ua = v
		}
	}
	req.Header.Set("User-Agent", ua)
	if opt.Accept != "" {
		req.Header.Set("Accept", opt.Accept)
	}
	if opt.ETag != "" {
		req.Header.Set("If-None-Match", opt.ETag)
	}
	if opt.LastModified != "" {
		req.Header.Set("If-Modified-Since", opt.LastModified)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := &Response{Status: resp.StatusCode, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), FinalURL: resp.Request.URL.String()}
	if resp.StatusCode == http.StatusNotModified {
		out.NotModified = true
		return out, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		msg := strings.TrimSpace(string(b))
		if len(msg) > 120 || strings.Contains(msg, "<") {
			msg = ""
		}
		return out, fmt.Errorf("HTTP %d %s", resp.StatusCode, msg)
	}
	if resp.ContentLength > opt.MaxBytes {
		return out, fmt.Errorf("response exceeds %d bytes", opt.MaxBytes)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, opt.MaxBytes+1))
	if err != nil {
		return out, fmt.Errorf("read body: %w", err)
	}
	if int64(len(b)) > opt.MaxBytes {
		return out, fmt.Errorf("response exceeds %d bytes", opt.MaxBytes)
	}
	out.Body = b
	return out, nil
}

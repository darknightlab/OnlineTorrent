package swarm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// HTTP scrape (BEP 48): GET {scrape}?info_hash=<20 raw bytes>&info_hash=… →
// d5:filesd20:<hash>d8:completei…e10:downloadedi…e10:incompletei…eeee

const (
	httpMaxBatch = 50
	httpMaxBody  = 1 << 20
)

// httpScrapeRequest builds the scrape URL for hashes (existing query, e.g. a passkey, is kept).
func httpScrapeRequest(scrape string, hashes [][20]byte) string {
	var b strings.Builder
	b.WriteString(scrape)
	sep := "?"
	if strings.Contains(scrape, "?") {
		sep = "&"
	}
	for _, h := range hashes {
		b.WriteString(sep)
		b.WriteString("info_hash=")
		b.WriteString(url.QueryEscape(string(h[:])))
		sep = "&"
	}
	return b.String()
}

// parseHTTPScrape decodes a scrape response. Hashes the tracker didn't list are absent.
func parseHTTPScrape(body []byte) (map[[20]byte]Counts, error) {
	v, err := torrentfile.Decode(body)
	if err != nil {
		return nil, fmt.Errorf("bad scrape response: %w", err)
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("bad scrape response: not a dictionary")
	}
	if fr, ok := root["failure reason"].([]byte); ok {
		return nil, fmt.Errorf("tracker error: %s", sanitize(string(fr)))
	}
	files, ok := root["files"].(map[string]any)
	if !ok {
		return nil, errors.New("bad scrape response: no files")
	}
	out := map[[20]byte]Counts{}
	for k, fv := range files {
		if len(k) != 20 {
			continue
		}
		f, ok := fv.(map[string]any)
		if !ok {
			continue
		}
		var h [20]byte
		copy(h[:], k)
		num := func(key string) int {
			n, _ := f[key].(int64)
			return int(max(0, min(n, 1<<31-1)))
		}
		out[h] = Counts{Seeders: num("complete"), Leechers: num("incomplete"), Completed: num("downloaded")}
	}
	return out, nil
}

func (sc *Scraper) httpScrape(ctx context.Context, announce string, hashes [][20]byte) (map[[20]byte]Counts, error) {
	su, ok := scrapeURL(announce)
	if !ok {
		return nil, errors.New("tracker does not support scrape")
	}
	res, err := sc.httpScrapeOnce(ctx, su, hashes)
	var se statusError
	if errors.As(err, &se) && se >= 500 && ctx.Err() == nil {
		// some trackers (e.g. nyaa.tracker.wf) answer a random 505 now and then: retry once
		res, err = sc.httpScrapeOnce(ctx, su, hashes)
	}
	return res, err
}

type statusError int

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", int(e)) }

func (sc *Scraper) httpScrapeOnce(ctx context.Context, su string, hashes [][20]byte) (map[[20]byte]Counts, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpScrapeRequest(su, hashes), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", sc.userAgent())
	resp, err := sc.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL holds binary hashes: don't put it in messages
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > httpMaxBody {
		return nil, errors.New("scrape response too large")
	}
	return parseHTTPScrape(body)
}

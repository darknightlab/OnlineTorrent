package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/net/websocket"
)

// WebTorrent tracker scrape (bittorrent-tracker / aquatic_ws):
//   → {"action":"scrape","info_hash":["<20 bytes as latin1 string>", …]}
//   ← {"action":"scrape","files":{"<latin1>":{"complete":1,"incomplete":0,"downloaded":3}}}
// Several requests are sent on one connection; replies carry no transaction id, so they
// are matched by their keys.

const wssMaxBatch = 50

// latin1 maps every byte to the code point of the same value (how WebTorrent trackers
// put binary infohashes into JSON strings).
func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// fromLatin1 reverses latin1 (ok = every code point ≤ 0xff).
func fromLatin1(s string) ([]byte, bool) {
	out := make([]byte, 0, 20)
	for _, c := range s {
		if c > 0xff {
			return nil, false
		}
		out = append(out, byte(c))
	}
	return out, true
}

type wssReq struct {
	Action   string   `json:"action"`
	InfoHash []string `json:"info_hash"`
}

type wssResp struct {
	Action  string `json:"action"`
	Failure string `json:"failure reason"`
	Files   map[string]struct {
		Complete   int `json:"complete"`
		Incomplete int `json:"incomplete"`
		Downloaded int `json:"downloaded"`
	} `json:"files"`
}

func parseWSSScrape(msg []byte) (map[[20]byte]Counts, bool, error) {
	var r wssResp
	if err := json.Unmarshal(msg, &r); err != nil {
		return nil, false, fmt.Errorf("bad scrape response: %w", err)
	}
	if r.Failure != "" {
		return nil, true, fmt.Errorf("tracker error: %s", sanitize(r.Failure))
	}
	if r.Action != "scrape" {
		return nil, false, nil // e.g. an announce-related message: not ours
	}
	out := map[[20]byte]Counts{}
	for k, f := range r.Files {
		b, ok := fromLatin1(k)
		if !ok || len(b) != 20 {
			continue
		}
		var h [20]byte
		copy(h[:], b)
		out[h] = Counts{Seeders: max(0, f.Complete), Leechers: max(0, f.Incomplete), Completed: max(0, f.Downloaded)}
	}
	return out, true, nil
}

func (sc *Scraper) wssScrape(ctx context.Context, tracker string, hashes [][20]byte) (map[[20]byte]Counts, error) {
	cfg, err := websocket.NewConfig(tracker, sc.origin())
	if err != nil {
		return nil, err
	}
	cfg.Header.Set("User-Agent", sc.userAgent())
	ws, err := cfg.DialContext(ctx)
	if err != nil {
		var de *websocket.DialError
		if errors.As(err, &de) && de.Err != nil {
			if errors.Is(de.Err, websocket.ErrBadStatus) {
				return nil, errors.New("handshake failed: bad status")
			}
			return nil, de.Err // keep the net error (dns / refused / timeout) for classify
		}
		return nil, err
	}
	defer ws.Close()
	ws.MaxPayloadBytes = 1 << 20
	dl := time.Now().Add(8 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = ws.SetDeadline(dl)
	// close the socket when ctx ends so a blocked read returns
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()

	batches := 0
	for start := 0; start < len(hashes); start += wssMaxBatch {
		req := wssReq{Action: "scrape"}
		for _, h := range hashes[start:min(len(hashes), start+wssMaxBatch)] {
			req.InfoHash = append(req.InfoHash, latin1(h[:]))
		}
		if err := websocket.JSON.Send(ws, req); err != nil {
			return nil, err
		}
		batches++
	}
	out := map[[20]byte]Counts{}
	for got := 0; got < batches; {
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			if len(out) > 0 {
				return out, nil // partial answer
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil, errors.New("tracker closed the connection without answering (scrape unsupported?)")
			}
			return nil, err
		}
		m, isScrape, err := parseWSSScrape(msg)
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		if !isScrape {
			continue
		}
		got++
		for h, c := range m {
			out[h] = c
		}
	}
	return out, nil
}

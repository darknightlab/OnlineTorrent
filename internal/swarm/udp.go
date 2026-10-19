package swarm

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

// UDP tracker protocol (BEP 15): connect → connection id (valid ~1 minute) → scrape with up
// to 74 infohashes per packet (8 + 20·74 = 1488 bytes stays below common MTUs).

const (
	udpProtocolID   = 0x41727101980
	udpActConnect   = 0
	udpActScrape    = 2
	udpActError     = 3
	udpMaxBatch     = 74
	udpConnLifetime = 55 * time.Second
)

// Counts are the scrape numbers of one torrent on one tracker.
type Counts struct {
	Seeders   int
	Leechers  int
	Completed int
}

// udpTimeouts are the per-attempt waits (BEP 15 suggests 15·2^n s, far too long for us).
var udpTimeouts = []time.Duration{1500 * time.Millisecond, 2500 * time.Millisecond, 4 * time.Second}

type udpTracker struct {
	mu       sync.Mutex // one exchange at a time (keeps transaction handling simple)
	hostPort string
	connID   uint64
	connAt   time.Time
}

func udpHostPort(announce string) (string, error) {
	u, err := url.Parse(announce)
	if err != nil || u.Scheme != "udp" || u.Hostname() == "" || u.Port() == "" {
		return "", fmt.Errorf("invalid udp tracker %q", announce)
	}
	return u.Host, nil
}

func newTID() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

// exchange sends req and waits for a reply with the same transaction id, retrying with
// udpTimeouts (bounded by ctx).
func udpExchange(ctx context.Context, conn net.Conn, req []byte, tid uint32) ([]byte, error) {
	buf := make([]byte, 8+12*udpMaxBatch+64)
	var lastErr error = errors.New("timeout")
	for _, wait := range udpTimeouts {
		if ctx.Err() != nil {
			break
		}
		dl := time.Now().Add(wait)
		if d, ok := ctx.Deadline(); ok && d.Before(dl) {
			dl = d
		}
		_ = conn.SetDeadline(dl)
		if _, err := conn.Write(req); err != nil {
			return nil, err
		}
		for {
			n, err := conn.Read(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					lastErr = errors.New("timeout")
					break
				}
				return nil, err // e.g. ICMP port unreachable
			}
			if n < 8 || binary.BigEndian.Uint32(buf[4:8]) != tid {
				continue // stale reply of an earlier attempt
			}
			out := make([]byte, n)
			copy(out, buf[:n])
			if binary.BigEndian.Uint32(out[0:4]) == udpActError {
				return nil, fmt.Errorf("tracker error: %s", sanitize(string(out[8:])))
			}
			return out, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, lastErr
}

func (t *udpTracker) connect(ctx context.Context, conn net.Conn) error {
	if t.connID != 0 && time.Since(t.connAt) < udpConnLifetime {
		return nil
	}
	tid := newTID()
	req := make([]byte, 16)
	binary.BigEndian.PutUint64(req[0:], udpProtocolID)
	binary.BigEndian.PutUint32(req[8:], udpActConnect)
	binary.BigEndian.PutUint32(req[12:], tid)
	resp, err := udpExchange(ctx, conn, req, tid)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if len(resp) < 16 || binary.BigEndian.Uint32(resp[0:4]) != udpActConnect {
		return errors.New("connect: bad response")
	}
	t.connID, t.connAt = binary.BigEndian.Uint64(resp[8:16]), time.Now()
	return nil
}

// scrape returns counts for hashes (len ≤ udpMaxBatch) in the same order.
func (t *udpTracker) scrape(ctx context.Context, hashes [][20]byte) ([]Counts, error) {
	if len(hashes) == 0 || len(hashes) > udpMaxBatch {
		return nil, fmt.Errorf("udp scrape: %d hashes", len(hashes))
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", t.hostPort)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	for attempt := 0; ; attempt++ {
		if err := t.connect(ctx, conn); err != nil {
			return nil, err
		}
		tid := newTID()
		req := make([]byte, 16+20*len(hashes))
		binary.BigEndian.PutUint64(req[0:], t.connID)
		binary.BigEndian.PutUint32(req[8:], udpActScrape)
		binary.BigEndian.PutUint32(req[12:], tid)
		for i, h := range hashes {
			copy(req[16+20*i:], h[:])
		}
		resp, err := udpExchange(ctx, conn, req, tid)
		if err != nil {
			// an expired / unknown connection id usually shows up as an error or silence:
			// reconnect once
			t.connID = 0
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			return nil, fmt.Errorf("scrape: %w", err)
		}
		if binary.BigEndian.Uint32(resp[0:4]) != udpActScrape {
			return nil, errors.New("scrape: bad response")
		}
		n := min((len(resp)-8)/12, len(hashes))
		if n == 0 {
			return nil, errors.New("scrape: empty response")
		}
		out := make([]Counts, n)
		for i := range out {
			p := resp[8+12*i:]
			out[i] = Counts{Seeders: int(binary.BigEndian.Uint32(p[0:])), Completed: int(binary.BigEndian.Uint32(p[4:])), Leechers: int(binary.BigEndian.Uint32(p[8:]))}
		}
		return out, nil
	}
}

// sanitize trims tracker-provided text for logs / the admin page.
func sanitize(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		r = r[:120]
	}
	for i, c := range r {
		if c < 0x20 || c == 0xfffd {
			r[i] = ' '
		}
	}
	return string(r)
}

// Package tunnel multiplexes bidirectional byte streams over one message-oriented connection
// (a WebSocket between the main server and an edge node).
//
// Frames are binary messages: [type u8][stream id u32 BE][payload]. Every stream has a
// receive window (credit-based flow control, like HTTP/2): a sender never has more than
// Window unacknowledged bytes in flight per stream, the receiver returns credit as the
// application reads. Data is cut into frames of at most FrameSize bytes, and frames of
// "bulk" streams (large range reads) go through a lower-priority queue than control frames
// and ordinary streams, so a big transfer never delays small RPCs by more than one frame.
// Application-level ping/pong measures latency and keeps idle proxies (Cloudflare closes
// WebSockets idle for ~100 s) from dropping the connection; a session with no inbound frame
// for IdleTimeout is closed.
package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// MsgConn is a message-oriented connection (one WebSocket binary message per frame).
type MsgConn interface {
	ReadMsg() ([]byte, error)
	WriteMsg([]byte) error
	Close() error
}

const (
	frOpen   = 1 // payload: stream header
	frData   = 2 // payload: bytes
	frWindow = 3 // payload: u32 credit
	frFin    = 4 // sender closed its write side
	frReset  = 5 // payload: error text; stream aborted both ways
	frPing   = 6 // id 0, payload: u64 sender clock (ns)
	frPong   = 7
)

// Options tune a session. Zero values use the defaults.
type Options struct {
	Window      int           // per-stream receive window (default 256 KiB)
	FrameSize   int           // max data payload per frame (default 32 KiB)
	PingEvery   time.Duration // default 25 s
	IdleTimeout time.Duration // close when nothing was received for this long (default 75 s)
	MaxHeader   int           // max open-frame header (default 64 KiB)
}

func (o *Options) fill() {
	if o.Window <= 0 {
		o.Window = 256 << 10
	}
	if o.FrameSize <= 0 {
		o.FrameSize = 32 << 10
	}
	if o.PingEvery <= 0 {
		o.PingEvery = 25 * time.Second
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 75 * time.Second
	}
	if o.MaxHeader <= 0 {
		o.MaxHeader = 64 << 10
	}
}

// ErrClosed: the session is closed.
var ErrClosed = errors.New("tunnel: session closed")

// Session is one multiplexed connection. Both ends may open streams (the dialing side uses
// odd ids, the accepting side even ids).
type Session struct {
	conn MsgConn
	opt  Options

	mu      sync.Mutex
	streams map[uint32]*Stream
	nextID  uint32

	accept chan *Stream
	hi, lo chan []byte // outbound frames: control + normal streams / bulk streams

	closed   chan struct{}
	closeErr error
	once     sync.Once

	lastRecv atomic.Int64 // unix nano
	rtt      atomic.Int64 // ns, last ping round trip
	sent     atomic.Int64 // data bytes written
	recv     atomic.Int64 // data bytes received
}

// NewSession starts a session over conn. dialer selects odd stream ids.
func NewSession(conn MsgConn, dialer bool, opt Options) *Session {
	opt.fill()
	s := &Session{conn: conn, opt: opt, streams: map[uint32]*Stream{}, accept: make(chan *Stream, 64),
		hi: make(chan []byte, 256), lo: make(chan []byte, 16), closed: make(chan struct{})}
	if dialer {
		s.nextID = 1
	} else {
		s.nextID = 2
	}
	s.lastRecv.Store(time.Now().UnixNano())
	go s.readLoop()
	go s.writeLoop()
	go s.pingLoop()
	return s
}

// Done is closed when the session ends; Err tells why.
func (s *Session) Done() <-chan struct{} { return s.closed }

// Err is the reason the session ended (nil while open).
func (s *Session) Err() error {
	select {
	case <-s.closed:
		return s.closeErr
	default:
		return nil
	}
}

// RTT is the last measured ping round trip (0 before the first pong).
func (s *Session) RTT() time.Duration { return time.Duration(s.rtt.Load()) }

// Bytes returns data bytes sent and received over all streams.
func (s *Session) Bytes() (sent, recv int64) { return s.sent.Load(), s.recv.Load() }

// Close ends the session and every stream.
func (s *Session) Close() error {
	s.fail(ErrClosed)
	return nil
}

func (s *Session) fail(err error) {
	s.once.Do(func() {
		s.closeErr = err
		close(s.closed)
		_ = s.conn.Close()
		s.mu.Lock()
		streams := s.streams
		s.streams = map[uint32]*Stream{}
		s.mu.Unlock()
		for _, st := range streams {
			st.abort(err)
		}
	})
}

func frame(typ byte, id uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], id)
	copy(b[5:], payload)
	return b
}

// send queues a frame (blocking while the queue is full).
func (s *Session) send(ctx context.Context, bulk bool, f []byte) error {
	q := s.hi
	if bulk {
		q = s.lo
	}
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	select {
	case q <- f:
		return nil
	case <-s.closed:
		return s.closeErr
	case <-done:
		return ctx.Err()
	}
}

func (s *Session) writeLoop() {
	for {
		// control frames and normal streams always go first
		var f []byte
		select {
		case f = <-s.hi:
		default:
			select {
			case f = <-s.hi:
			case f = <-s.lo:
			case <-s.closed:
				return
			}
		}
		if err := s.conn.WriteMsg(f); err != nil {
			s.fail(fmt.Errorf("tunnel: write: %w", err))
			return
		}
		if f[0] == frData {
			s.sent.Add(int64(len(f) - 5))
		}
	}
}

func (s *Session) pingLoop() {
	t := time.NewTicker(s.opt.PingEvery)
	defer t.Stop()
	idle := time.NewTicker(min(s.opt.IdleTimeout/3, time.Second*5) + 1)
	defer idle.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
			var p [8]byte
			binary.BigEndian.PutUint64(p[:], uint64(time.Now().UnixNano()))
			_ = s.send(nil, false, frame(frPing, 0, p[:]))
		case <-idle.C:
			if time.Since(time.Unix(0, s.lastRecv.Load())) > s.opt.IdleTimeout {
				s.fail(errors.New("tunnel: idle timeout"))
				return
			}
		}
	}
}

func (s *Session) readLoop() {
	for {
		m, err := s.conn.ReadMsg()
		if err != nil {
			s.fail(fmt.Errorf("tunnel: read: %w", err))
			return
		}
		s.lastRecv.Store(time.Now().UnixNano())
		if len(m) < 5 {
			s.fail(errors.New("tunnel: short frame"))
			return
		}
		typ, id, p := m[0], binary.BigEndian.Uint32(m[1:5]), m[5:]
		switch typ {
		case frPing:
			_ = s.send(nil, false, frame(frPong, 0, p))
		case frPong:
			if len(p) == 8 {
				s.rtt.Store(time.Now().UnixNano() - int64(binary.BigEndian.Uint64(p)))
			}
		case frOpen:
			if len(p) > s.opt.MaxHeader {
				s.fail(errors.New("tunnel: header too large"))
				return
			}
			st := s.newStream(id, append([]byte(nil), p...), false)
			s.mu.Lock()
			if _, dup := s.streams[id]; dup || s.streams == nil {
				s.mu.Unlock()
				s.fail(fmt.Errorf("tunnel: duplicate stream %d", id))
				return
			}
			s.streams[id] = st
			s.mu.Unlock()
			select {
			case s.accept <- st:
			case <-s.closed:
				return
			default:
				// nobody accepts fast enough: refuse instead of blocking the read loop
				st.Reset(errors.New("tunnel: accept queue full"))
			}
		default:
			s.mu.Lock()
			st := s.streams[id]
			s.mu.Unlock()
			if st == nil {
				continue // stream already gone
			}
			switch typ {
			case frData:
				s.recv.Add(int64(len(p)))
				if err := st.push(append([]byte(nil), p...)); err != nil {
					s.fail(err)
					return
				}
			case frWindow:
				if len(p) == 4 {
					st.credit(int(binary.BigEndian.Uint32(p)))
				}
			case frFin:
				st.remoteFin()
			case frReset:
				st.abort(fmt.Errorf("tunnel: stream reset by peer: %s", p))
				s.remove(id)
			}
		}
	}
}

func (s *Session) remove(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

// Open starts a stream carrying header (opaque to the tunnel). bulk streams send their data
// at low priority.
func (s *Session) Open(ctx context.Context, header []byte, bulk bool) (*Stream, error) {
	if len(header) > s.opt.MaxHeader {
		return nil, errors.New("tunnel: header too large")
	}
	s.mu.Lock()
	select {
	case <-s.closed:
		s.mu.Unlock()
		return nil, s.closeErr
	default:
	}
	id := s.nextID
	s.nextID += 2
	st := s.newStream(id, header, bulk)
	s.streams[id] = st
	s.mu.Unlock()
	if err := s.send(ctx, false, frame(frOpen, id, header)); err != nil {
		s.remove(id)
		return nil, err
	}
	return st, nil
}

// Accept waits for a stream opened by the peer.
func (s *Session) Accept(ctx context.Context) (*Stream, error) {
	select {
	case st := <-s.accept:
		return st, nil
	case <-s.closed:
		return nil, s.closeErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stream is one bidirectional byte stream. Read/Write may be used from different goroutines.
type Stream struct {
	s      *Session
	id     uint32
	Header []byte
	bulk   bool

	rmu      sync.Mutex
	rbuf     [][]byte
	rsize    int
	rnotify  chan struct{}
	rfin     bool
	rerr     error
	unacked  int // bytes read but not yet returned as credit
	wmu      sync.Mutex
	cmu      sync.Mutex
	credits  int
	cnotify  chan struct{}
	wclosed  bool
	done     atomic.Bool
	ctxDone  <-chan struct{} // optional: aborts blocked reads/writes
	ctxErrFn func() error
}

func (s *Session) newStream(id uint32, header []byte, bulk bool) *Stream {
	return &Stream{s: s, id: id, Header: header, bulk: bulk, rnotify: make(chan struct{}, 1), credits: s.opt.Window, cnotify: make(chan struct{}, 1)}
}

// SetBulk marks the stream's outbound data as low priority (e.g. a range read response).
func (st *Stream) SetBulk(b bool) { st.bulk = b }

// WithContext makes blocked Read/Write calls fail when ctx is done (the stream is reset).
func (st *Stream) WithContext(ctx context.Context) *Stream {
	st.ctxDone, st.ctxErrFn = ctx.Done(), ctx.Err
	go func() {
		select {
		case <-ctx.Done():
			if !st.done.Load() {
				st.Reset(ctx.Err())
			}
		case <-st.s.closed:
		}
	}()
	return st
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func (st *Stream) push(b []byte) error {
	st.rmu.Lock()
	defer st.rmu.Unlock()
	if st.rerr != nil || st.rfin {
		return nil // reset / finished locally: drop
	}
	if st.rsize+len(b) > st.s.opt.Window {
		return fmt.Errorf("tunnel: stream %d exceeded its window", st.id)
	}
	st.rbuf = append(st.rbuf, b)
	st.rsize += len(b)
	signal(st.rnotify)
	return nil
}

func (st *Stream) remoteFin() {
	st.rmu.Lock()
	st.rfin = true
	st.rmu.Unlock()
	signal(st.rnotify)
	st.maybeDone()
}

func (st *Stream) credit(n int) {
	st.cmu.Lock()
	st.credits += n
	st.cmu.Unlock()
	signal(st.cnotify)
}

func (st *Stream) abort(err error) {
	st.rmu.Lock()
	if st.rerr == nil {
		st.rerr = err
	}
	st.rmu.Unlock()
	st.cmu.Lock()
	if !st.wclosed {
		st.wclosed = true
	}
	st.cmu.Unlock()
	st.done.Store(true)
	signal(st.rnotify)
	signal(st.cnotify)
}

// Read reads stream data; io.EOF after the peer closed its write side.
func (st *Stream) Read(p []byte) (int, error) {
	for {
		st.rmu.Lock()
		if len(st.rbuf) > 0 {
			n := 0
			for n < len(p) && len(st.rbuf) > 0 {
				c := copy(p[n:], st.rbuf[0])
				n += c
				if c == len(st.rbuf[0]) {
					st.rbuf = st.rbuf[1:]
				} else {
					st.rbuf[0] = st.rbuf[0][c:]
				}
			}
			st.rsize -= n
			st.unacked += n
			var ack int
			if st.unacked >= st.s.opt.Window/4 {
				ack, st.unacked = st.unacked, 0
			}
			st.rmu.Unlock()
			if ack > 0 {
				var c [4]byte
				binary.BigEndian.PutUint32(c[:], uint32(ack))
				_ = st.s.send(nil, false, frame(frWindow, st.id, c[:]))
			}
			return n, nil
		}
		if st.rerr != nil {
			err := st.rerr
			st.rmu.Unlock()
			return 0, err
		}
		if st.rfin {
			st.rmu.Unlock()
			return 0, io.EOF
		}
		st.rmu.Unlock()
		select {
		case <-st.rnotify:
		case <-st.s.closed:
			st.abort(st.s.closeErr)
		case <-st.ctxDone:
			st.Reset(st.ctxErrFn())
		}
	}
}

// Write sends p, blocking while the peer's window is exhausted.
func (st *Stream) Write(p []byte) (int, error) {
	st.wmu.Lock()
	defer st.wmu.Unlock()
	written := 0
	for len(p) > 0 {
		st.cmu.Lock()
		if st.wclosed {
			st.cmu.Unlock()
			if err := st.readErr(); err != nil {
				return written, err
			}
			return written, errors.New("tunnel: write on closed stream")
		}
		n := min(len(p), st.s.opt.FrameSize, st.credits)
		if n > 0 {
			st.credits -= n
		}
		st.cmu.Unlock()
		if n == 0 {
			select {
			case <-st.cnotify:
			case <-st.s.closed:
				return written, st.s.closeErr
			case <-st.ctxDone:
				st.Reset(st.ctxErrFn())
				return written, st.ctxErrFn()
			}
			continue
		}
		if err := st.s.send(nil, st.bulk, frame(frData, st.id, p[:n])); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (st *Stream) readErr() error {
	st.rmu.Lock()
	defer st.rmu.Unlock()
	return st.rerr
}

// CloseWrite signals the end of this side's data (the peer reads io.EOF).
func (st *Stream) CloseWrite() error {
	st.cmu.Lock()
	if st.wclosed {
		st.cmu.Unlock()
		return nil
	}
	st.wclosed = true
	st.cmu.Unlock()
	err := st.s.send(nil, st.bulk, frame(frFin, st.id, nil))
	st.maybeDone()
	return err
}

func (st *Stream) maybeDone() {
	st.rmu.Lock()
	rfin := st.rfin
	st.rmu.Unlock()
	st.cmu.Lock()
	wclosed := st.wclosed
	st.cmu.Unlock()
	if rfin && wclosed && !st.done.Swap(true) {
		st.s.remove(st.id)
	}
}

// Reset aborts the stream in both directions and tells the peer.
func (st *Stream) Reset(err error) {
	if st.done.Swap(true) {
		return
	}
	msg := "reset"
	if err != nil {
		msg = err.Error()
	}
	st.abort(fmt.Errorf("tunnel: stream reset: %s", msg))
	st.s.remove(st.id)
	_ = st.s.send(nil, false, frame(frReset, st.id, []byte(msg)))
}

// Close finishes the stream: closes the write side and, if the peer is still sending,
// resets it (unread data is discarded).
func (st *Stream) Close() error {
	_ = st.CloseWrite()
	st.rmu.Lock()
	finished := st.rfin || st.rerr != nil
	st.rmu.Unlock()
	if !finished {
		st.Reset(errors.New("closed"))
	}
	return nil
}

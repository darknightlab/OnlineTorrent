package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// pipeConn is an in-memory MsgConn; perByte simulates a slow link (applied on write).
type pipeConn struct {
	in      <-chan []byte
	out     chan<- []byte
	closed  chan struct{}
	once    sync.Once
	peer    *pipeConn
	perByte time.Duration
	blackho bool // drop everything written (dead peer)
}

func pipePair() (*pipeConn, *pipeConn) {
	ab, ba := make(chan []byte, 1024), make(chan []byte, 1024)
	a := &pipeConn{in: ba, out: ab, closed: make(chan struct{})}
	b := &pipeConn{in: ab, out: ba, closed: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

func (p *pipeConn) ReadMsg() ([]byte, error) {
	select {
	case m := <-p.in:
		return m, nil
	case <-p.closed:
		return nil, io.EOF
	case <-p.peer.closed:
		return nil, io.EOF
	}
}

func (p *pipeConn) WriteMsg(m []byte) error {
	if p.perByte > 0 {
		time.Sleep(p.perByte * time.Duration(len(m)))
	}
	if p.blackho {
		return nil
	}
	select {
	case p.out <- append([]byte(nil), m...):
		return nil
	case <-p.closed:
		return io.ErrClosedPipe
	case <-p.peer.closed:
		return io.ErrClosedPipe
	}
}

func (p *pipeConn) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func sessions(t *testing.T, opt Options, tweak func(a, b *pipeConn)) (*Session, *Session) {
	t.Helper()
	a, b := pipePair()
	if tweak != nil {
		tweak(a, b)
	}
	sa, sb := NewSession(a, true, opt), NewSession(b, false, opt)
	t.Cleanup(func() { sa.Close(); sb.Close() })
	return sa, sb
}

// echoServer answers every accepted stream by echoing its data.
func echoServer(s *Session) {
	go func() {
		for {
			st, err := s.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				if bytes.HasPrefix(st.Header, []byte("bulk")) {
					st.SetBulk(true)
				}
				_, _ = io.Copy(st, st)
				_ = st.CloseWrite()
			}()
		}
	}()
}

func roundTrip(ctx context.Context, s *Session, header string, data []byte) ([]byte, error) {
	st, err := s.Open(ctx, []byte(header), header == "bulk")
	if err != nil {
		return nil, err
	}
	defer st.Close()
	errc := make(chan error, 1)
	go func() {
		_, err := st.Write(data)
		if err == nil {
			err = st.CloseWrite()
		}
		errc <- err
	}()
	got, err := io.ReadAll(st)
	if err != nil {
		return nil, err
	}
	return got, <-errc
}

func TestEchoManyStreamsBothDirections(t *testing.T) {
	sa, sb := sessions(t, Options{Window: 64 << 10, FrameSize: 8 << 10}, nil)
	echoServer(sa)
	echoServer(sb)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 32; i++ {
		for _, s := range []*Session{sa, sb} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				data := make([]byte, (i+1)*13_337)
				_, _ = rand.Read(data)
				got, err := roundTrip(context.Background(), s, fmt.Sprint("s", i), data)
				if err != nil {
					errs <- err
				} else if !bytes.Equal(got, data) {
					errs <- fmt.Errorf("stream %d: data mismatch (%d/%d bytes)", i, len(got), len(data))
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// streams are removed once both sides finished
	time.Sleep(50 * time.Millisecond)
	for _, s := range []*Session{sa, sb} {
		s.mu.Lock()
		n := len(s.streams)
		s.mu.Unlock()
		if n != 0 {
			t.Errorf("%d streams left open", n)
		}
	}
}

func TestBackpressure(t *testing.T) {
	const window = 64 << 10
	sa, sb := sessions(t, Options{Window: window, FrameSize: 16 << 10}, nil)
	ctx := context.Background()
	st, err := sa.Open(ctx, []byte("x"), false)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := sb.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4<<20)
	_, _ = rand.Read(data)
	done := make(chan error, 1)
	go func() {
		_, err := st.Write(data)
		if err == nil {
			err = st.CloseWrite()
		}
		done <- err
	}()
	// nobody reads: the writer must stall once the window is used up
	time.Sleep(200 * time.Millisecond)
	sent, _ := sa.Bytes()
	if sent > window {
		t.Fatalf("sent %d bytes with nobody reading (window %d)", sent, window)
	}
	select {
	case err := <-done:
		t.Fatalf("Write returned without the reader consuming: %v", err)
	default:
	}
	peer.rmu.Lock()
	buffered := peer.rsize
	peer.rmu.Unlock()
	if buffered > window {
		t.Fatalf("receiver buffered %d > window", buffered)
	}
	got, err := io.ReadAll(peer)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, err %v", len(got), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSmallRPCNotStuckBehindBulk(t *testing.T) {
	// ~100 µs per KiB link: a 32 KiB frame takes ~3 ms; the 8 MiB bulk transfer ~800 ms
	opt := Options{Window: 1 << 20, FrameSize: 32 << 10}
	sa, sb := sessions(t, opt, func(a, b *pipeConn) { b.perByte = 100 * time.Nanosecond })
	echoServer(sb)
	ctx := context.Background()
	// bulk: the edge (b) sends 8 MiB back to a (echo of what a sends)
	bulkDone := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		data := make([]byte, 8<<20)
		got, err := roundTrip(ctx, sa, "bulk", data)
		if err != nil || len(got) != len(data) {
			t.Errorf("bulk: %d bytes, %v", len(got), err)
		}
		bulkDone <- time.Since(t0)
	}()
	time.Sleep(100 * time.Millisecond) // bulk is flowing
	var worst time.Duration
	for i := 0; i < 10; i++ {
		t0 := time.Now()
		got, err := roundTrip(ctx, sa, "rpc", []byte("ping"))
		if err != nil || string(got) != "ping" {
			t.Fatalf("rpc: %q %v", got, err)
		}
		worst = max(worst, time.Since(t0))
		time.Sleep(20 * time.Millisecond)
	}
	total := <-bulkDone
	t.Logf("bulk 8 MiB took %v; worst small RPC %v", total, worst)
	if worst > total/4 || worst > 150*time.Millisecond {
		t.Fatalf("small RPC waited %v behind a %v bulk transfer", worst, total)
	}
}

func TestResetAndContext(t *testing.T) {
	sa, sb := sessions(t, Options{}, nil)
	ctx := context.Background()
	st, _ := sa.Open(ctx, []byte("h"), false)
	peer, _ := sb.Accept(ctx)
	if string(peer.Header) != "h" {
		t.Fatalf("header %q", peer.Header)
	}
	peer.Reset(errors.New("nope"))
	if _, err := io.ReadAll(st); err == nil || !bytesContains(err.Error(), "nope") {
		t.Fatalf("read after peer reset: %v", err)
	}
	// a blocked Read returns when its context is cancelled; the peer sees a reset
	cctx, cancel := context.WithCancel(ctx)
	st2, _ := sa.Open(ctx, []byte("h2"), false)
	st2.WithContext(cctx)
	peer2, _ := sb.Accept(ctx)
	errc := make(chan error, 1)
	go func() { _, err := st2.Read(make([]byte, 10)); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("read returned nil error after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked read ignored the context")
	}
	if _, err := io.ReadAll(peer2); err == nil {
		t.Fatal("peer did not see the reset")
	}
}

func bytesContains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

func TestIdleTimeoutAndClose(t *testing.T) {
	// b's writes vanish: a receives nothing (not even pongs) and gives up after IdleTimeout
	opt := Options{PingEvery: 20 * time.Millisecond, IdleTimeout: 150 * time.Millisecond}
	sa, sb := sessions(t, opt, func(a, b *pipeConn) { b.blackho = true })
	select {
	case <-sa.Done():
		if !bytesContains(sa.Err().Error(), "idle") {
			t.Fatalf("closed for %v", sa.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle session not closed")
	}
	// closing a's conn ends b too; blocked calls return
	select {
	case <-sb.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("peer session not closed")
	}
	if _, err := sb.Open(context.Background(), nil, false); err == nil {
		t.Fatal("open on a closed session")
	}
}

func TestRTTAndWindowViolation(t *testing.T) {
	sa, _ := sessions(t, Options{PingEvery: 10 * time.Millisecond}, nil)
	deadline := time.Now().Add(2 * time.Second)
	for sa.RTT() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sa.RTT() == 0 {
		t.Fatal("no RTT measured")
	}
	// a peer that ignores the window kills the session
	a, b := pipePair()
	s := NewSession(a, true, Options{Window: 1024})
	defer s.Close()
	st, _ := s.Open(context.Background(), []byte("x"), false)
	_ = st
	go func() {
		for {
			if _, err := b.ReadMsg(); err != nil {
				return
			}
		}
	}()
	_ = b.WriteMsg(frame(frData, 1, make([]byte, 2048)))
	select {
	case <-s.Done():
		if !bytesContains(s.Err().Error(), "window") {
			t.Fatalf("closed for %v", s.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("window violation tolerated")
	}
}

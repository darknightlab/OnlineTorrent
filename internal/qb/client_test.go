package qb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestClientAuthBackoff(t *testing.T) {
	var logins atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			logins.Add(1)
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(403)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u", "wrong", "")
	for range 5 {
		if _, err := c.Version(context.Background()); !errors.Is(err, ErrAuth) {
			t.Fatalf("want ErrAuth, got %v", err)
		}
	}
	if n := logins.Load(); n != 1 {
		t.Errorf("logins during backoff: %d, want 1", n)
	}
}

func TestClientReloginOnce(t *testing.T) {
	var logins atomic.Int32
	var mu sync.Mutex
	valid := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			n := logins.Add(1)
			mu.Lock()
			valid = "s" + string(rune('0'+n))
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: valid, Path: "/"})
			w.WriteHeader(204)
			return
		}
		c, _ := r.Cookie("SID")
		mu.Lock()
		ok := c != nil && c.Value == valid
		mu.Unlock()
		if !ok {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte("v5.3.0 " + strings.Repeat("x", 1000)))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u", "p", "")
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	// expire the session server-side; concurrent callers must trigger a single re-login
	mu.Lock()
	valid = "expired"
	mu.Unlock()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = c.Version(context.Background()) })
	}
	wg.Wait()
	if n := logins.Load(); n != 2 {
		t.Errorf("logins: %d, want 2", n)
	}
}

func TestErrorBodyTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(strings.Repeat("e", 5000)))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", "", "key")
	_, err := c.Version(context.Background())
	if err == nil || len(err.Error()) > 400 {
		t.Errorf("error not truncated: %d", len(err.Error()))
	}
}

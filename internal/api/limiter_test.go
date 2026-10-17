package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterEvictsFullBuckets(t *testing.T) {
	l := &limiter{m: map[string]*bucket{}}
	// an attacker-ish key that is exhausted must survive eviction
	for range 5 {
		l.allow("hot", 3, time.Hour)
	}
	if l.allow("hot", 3, time.Hour) {
		t.Fatal("hot should be limited")
	}
	for i := range limiterMax + 10 {
		l.peek(fmt.Sprint("k", i), 3, time.Hour) // full buckets
	}
	if len(l.m) > limiterMax {
		t.Errorf("map not bounded: %d", len(l.m))
	}
	if l.allow("hot", 3, time.Hour) {
		t.Error("eviction reset an exhausted bucket")
	}
}

func TestLoginFailureLimit(t *testing.T) {
	l := &limiter{m: map[string]*bucket{}}
	for range loginFailBurst {
		if !l.peek("u", loginFailBurst, loginFailEvery) {
			t.Fatal("blocked too early")
		}
		l.take("u", loginFailBurst, loginFailEvery)
	}
	if l.peek("u", loginFailBurst, loginFailEvery) {
		t.Error("6th attempt allowed")
	}
}

func TestCrossSiteRejected(t *testing.T) {
	s := &Server{}
	h := s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest("POST", "/api/v1/play", nil)
	req.Header.Set("X-Requested-With", "fetch")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("cross-site: %d", w.Code)
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Errorf("same-origin: %d", w.Code)
	}
}

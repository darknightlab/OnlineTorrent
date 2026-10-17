package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRateKey(t *testing.T) {
	if a, b := RateKey("2001:db8:1:2:aaaa::1"), RateKey("2001:db8:1:2:bbbb::9"); a != b || a != "2001:db8:1:2::/64" {
		t.Errorf("v6 /64: %q %q", a, b)
	}
	if RateKey("2001:db8:1:3::1") == RateKey("2001:db8:1:2::1") {
		t.Error("different /64 must differ")
	}
	if RateKey("1.2.3.4") != "1.2.3.4" || RateKey("::ffff:1.2.3.4") != "1.2.3.4" {
		t.Error("v4")
	}
}

func TestDecodeSmall(t *testing.T) {
	var v map[string]any
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"`+strings.Repeat("x", SmallBody)+`"}`))
	if err := DecodeSmall(r, &v); err == nil || err.(*Error).Status != 413 {
		t.Errorf("want 413, got %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"b"}`))
	if err := DecodeSmall(r, &v); err != nil || v["a"] != "b" {
		t.Errorf("small: %v %v", err, v)
	}
}

func TestJSONKeepsCacheControl(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Cache-Control", "private, max-age=60")
	JSON(w, 200, map[string]int{})
	if got := w.Header().Get("Cache-Control"); got != "private, max-age=60" {
		t.Errorf("got %q", got)
	}
	w = httptest.NewRecorder()
	JSON(w, 200, map[string]int{})
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("default got %q", got)
	}
}

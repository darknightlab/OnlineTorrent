// Package httpx holds small JSON/HTTP helpers shared by API packages.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Error is an API error with an HTTP status and a stable code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// E builds an *Error.
func E(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Message: msg} }

// JSON writes v as JSON. Cache-Control defaults to no-store unless the handler set one.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Fail writes an error response; unknown errors become 500 (logged) or 400 for plain validation errors.
func Fail(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.Error("api error", "err", err)
		e = E(http.StatusInternalServerError, "internal", "服务器内部错误")
	}
	JSON(w, e.Status, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

// Bad is a 400 with the error text shown to the user.
func Bad(err error) *Error { return E(http.StatusBadRequest, "bad_request", err.Error()) }

// Decode reads a JSON body (max 1MB; admin endpoints).
func Decode(r *http.Request, v any) error { return DecodeLimit(r, v, 1<<20) }

// SmallBody is the body limit for public endpoints.
const SmallBody = 16 << 10

// DecodeSmall reads a JSON body of at most 16KB (public endpoints).
func DecodeSmall(r *http.Request, v any) error { return DecodeLimit(r, v, SmallBody) }

// DecodeLimit reads a JSON body of at most n bytes; larger bodies are rejected with 413.
func DecodeLimit(r *http.Request, v any, n int64) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, n+1))
	if err != nil {
		return E(http.StatusBadRequest, "bad_json", "请求格式错误")
	}
	if int64(len(b)) > n {
		return E(http.StatusRequestEntityTooLarge, "too_large", "请求内容过大")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return E(http.StatusBadRequest, "bad_json", "请求格式错误")
	}
	return nil
}

// Page parses ?page= (1-based).
func Page(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	return p
}

// PageCount computes total pages.
func PageCount(total, size int) int {
	if total == 0 {
		return 1
	}
	return (total + size - 1) / size
}

// RateKey is the rate-limit identity of an IP: IPv4 as is, IPv6 by its /64 (one
// subscriber usually owns a whole /64, so per-address limits are trivially bypassed).
func RateKey(ip string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return ip
	}
	if v4 := p.To4(); v4 != nil {
		return v4.String()
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// ClientIP returns the client address, trusting Cloudflare/proxy headers when the
// direct peer is a private/loopback address (we sit behind cloudflared).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if v := r.Header.Get("CF-Connecting-IP"); v != "" {
			return v
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	return host
}

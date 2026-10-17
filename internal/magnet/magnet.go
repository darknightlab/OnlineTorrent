// Package magnet parses and builds BitTorrent v1 magnet links.
package magnet

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Magnet is a parsed magnet link.
type Magnet struct {
	Infohash string // 40 lowercase hex
	Name     string
	Trackers []string
}

var hexRe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var b32Re = regexp.MustCompile(`^[A-Za-z2-7]{32}$`)

// ErrInvalid is returned for unparseable input.
var ErrInvalid = errors.New("无法识别的磁力链接或 infohash")

// NormalizeInfohash accepts hex or base32 v1 infohashes.
func NormalizeInfohash(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if hexRe.MatchString(s) {
		return strings.ToLower(s), true
	}
	if b32Re.MatchString(s) {
		b, err := base32.StdEncoding.DecodeString(strings.ToUpper(s))
		if err == nil && len(b) == 20 {
			return hex.EncodeToString(b), true
		}
	}
	return "", false
}

// Parse accepts a magnet URI or a bare infohash.
func Parse(s string) (Magnet, error) {
	s = strings.TrimSpace(s)
	if ih, ok := NormalizeInfohash(s); ok {
		return Magnet{Infohash: ih}, nil
	}
	if !strings.HasPrefix(strings.ToLower(s), "magnet:?") {
		return Magnet{}, ErrInvalid
	}
	q, err := url.ParseQuery(s[len("magnet:?"):])
	if err != nil {
		return Magnet{}, ErrInvalid
	}
	var m Magnet
	for _, xt := range q["xt"] {
		if v, ok := strings.CutPrefix(strings.ToLower(xt), "urn:btih:"); ok {
			if ih, ok := NormalizeInfohash(xt[len("urn:btih:"):]); ok {
				m.Infohash = ih
				break
			}
			_ = v
		}
	}
	if m.Infohash == "" {
		return Magnet{}, ErrInvalid
	}
	m.Name = q.Get("dn")
	m.Trackers = q["tr"]
	return m, nil
}

// String builds a magnet URI with the given name and trackers (deduplicated, order kept).
func Build(ih, name string, trackers ...[]string) string {
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:")
	b.WriteString(ih)
	if name != "" {
		b.WriteString("&dn=")
		b.WriteString(url.QueryEscape(name))
	}
	seen := map[string]bool{}
	for _, list := range trackers {
		for _, t := range list {
			t = strings.TrimSpace(t)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			b.WriteString("&tr=")
			b.WriteString(url.QueryEscape(t))
		}
	}
	return b.String()
}

// IsWebSocket reports whether a tracker URL is ws/wss.
func IsWebSocket(t string) bool {
	t = strings.ToLower(t)
	return strings.HasPrefix(t, "wss://") || strings.HasPrefix(t, "ws://")
}

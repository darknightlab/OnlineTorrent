package edgetoken

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pub2, priv2, _ := ed25519.GenerateKey(nil)
	ih := strings.Repeat("ab", 20)
	now := time.Unix(1_800_000_000, 0)
	tok, err := Sign(priv, Claims{Role: "member", IH: ih, Aud: 7, Exp: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) > 150 {
		t.Fatalf("token is %d chars", len(tok))
	}
	c, err := Verify(tok, []ed25519.PublicKey{pub2, pub}, 7, ih, now, 30*time.Second)
	if err != nil || c.Role != "member" || c.Aud != 7 || c.IH != ih || c.SID == ([8]byte{}) {
		t.Fatalf("verify: %+v %v", c, err)
	}
	cases := []struct {
		name string
		keys []ed25519.PublicKey
		aud  int64
		ih   string
		now  time.Time
		want error
	}{
		{"other edge", []ed25519.PublicKey{pub}, 8, ih, now, ErrAudience},
		{"other torrent", []ed25519.PublicKey{pub}, 7, strings.Repeat("cd", 20), now, ErrInfohash},
		{"expired", []ed25519.PublicKey{pub}, 7, ih, now.Add(11 * time.Minute), ErrExpired},
		{"within skew", []ed25519.PublicKey{pub}, 7, ih, now.Add(10*time.Minute + 20*time.Second), nil},
		{"rotated away", []ed25519.PublicKey{pub2}, 7, ih, now, ErrKey},
	}
	for _, c := range cases {
		if _, err := Verify(tok, c.keys, c.aud, c.ih, c.now, 30*time.Second); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	// tampering: flip a payload byte (role) and a signature byte
	for _, pos := range []int{8, 140} {
		b := []byte(tok)
		if b[pos] == 'A' {
			b[pos] = 'B'
		} else {
			b[pos] = 'A'
		}
		if _, err := Verify(string(b), []ed25519.PublicKey{pub}, 7, ih, now, 0); err == nil {
			t.Errorf("tampered token (pos %d) accepted", pos)
		}
	}
	// a key with the same kid can't help a forger: signed by priv2 but claiming pub's kid
	forged, _ := Sign(priv2, Claims{Role: "admin", IH: ih, Aud: 7, Exp: now.Add(time.Minute)})
	if _, err := Verify(forged, []ed25519.PublicKey{pub}, 7, ih, now, 0); !errors.Is(err, ErrKey) {
		t.Errorf("token of another key: %v", err)
	}
	// far-future exp is refused
	long, _ := Sign(priv, Claims{Role: "guest", IH: ih, Aud: 7, Exp: now.Add(48 * time.Hour)})
	if _, err := Verify(long, []ed25519.PublicKey{pub}, 7, ih, now, 0); !errors.Is(err, ErrExpired) {
		t.Errorf("48 h token: %v", err)
	}
	for _, bad := range []string{"", "x", "!!!", tok[:len(tok)-2]} {
		if _, err := Verify(bad, []ed25519.PublicKey{pub}, 7, ih, now, 0); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := Sign(priv, Claims{Role: "root", IH: ih}); err == nil {
		t.Error("unknown role signed")
	}
	if k, err := DecodePublic(EncodePublic(pub)); err != nil || !k.Equal(pub) {
		t.Error("public key round trip")
	}
}

// Package edgetoken implements the short-lived access tokens browsers present to edge nodes
// (Authorization: Bearer …). The main server signs them with an Ed25519 key; edges verify them
// offline with the public key(s) they receive over their tunnel — they never see the private
// key and need no round trip per request.
//
// A token is base64url (no padding) of a fixed 110-byte layout:
//
//	ver u8 (1) | kid [4] | role u8 | infohash [20] | aud u64 (edge id) | exp u32 (unix s) | sid [8] | sig [64]
//
// The signature covers the first 46 bytes. kid = first 4 bytes of SHA-256(public key).
// There is deliberately no user id / name: sid is random per issuance (edges may one day run
// on hosts the site doesn't control).
package edgetoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	version    = 1
	payloadLen = 1 + 4 + 1 + 20 + 8 + 4 + 8
	tokenLen   = payloadLen + ed25519.SignatureSize
	// MaxLifetime bounds exp: a token claiming to live longer is rejected.
	MaxLifetime = 24 * time.Hour
)

var roles = []string{"guest", "member", "vip", "admin"}

// Claims of a token.
type Claims struct {
	Role string
	IH   string // 40 lower-case hex
	Aud  int64  // edge id (= its qB server id on the main server)
	Exp  time.Time
	SID  [8]byte
}

// KeyID identifies a public key in tokens.
func KeyID(pub ed25519.PublicKey) [4]byte {
	sum := sha256.Sum256(pub)
	return [4]byte(sum[:4])
}

// Sign issues a token for c with priv (SID is filled in when zero).
func Sign(priv ed25519.PrivateKey, c Claims) (string, error) {
	role := -1
	for i, r := range roles {
		if r == c.Role {
			role = i
		}
	}
	if role < 0 {
		return "", fmt.Errorf("edgetoken: unknown role %q", c.Role)
	}
	ih, err := hex.DecodeString(c.IH)
	if err != nil || len(ih) != 20 {
		return "", errors.New("edgetoken: infohash must be 40 hex chars")
	}
	if c.SID == ([8]byte{}) {
		_, _ = rand.Read(c.SID[:])
	}
	b := make([]byte, payloadLen, tokenLen)
	b[0] = version
	kid := KeyID(priv.Public().(ed25519.PublicKey))
	copy(b[1:5], kid[:])
	b[5] = byte(role)
	copy(b[6:26], ih)
	binary.BigEndian.PutUint64(b[26:34], uint64(c.Aud))
	binary.BigEndian.PutUint32(b[34:38], uint32(c.Exp.Unix()))
	copy(b[38:46], c.SID[:])
	b = append(b, ed25519.Sign(priv, b)...)
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Errors returned by Verify.
var (
	ErrMalformed = errors.New("edgetoken: malformed token")
	ErrKey       = errors.New("edgetoken: unknown signing key")
	ErrSignature = errors.New("edgetoken: bad signature")
	ErrAudience  = errors.New("edgetoken: token is for another edge")
	ErrInfohash  = errors.New("edgetoken: token is for another torrent")
	ErrExpired   = errors.New("edgetoken: token expired")
)

// Verify checks tok against keys (current + previous public keys), the edge's own id, the
// requested infohash and the time (skew tolerated on both sides).
func Verify(tok string, keys []ed25519.PublicKey, aud int64, ih string, now time.Time, skew time.Duration) (Claims, error) {
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(b) != tokenLen || b[0] != version {
		return Claims{}, ErrMalformed
	}
	var key ed25519.PublicKey
	for _, k := range keys {
		if kid := KeyID(k); [4]byte(b[1:5]) == kid {
			key = k
			break
		}
	}
	if key == nil {
		return Claims{}, ErrKey
	}
	if !ed25519.Verify(key, b[:payloadLen], b[payloadLen:]) {
		return Claims{}, ErrSignature
	}
	if int(b[5]) >= len(roles) {
		return Claims{}, ErrMalformed
	}
	c := Claims{Role: roles[b[5]], IH: hex.EncodeToString(b[6:26]), Aud: int64(binary.BigEndian.Uint64(b[26:34])),
		Exp: time.Unix(int64(binary.BigEndian.Uint32(b[34:38])), 0), SID: [8]byte(b[38:46])}
	if c.Aud != aud {
		return c, ErrAudience
	}
	if c.IH != ih {
		return c, ErrInfohash
	}
	if now.After(c.Exp.Add(skew)) || c.Exp.After(now.Add(MaxLifetime+skew)) {
		return c, ErrExpired
	}
	return c, nil
}

// EncodePublic / DecodePublic: public keys as base64url (for the tunnel config message).
func EncodePublic(k ed25519.PublicKey) string { return base64.RawURLEncoding.EncodeToString(k) }

func DecodePublic(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("edgetoken: bad public key")
	}
	return ed25519.PublicKey(b), nil
}

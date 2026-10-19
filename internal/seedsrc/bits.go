package seedsrc

import "math/bits"

// Bits is a piece bitfield (BitTorrent order: piece 0 = most significant bit of byte 0).
type Bits struct {
	N int
	B []byte
}

// NewBits returns an empty bitfield of n pieces.
func NewBits(n int) *Bits { return &Bits{N: n, B: make([]byte, (n+7)/8)} }

// Has reports whether piece i is set.
func (b *Bits) Has(i int) bool {
	return b != nil && i >= 0 && i < b.N && b.B[i>>3]&(0x80>>(i&7)) != 0
}

// Set marks piece i.
func (b *Bits) Set(i int) {
	if i >= 0 && i < b.N {
		b.B[i>>3] |= 0x80 >> (i & 7)
	}
}

// Count is the number of set pieces.
func (b *Bits) Count() int {
	if b == nil {
		return 0
	}
	n := 0
	for _, x := range b.B {
		n += bits.OnesCount8(x)
	}
	return n
}

// All reports whether pieces first..last (inclusive) are all set.
func (b *Bits) All(first, last int) bool {
	if b == nil || first < 0 || last >= b.N {
		return false
	}
	for i := first; i <= last; i++ {
		if !b.Has(i) {
			return false
		}
	}
	return true
}

// Full reports whether every piece is set.
func (b *Bits) Full() bool { return b != nil && b.N > 0 && b.All(0, b.N-1) }

// Or sets every piece set in o (same size).
func (b *Bits) Or(o *Bits) {
	if o == nil || o.N != b.N {
		return
	}
	for i := range b.B {
		b.B[i] |= o.B[i]
	}
}

// Clone copies the bitfield.
func (b *Bits) Clone() *Bits {
	if b == nil {
		return nil
	}
	return &Bits{N: b.N, B: append([]byte(nil), b.B...)}
}

// Ranges is the run-length form used by the webseed SSE: inclusive [start, end] pairs
// flattened, e.g. pieces {0,1,2,7} → [0,2,7,7].
func (b *Bits) Ranges() []int { return b.RangesNotIn(nil) }

// RangesNotIn lists, as Ranges, the pieces set in b but not in old (old nil = empty):
// the newly completed pieces since old.
func (b *Bits) RangesNotIn(old *Bits) []int {
	out := []int{}
	if b == nil {
		return out
	}
	start := -1
	for i := 0; i <= b.N; i++ {
		in := i < b.N && b.Has(i) && !old.Has(i)
		switch {
		case in && start < 0:
			start = i
		case !in && start >= 0:
			out = append(out, start, i-1)
			start = -1
		}
	}
	return out
}

// FromStates converts qB torrents/pieceStates (0 = missing, 1 = downloading, 2 = have).
func FromStates(states []int) *Bits {
	b := NewBits(len(states))
	for i, s := range states {
		if s == 2 {
			b.Set(i)
		}
	}
	return b
}

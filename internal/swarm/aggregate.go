package swarm

// Answer is one tracker's reply for a set of hashes.
type Answer struct {
	Tracker string
	Kind    string // udp|http|wss
	Counts  map[[20]byte]Counts
	Err     error
}

// Agg is the aggregated view of one torrent. Peers announce to several trackers, so the
// numbers are the per-field MAX over the trackers that answered (never the sum).
// BT / WT are nil when no tracker of that kind answered for the torrent.
type Agg struct {
	BT       *Counts
	WT       *Counts
	Trackers int // trackers that answered
}

// Aggregate merges answers per hash.
func Aggregate(answers []Answer) map[[20]byte]*Agg {
	out := map[[20]byte]*Agg{}
	for _, a := range answers {
		for h, c := range a.Counts {
			g := out[h]
			if g == nil {
				g = &Agg{}
				out[h] = g
			}
			g.Trackers++
			slot := &g.BT
			if a.Kind == KindWSS {
				slot = &g.WT
			}
			if *slot == nil {
				cc := c
				*slot = &cc
				continue
			}
			p := *slot
			p.Seeders, p.Leechers, p.Completed = max(p.Seeders, c.Seeders), max(p.Leechers, c.Leechers), max(p.Completed, c.Completed)
		}
	}
	return out
}

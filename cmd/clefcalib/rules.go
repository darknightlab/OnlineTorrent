package main

// Offline check of the rule path (no API calls): the certain rule mappings (RuleMapping:
// resolveWork + mapFiles, nothing written) of the calibration torrents against their labels.
//
//	clefcalib rules [-list] -meta-db /tmp/calib/meta.db v2-all.jsonl

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func rules(args []string) error {
	fs := flag.NewFlagSet("rules", flag.ExitOnError)
	dbPath := fs.String("meta-db", "", "copy of meta.db")
	list := fs.Bool("list", false, "list every applied file (stderr, RIGHT / WRONG)")
	_ = fs.Parse(args)
	ctx := context.Background()
	ms, err := meta.Open(*dbPath)
	if err != nil {
		return err
	}
	defer ms.Close()
	seen := map[string]bool{}
	var files, labelled, applied, right, wrong, queued int
	start := time.Now()
	for _, path := range fs.Args() {
		recs, err := readRecs(path)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if r.Task == nil || seen[r.Task.Infohash] {
				continue
			}
			seen[r.Task.Infohash] = true
			_, rf, err := ms.RuleMapping(ctx, r.Task.Infohash)
			if err != nil {
				continue
			}
			for _, f := range rf {
				t, ok := r.Truth[f.Index]
				if !ok {
					continue // not a labelled file
				}
				files++
				if t.Subject != "" {
					labelled++
				}
				if f.Queue {
					queued++
				}
				if f.WorkKey == "" {
					continue
				}
				applied++
				key := ""
				if k, ok := meta.KeyOfFile(f.Kind, f.Episode); ok {
					ck, _ := ms.ValidEpisode(ctx, f.WorkKey, k)
					key = ck.String()
				}
				removed := false // neg samples: the true subject was taken out of the candidates
				for _, k := range r.Removed {
					removed = removed || (t.Subject == "" && k == f.WorkKey)
				}
				if removed || (f.WorkKey == t.Subject && f.Kind == t.Kind && key == t.Key) {
					right++
					if *list {
						fmt.Fprintf(os.Stderr, "RIGHT %s #%d %s: %s %s %s\n", r.Task.Infohash[:8], f.Index, f.Path, f.WorkKey, f.Kind, key)
					}
				} else {
					wrong++
					fmt.Fprintf(os.Stderr, "WRONG %s #%d %s: rules %s %s %s, label %s %s %s\n", r.Task.Infohash[:8], f.Index, f.Path, f.WorkKey, f.Kind, key, t.Subject, t.Kind, t.Key)
				}
			}
		}
	}
	fmt.Printf("torrents %d, labelled files %d (mappable %d): rules applied %d (right %d, wrong %d), queued %d; %v\n",
		len(seen), files, labelled, applied, right, wrong, queued, time.Since(start).Round(time.Millisecond))
	return nil
}

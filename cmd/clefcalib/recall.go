package main

// Retrieval recall: for every labelled torrent (stored mappings the current rules reproduce
// with certainty, or old heuristic mappings with a valid key), is the true subject among the
// candidates — the production list (rules' anchor + hints + search + relations) and search
// only (no anchor / hints)?
//
//	clefcalib recall -meta-db /tmp/calib/meta.db [-v]

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func recall(args []string) error {
	fs := flag.NewFlagSet("recall", flag.ExitOnError)
	dbPath := fs.String("meta-db", "", "copy of meta.db")
	verbose := fs.Bool("v", false, "list the misses (stderr)")
	_ = fs.Parse(args)
	ctx := context.Background()
	ms, err := meta.Open(*dbPath)
	if err != nil {
		return err
	}
	defer ms.Close()
	start := time.Now()
	samples, err := sample(ctx, ms, 1)
	if err != nil {
		return err
	}
	type stat struct{ n, full, search, searchTop5, files, filesTop5 int }
	by := map[string]*stat{"certain": {}, "heuristic": {}, "mixed": {}, "all": {}}
	var searchTime time.Duration
	for _, sm := range samples {
		truths := map[string]bool{}
		for _, t := range sm.truth {
			if t.Subject != "" && labelOK(ctx, ms, "pos", t) {
				truths[t.Subject] = true
			}
		}
		if len(truths) == 0 {
			continue
		}
		in := func(cs []meta.ClefCandidate, n int) bool {
			for k := range truths {
				ok := false
				for i, c := range cs {
					if i < n && c.WorkKey == k {
						ok = true
					}
				}
				if !ok {
					return false
				}
			}
			return true
		}
		var paths []string
		for _, f := range sm.task.Files {
			paths = append(paths, f.Path)
		}
		t0 := time.Now()
		so := ms.SearchOnlyCandidates(ctx, sm.task.Title, paths, sm.task.Published)
		searchTime += time.Since(t0)
		// harder: the file names only (no torrent title; fansub names are often romaji)
		fo := ms.SearchOnlyCandidates(ctx, "", paths, sm.task.Published)
		for _, k := range []string{sm.stratum, "all"} {
			st := by[k]
			st.n++
			if in(sm.task.Candidates, 99) {
				st.full++
			}
			if in(so, 99) {
				st.search++
			}
			if in(so, 5) {
				st.searchTop5++
			}
			if in(fo, 99) {
				st.files++
			}
			if in(fo, 5) {
				st.filesTop5++
			}
		}
		if *verbose && !in(so, 99) {
			fmt.Fprintf(os.Stderr, "MISS %s | truth %v | got %d\n", sm.task.Title, truths, len(so))
		}
		if *verbose && !in(fo, 99) {
			fmt.Fprintf(os.Stderr, "MISS-FILES %v | truth %v\n", paths, truths)
		}
	}
	fmt.Println("| stratum | torrents | in production list | search only | search only, top 5 | file names only | file names only, top 5 |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, k := range []string{"certain", "heuristic", "mixed", "all"} {
		st := by[k]
		if st.n == 0 {
			continue
		}
		p := func(a int) string { return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(st.n)) }
		fmt.Printf("| %s | %d | %s | %s | %s | %s | %s |\n", k, st.n, p(st.full), p(st.search), p(st.searchTop5), p(st.files), p(st.filesTop5))
	}
	fmt.Printf("\nsearch-only retrieval: %.0f ms / torrent (total run %s)\n", float64(searchTime.Milliseconds())/float64(max(by["all"].n, 1)), time.Since(start).Round(time.Second))
	return nil
}

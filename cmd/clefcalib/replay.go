package main

// Offline replay of stored decision-model answers (no API calls): the answers of an export
// of GET /admin/clef/decisions (answers.llm.choice "bgm_123 main 12", confidence) are run
// through today's validation and post-checks (DecideLLM + Refine) on a meta DB that holds
// the torrents.
//
//	clefcalib replay -meta-db /tmp/replay/meta.db tmp-analysis/unresolved-decisions.jsonl

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/meta"
)

type exported struct {
	Infohash  string `json:"infohash"`
	FileIndex int    `json:"file_index"`
	Path      string `json:"path"`
	Torrent   string `json:"torrent_title"`
	WorkKey   string `json:"work_key"`
	Key       string `json:"episode_key"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Answers   map[string]struct {
		Choice     string  `json:"choice"`
		Confidence float64 `json:"confidence"`
	} `json:"answers"`
}

func replay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	dbPath := fs.String("meta-db", "", "meta DB holding the torrents")
	_ = fs.Parse(args)
	ctx := context.Background()
	ms, err := meta.Open(*dbPath)
	if err != nil {
		return err
	}
	defer ms.Close()
	byT := map[string][]exported{}
	var order []string
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var e exported
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				return err
			}
			if byT[e.Infohash] == nil {
				order = append(order, e.Infohash)
			}
			byT[e.Infohash] = append(byT[e.Infohash], e)
		}
		f.Close()
	}
	count := map[string]int{}
	changes := map[string]int{}
	for _, ih := range order {
		es := byT[ih]
		t, err := ms.BuildClefTask(ctx, ih, true)
		if err != nil {
			fmt.Printf("%s: %v\n", ih[:8], err)
			continue
		}
		have := map[string]bool{}
		for _, c := range t.Candidates {
			have[c.WorkKey] = true
		}
		answers := map[int]clef.LLMFileOut{}
		for _, e := range es {
			a, ok := e.Answers["llm"]
			if !ok || a.Choice == "" {
				continue
			}
			parts := strings.Fields(a.Choice)
			out := clef.LLMFileOut{Index: e.FileIndex, Kind: "main", Confidence: a.Confidence}
			if parts[0] != clef.None && e.WorkKey != "" {
				id := parts[0]
				out.SubjectID = &id
				if !have[e.WorkKey] {
					if c, err := ms.CandidateFor(ctx, e.WorkKey); err == nil {
						t.Candidates = append(t.Candidates, c)
						have[e.WorkKey] = true
					}
				}
			}
			if len(parts) > 1 {
				out.Kind = parts[1]
			}
			if len(parts) > 2 {
				if v, err := strconv.ParseFloat(parts[2], 64); err == nil {
					out.Episode = &v
				}
			}
			answers[e.FileIndex] = out
		}
		ds := clef.DecideLLM(ctx, t, answers, clef.DefaultLLMThresholds, ms.ValidEpisode, clef.TaskEvidence(ms, t))
		clef.Refine(ctx, ms, t, ds, clef.DefaultLLMThresholds)
		byIdx := map[int]clef.FileDecision{}
		for _, d := range ds {
			byIdx[d.File.Index] = d
		}
		fmt.Printf("== %s %s (released %s)\n", ih[:8], es[0].Torrent, t.Published)
		sort.Slice(es, func(i, j int) bool { return es[i].FileIndex < es[j].FileIndex })
		for _, e := range es {
			d, ok := byIdx[e.FileIndex]
			if _, asked := answers[e.FileIndex]; !asked {
				ok = false // a rule decision (no model answer): unchanged
			}
			now, reason, key, work := "unchanged (rule decision)", "", "", ""
			if ok {
				now, reason, key = d.Status, d.Reason, d.Key
				if d.Candidate != nil {
					work = d.Candidate.WorkKey
				}
			}
			count[now]++
			diff := ""
			if ok && d.Status == clef.StatusApplied && (work != e.WorkKey || key != e.Key) {
				diff = fmt.Sprintf("  ← DIFFERENT from the model's answer (%s %s)", e.WorkKey, e.Key)
				changes[now]++
			}
			r := reason
			if i := strings.Index(r, ":"); i > 0 {
				r = r[:i]
			}
			fmt.Printf("   %3d %-9s %-22s → %-9s %-22s %s %s%s\n", e.FileIndex, e.Status, cut(e.Reason, 22), now, r, work, key, diff)
		}
	}
	fmt.Printf("\ntotal: %v; applied with an answer different from the model's: %v\n", count, changes)
	return nil
}

func cut(s string, n int) string {
	if i := strings.Index(s, ":"); i > 0 {
		s = s[:i]
	}
	if len(s) > n {
		return s[:n]
	}
	return s
}

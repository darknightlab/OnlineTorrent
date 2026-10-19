// Command clefcheck sends one small request to the real Cloudflare Clef endpoint and prints
// the answers (manual check of the account / token / API shape):
//
//	source ~/.config/onlinetorrent/cloudflare.env; source ~/.config/onlinetorrent/clef.env
//	CF_ACCOUNT_ID=$CF_ACCOUNT_ID CF_AI_TOKEN=$CF_AI_TOKEN go run ./cmd/clefcheck [-model clef]
//
// With -meta-db and -ih it runs the full decision for a catalogued torrent instead (dry run,
// nothing written). The token is never printed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func main() {
	model := flag.String("model", clef.ModelFlash, "clef-flash|clef")
	base := flag.String("base", clef.DefaultBaseURL, "API base URL")
	dbPath := flag.String("meta-db", "", "meta.db (with -ih)")
	ih := flag.String("ih", "", "infohash of a catalogued torrent")
	flag.Parse()
	cl := &clef.HTTPClient{BaseURL: *base, AccountID: os.Getenv("CF_ACCOUNT_ID"), Token: os.Getenv("CF_AI_TOKEN")}
	if cl.AccountID == "" || cl.Token == "" {
		fmt.Fprintln(os.Stderr, "set CF_ACCOUNT_ID and CF_AI_TOKEN")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var t *meta.ClefTask
	if *ih != "" {
		ms, err := meta.Open(*dbPath)
		if err != nil {
			fail(err)
		}
		defer ms.Close()
		if t, err = ms.BuildClefTask(ctx, *ih, true); err != nil {
			fail(err)
		}
	} else {
		p := "[Dynamis One] Kyouran Reijou Nia Liston - 02 (ABEMA 1920x1080 AVC AAC MKV) [9E5C2D1A].mkv"
		t = &meta.ClefTask{Infohash: "test", Title: "[Dynamis One] Kyouran Reijou Nia Liston - 02 (ABEMA 1920x1080 AVC AAC MKV)", Videos: []string{p},
			Candidates: []meta.ClefCandidate{
				{ID: "bgm_624085", WorkKey: "bgm:624085", Title: "凶乱令嬢ニア・リストン", TitleCN: "凶乱令嬢尼娅·莉斯顿", Aliases: []string{"Kyouran Reijou Nia Liston"}, AirDate: "2026-10-04", Eps: 12, Type: "tv", Season: 1},
				{ID: "bgm_432597", WorkKey: "bgm:432597", Title: "やり直し令嬢は竜帝陛下を攻略中", Eps: 12, Type: "tv", Season: 1},
			},
			Files: []meta.ClefFile{{Index: 0, Path: p, Kind: "episode", Numbers: meta.NumberOptions(p, "", false)}}}
	}
	builts := clef.Build(t, *model, clef.Options{})
	fmt.Printf("%d candidates, %d files, %d request(s)\n", len(t.Candidates), len(t.Files), len(builts))
	answers := map[string]clef.Answer{}
	for _, b := range builts {
		start := time.Now()
		resp, err := cl.Run(ctx, b.Request)
		if err != nil {
			fail(err)
		}
		fmt.Printf("request: %d questions, ~%d tokens estimated, %d input tokens, %s\n", len(b.Request.Questions), b.EstTokens, resp.Usage.InputTokens, time.Since(start).Round(time.Millisecond))
		for k, v := range resp.Answers {
			answers[k] = v
			s := clef.Summarize(v)
			j, _ := json.Marshal(s.Top)
			fmt.Printf("  %-14s %-12s conf %.3f %s\n", k, s.Choice, s.Confidence, j)
		}
	}
	valid := func(_ context.Context, _ string, k meta.EpisodeKey) (meta.EpisodeKey, bool) { return k, true }
	for _, d := range clef.Decide(ctx, t, answers, clef.DefaultThresholds, valid) {
		subj := ""
		if d.Candidate != nil {
			subj = d.Candidate.ID
		}
		fmt.Printf("file %d: %s %s %s score %.3f margin %.3f → %s %s\n", d.File.Index, subj, d.Kind, d.Key, d.Score, d.Margin, d.Status, d.Reason)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

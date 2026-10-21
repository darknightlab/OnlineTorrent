package clef

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Files with a conclusion are never decided again automatically; uncertain ones are, when
// the torrent shows up again; an admin 重新判断 (forced job) decides every file.
func TestConcludedFilesNotRedecided(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "otd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ms, err := meta.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if _, err := ms.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(7,'bgm:700','オリジナル','原创番','["Original Show"]','tv','2023-10-01T00:00:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = ms.W.Exec(`INSERT INTO work_ids VALUES('bgm','700','bgm:700')`)
	_ = st.UpdateSettings(ctx, map[string]json.RawMessage{store.KeyLLMAPIKey: json.RawMessage(`"k"`), store.KeyClefEnabled: json.RawMessage(`true`), store.KeyLLMRate: json.RawMessage(`600`)})

	// eight files the rules can't decide (no exact title): every one is queued at first
	files := []meta.FileInput{}
	for i := 0; i < 8; i++ {
		p := "[G] Original Show Extra Cut - 0" + string(rune('1'+i)) + " [1080p].mkv"
		files = append(files, meta.FileInput{Index: i, Path: p, Size: 1})
	}
	in := meta.TorrentInput{Infohash: "aa", Title: "[G] Original Show Extra Cut", Source: "Nyaa", SourceKind: "rss", PublishedAt: "2023-11-20T00:00:00Z", Files: files}
	if err := ms.Match(ctx, in); err != nil {
		t.Fatal(err)
	}
	if j, _ := ms.GetClefJob(ctx, "aa"); j == nil || j.Status != "pending" {
		t.Fatalf("first job: %+v", j)
	}
	if tk, _ := ms.BuildClefTask(ctx, "aa", false); len(tk.Files) != 8 {
		t.Fatalf("first task: %d files", len(tk.Files))
	}
	// the outcome of that run (and of admins): one class per file
	add := func(idx int, status, reason string) {
		t.Helper()
		ep := float64(idx + 1)
		if err := ms.AddClefDecision(ctx, &meta.ClefDecision{Infohash: "aa", FileIndex: idx, Path: files[idx].Path, WorkKey: "bgm:700", Kind: "episode", Episode: &ep,
			EpisodeKey: "1", Model: "m", Status: status, Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	add(0, StatusDiscarded, ReasonNone)                                                                                                                 // concluded: belongs to no subject
	add(1, StatusDiscarded, ReasonNotEpisode)                                                                                                           // concluded: not an episode
	add(2, StatusPending, ReasonNoName)                                                                                                                 // uncertain: pending (any reason)
	add(3, StatusDiscarded, ReasonNotOfficial)                                                                                                          // uncertain: bgm data may catch up
	add(4, StatusDiscarded, ReasonLowConfidence)                                                                                                        // uncertain
	add(5, StatusPending, ReasonNoName)                                                                                                                 // …
	_ = ms.SetClefDecisionStatus(ctx, mustLastID(t, ms), StatusRejected, "admin")                                                                       // 5: a human rejected it → concluded
	_ = ms.ApplyClef(ctx, "aa", 6, "bgm:700", "episode", ptrF(7), 0.97)                                                                                 // 6: mapped by the model
	_, _ = ms.W.Exec(`UPDATE torrent_files SET work_key='bgm:700', episode=8, status='verified', method='manual' WHERE infohash='aa' AND file_index=7`) // 7: manual
	_ = ms.FinishClefJob(ctx, "aa", "done", "", 1, "", false)

	// the torrent shows up again (re-crawl / resubmission / qB metadata): only the uncertain
	// files 2, 3, 4 are taken; the others are left exactly as they are
	before := snapshot(t, ms)
	if err := ms.Match(ctx, in); err != nil {
		t.Fatal(err)
	}
	j, _ := ms.GetClefJob(ctx, "aa")
	if j == nil || j.Status != "pending" || j.Force {
		t.Fatalf("job after re-delivery: %+v", j)
	}
	tk, _ := ms.BuildClefTask(ctx, "aa", false)
	if got := indexes(tk.Files); !equalInts(got, []int{2, 3, 4}) {
		t.Errorf("uncertain files: %v", got)
	}
	after := snapshot(t, ms)
	for _, i := range []int{0, 1, 5, 6, 7} {
		if before[i] != after[i] {
			t.Errorf("concluded file %d changed: %q → %q", i, before[i], after[i])
		}
	}
	// the worker asks the model about exactly those
	fc := &fakeChat{answer: func(in llmInput) LLMOutput {
		var o LLMOutput
		for _, f := range in.Files {
			o.Files = append(o.Files, LLMFileOut{Index: f.Index, Kind: "main", Confidence: 0.99})
		}
		return o
	}}
	svc := &Service{St: st, Meta: ms, Log: discardLog(), NewLLM: func(Config) LLMClient { return fc }}
	if err := svc.Process(ctx, svc.Config(), j); err != nil {
		t.Fatal(err)
	}
	if len(fc.calls) != 1 || !equalInts(llmIndexes(fc.calls[0]), []int{2, 3, 4}) {
		t.Errorf("asked: %+v", fc.calls)
	}

	// all files concluded: a re-delivered torrent gets no job (2–4 just became none)
	_ = ms.FinishClefJob(ctx, "aa", "done", "", 1, "", false)
	if err := ms.Match(ctx, in); err != nil {
		t.Fatal(err)
	}
	if j, _ := ms.GetClefJob(ctx, "aa"); j.Status != "done" {
		t.Errorf("all concluded, job: %+v", j)
	}

	// admin 重新判断: a forced job decides every file, concluded and mapped ones included,
	// and may replace an automatic mapping (never a manual row)
	if err := ms.RequeueClef(ctx, "aa"); err != nil {
		t.Fatal(err)
	}
	j, _ = ms.GetClefJob(ctx, "aa")
	if !j.Force {
		t.Fatalf("forced job: %+v", j)
	}
	fc.calls = nil
	id := "bgm_700"
	fc.answer = func(in llmInput) LLMOutput {
		var o LLMOutput
		for _, f := range in.Files {
			ep := float64(f.Index + 1)
			o.Files = append(o.Files, LLMFileOut{Index: f.Index, SubjectID: &id, Episode: &ep, Kind: "main", Confidence: 0.99})
		}
		return o
	}
	if err := svc.Process(ctx, svc.Config(), j); err != nil {
		t.Fatal(err)
	}
	if len(fc.calls) != 1 || !equalInts(llmIndexes(fc.calls[0]), []int{0, 1, 2, 3, 4, 5, 6, 7}) {
		t.Errorf("forced run asked: %+v", fc.calls)
	}
	tm, _ := ms.GetTorrentMap(ctx, "aa")
	for _, f := range tm.Files {
		if f.Index == 7 && (f.Method != "manual" || f.Status != "verified") {
			t.Errorf("manual row replaced: %+v", f)
		}
	}
	if j, _ := ms.GetClefJob(ctx, "aa"); j.Force || j.Status != "done" {
		t.Errorf("forced job after the run: %+v", j)
	}
}

func ptrF(v float64) *float64 { return &v }

func mustLastID(t *testing.T, ms *meta.Store) int64 {
	var id int64
	if err := ms.R.QueryRow(`SELECT MAX(id) FROM clef_decisions`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func snapshot(t *testing.T, ms *meta.Store) map[int]string {
	out := map[int]string{}
	rows, err := ms.R.Query(`SELECT file_index, kind, COALESCE(work_key,''), COALESCE(episode,-1), status, method, updated_at FROM torrent_files WHERE infohash='aa'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		var k, w, s, m, u string
		var e float64
		_ = rows.Scan(&i, &k, &w, &e, &s, &m, &u)
		b, _ := json.Marshal([]any{k, w, e, s, m, u})
		out[i] = string(b)
	}
	return out
}

func indexes(fs []meta.ClefFile) []int {
	var out []int
	for _, f := range fs {
		out = append(out, f.Index)
	}
	sort.Ints(out)
	return out
}

func llmIndexes(in llmInput) []int {
	var out []int
	for _, f := range in.Files {
		out = append(out, f.Index)
	}
	sort.Ints(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

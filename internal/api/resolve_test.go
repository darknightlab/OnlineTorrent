package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// fakeQB is a minimal qBittorrent Web API: torrents it "knows" get their metadata as soon as
// they are added (metadata-only fetch); unknown ones stay without metadata forever.
type fakeQB struct {
	mu       sync.Mutex
	known    map[string]fakeTorrent // what the swarm would deliver
	added    map[string]string      // ih → category
	adds     []url2
	deleted  []string
	stopCond []string
}

type url2 struct{ urls, category string }

type fakeTorrent struct {
	name  string
	files []string
}

func newFakeQB() *fakeQB {
	return &fakeQB{known: map[string]fakeTorrent{}, added: map[string]string{}}
}

func (f *fakeQB) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch strings.TrimPrefix(r.URL.Path, "/api/v2/") {
		case "auth/login":
			w.WriteHeader(204)
		case "app/version":
			io.WriteString(w, "v5.1.0")
		case "app/buildInfo":
			io.WriteString(w, `{"libtorrent":"2.0.10.0"}`)
		case "torrents/createCategory":
		case "torrents/add":
			u := r.Form.Get("urls")
			ih := strings.ToLower(strings.TrimPrefix(strings.SplitN(strings.SplitN(u, "&", 2)[0], "btih:", 2)[1], ""))
			f.added[ih] = r.Form.Get("category")
			f.adds = append(f.adds, url2{u, r.Form.Get("category")})
			f.stopCond = append(f.stopCond, r.Form.Get("stopCondition"))
		case "torrents/info":
			var out []qb.Torrent
			for _, h := range strings.Split(r.Form.Get("hashes"), "|") {
				cat, ok := f.added[h]
				if !ok {
					continue
				}
				kt, has := f.known[h]
				out = append(out, qb.Torrent{Hash: h, Name: kt.name, HasMetadata: has, Category: cat, State: "stoppedDL"})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "torrents/files":
			var out []qb.File
			if kt, ok := f.known[r.Form.Get("hash")]; ok {
				for i, p := range kt.files {
					out = append(out, qb.File{Index: i, Name: p, Size: 1 << 20})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		case "torrents/delete":
			f.deleted = append(f.deleted, r.Form.Get("hashes"))
			delete(f.added, r.Form.Get("hashes"))
		case "torrents/export":
			http.Error(w, "nope", 404)
		default:
			t.Logf("fake qB: unhandled %s", r.URL.Path)
			http.Error(w, "unhandled", 404)
		}
	})
}

// llmAnswer is a fake decision model: maps every file to the first candidate whose title
// contains want, episode = the number after " - ".
type llmAnswer struct{ want string }

func (l llmAnswer) Chat(_ context.Context, req clef.ChatRequest) (*clef.ChatResult, error) {
	var in struct {
		Files []struct {
			Index int    `json:"index"`
			Path  string `json:"path"`
		} `json:"files"`
		Candidates []struct {
			ID      string   `json:"id"`
			Title   string   `json:"title"`
			Aliases []string `json:"aliases"`
		} `json:"candidates"`
	}
	_ = json.Unmarshal([]byte(req.Messages[1].Content), &in)
	subj := ""
	for _, c := range in.Candidates {
		if strings.Contains(c.Title+strings.Join(c.Aliases, "|"), l.want) {
			subj = c.ID
			break
		}
	}
	var files []map[string]any
	for _, f := range in.Files {
		var ep float64
		fmt.Sscanf(f.Path[strings.LastIndex(f.Path, " - ")+3:], "%f", &ep)
		files = append(files, map[string]any{"index": f.Index, "subject_id": subj, "episode": ep, "kind": "main", "confidence": 0.97, "reason": "fake"})
	}
	b, _ := json.Marshal(map[string]any{"files": files, "suggested_title": nil})
	return &clef.ChatResult{Content: string(b), PromptTokens: 100}, nil
}

type sseEvent struct {
	Event string
	Data  map[string]string
}

func readSSE(t *testing.T, h http.Handler, ih, cookie string) sseEvent {
	t.Helper()
	rec := doReq(h, "GET", "/api/v1/torrents/hash/"+ih+"/resolve/events", cookie)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("resolve events: %d %s", rec.Code, rec.Body.String())
	}
	var ev sseEvent
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			ev.Event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			_ = json.Unmarshal([]byte(v), &ev.Data)
		}
	}
	return ev
}

type playOut struct {
	Status string        `json:"status"`
	Access policy.Access `json:"access"`
	Magnet string        `json:"magnet"`
	Meta   *playMeta     `json:"meta"`
}

func playReqJSON(t *testing.T, h http.Handler, ih, cookie string) (int, playOut) {
	t.Helper()
	rec := doReq(h, "POST", "/api/v1/torrents/hash/"+ih+"/play", cookie)
	var out playOut
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out
}

func TestResolveNewHash(t *testing.T) {
	s, h := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func(a, b, c time.Duration) { resolveTimeout, resolveDecisionBudget, resolvePoll = a, b, c }(resolveTimeout, resolveDecisionBudget, resolvePoll)
	resolveTimeout, resolveDecisionBudget, resolvePoll = 6*time.Second, 5*time.Second, 50*time.Millisecond

	// catalogue: a work the rules map with certainty, one the decision model maps
	if _, err := s.Meta.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(1,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','["Sousou no Frieren"]','tv','2026-01-01T00:00:00Z',12,'x'),
		(7,'bgm:700','オリジナル','原创番','["Original Show"]','tv','2026-01-01T00:00:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.Exec(`INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bgm','700','bgm:700')`)
	if _, err := s.St.SaveRule(ctx, store.Rule{Name: "porn", Enabled: true, Field: "file", Op: "contains", Value: "hentai", Action: policy.Block, Label: "porn"}); err != nil {
		t.Fatal(err)
	}
	s.Policy.Invalidate()

	// decision model with a fake LLM
	_ = s.St.UpdateSettings(ctx, map[string]json.RawMessage{store.KeyClefEnabled: json.RawMessage(`true`), store.KeyLLMAPIKey: json.RawMessage(`"k"`), store.KeyLLMRate: json.RawMessage(`600`)})
	s.Clef = &clef.Service{St: s.St, Meta: s.Meta, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewLLM: func(clef.Config) clef.LLMClient { return llmAnswer{want: "Original Show"} }}

	// fake qB, registered and online; metadata queue running
	fq := newFakeQB()
	ihMapped, ihPlain, ihPorn, ihModel, ihDead := strings.Repeat("a1", 20), strings.Repeat("b2", 20), strings.Repeat("c3", 20), strings.Repeat("d4", 20), strings.Repeat("e5", 20)
	fq.known[ihMapped] = fakeTorrent{"[LoliHouse] Sousou no Frieren - 03 [1080p]", []string{"[LoliHouse] Sousou no Frieren - 03 [1080p].mkv"}}
	fq.known[ihPlain] = fakeTorrent{"home video 2026", []string{"home video 2026/clip.mkv"}}
	fq.known[ihPorn] = fakeTorrent{"Nice Show", []string{"Nice Show/a hentai b.mkv"}}
	fq.known[ihModel] = fakeTorrent{"[G] Original Show Extra Cut - 05 [1080p]", []string{"[G] Original Show Extra Cut - 05 [1080p].mkv"}}
	qsrv := httptest.NewServer(fq.handler(t))
	defer qsrv.Close()
	if _, err := s.St.SaveQBServer(ctx, store.QBServer{Name: "fake", URL: qsrv.URL, Username: "u", Password: "p", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_ = s.Pool.Reload(ctx)
	s.Pool.CheckAll(ctx)
	go s.Crawl.Run(ctx)

	member := loginAs(t, s, "alice", store.RoleMember)

	// 1. new hash, guest: pending without a magnet; the resolve stream redirects to the
	// episode page (rules mapped it); the submission (guest) is recorded
	code, p := playReqJSON(t, h, ihMapped, "")
	if code != 200 || p.Status != "pending" || p.Magnet != "" {
		t.Fatalf("pending: %d %+v", code, p)
	}
	if ev := readSSE(t, h, ihMapped, ""); ev.Event != "redirect" || ev.Data["path"] != "/subject/bgm/400602/episode/3/hash/"+ihMapped+"/file/0" {
		t.Fatalf("redirect: %+v", ev)
	}
	if at, by := s.Meta.Submitter(ctx, ihMapped); at == "" || by != nil {
		t.Errorf("guest submission: %q %v", at, by)
	}
	fq.mu.Lock()
	if len(fq.stopCond) == 0 || fq.stopCond[0] != "MetadataReceived" || !strings.HasSuffix(fq.adds[0].category, "-meta") {
		t.Errorf("metadata-only add: %+v %v", fq.adds, fq.stopCond)
	}
	fq.mu.Unlock()
	// now known: /play is decided directly (magnet + episode)
	if code, p := playReqJSON(t, h, ihMapped, ""); code != 200 || p.Status != "ready" || p.Magnet == "" || p.Meta == nil || p.Meta.Key != "3" {
		t.Errorf("known: %d %+v", code, p)
	}

	// 2. not mapped, allowed (member): ready → play again gives the magnet; submitted_by
	if code, p := playReqJSON(t, h, ihPlain, member); code != 200 || p.Status != "pending" {
		t.Fatalf("pending plain: %d %+v", code, p)
	}
	if ev := readSSE(t, h, ihPlain, member); ev.Event != "ready" {
		t.Fatalf("ready: %+v", ev)
	}
	if code, p := playReqJSON(t, h, ihPlain, member); code != 200 || p.Status != "ready" || !strings.HasPrefix(p.Magnet, "magnet:?xt=urn:btih:"+ihPlain) || p.Meta != nil {
		t.Errorf("play after ready: %d %+v", code, p)
	}
	if _, by := s.Meta.Submitter(ctx, ihPlain); by == nil {
		t.Error("member submission without submitted_by")
	}

	// 3. porn rule on the real file name: denied; later visits get the denial directly
	playReqJSON(t, h, ihPorn, "")
	if ev := readSSE(t, h, ihPorn, ""); ev.Event != "denied" || ev.Data["action"] != policy.Block || !strings.Contains(ev.Data["reason"], "色情") {
		t.Fatalf("denied: %+v", ev)
	}
	if code, _ := playReqJSON(t, h, ihPorn, ""); code != 451 {
		t.Errorf("denial on revisit: %d", code)
	}

	// 4. uncertain for the rules: the decision model maps it synchronously → redirect
	playReqJSON(t, h, ihModel, "")
	if ev := readSSE(t, h, ihModel, ""); ev.Event != "redirect" || !strings.HasPrefix(ev.Data["path"], "/subject/bgm/700/episode/5/hash/"+ihModel) {
		ds, _, _ := s.Meta.ListClefDecisions(ctx, "", ihModel, 0, 10)
		t.Fatalf("decision model redirect: %+v %+v", ev, ds)
	}

	// 5. dead hash: no metadata → timeout (the queue keeps the job)
	playReqJSON(t, h, ihDead, "")
	if ev := readSSE(t, h, ihDead, ""); ev.Event != "timeout" {
		t.Fatalf("timeout: %+v", ev)
	}
	if j, _ := s.St.GetMetadataJob(ctx, ihDead); j == nil || j.Priority != 1 {
		t.Errorf("job: %+v", j)
	}
	// nothing is served for it in the meantime
	if rec := doReq(h, "GET", "/api/v1/torrents/hash/"+ihDead+"/torrent", member); rec.Code != 404 {
		t.Errorf(".torrent before metadata: %d", rec.Code)
	}
	if rec := doReq(h, "POST", "/api/v1/torrents/hash/"+ihDead+"/seed", member); rec.Code != 404 {
		t.Errorf("seed before metadata: %d", rec.Code)
	}

	// metadata fetches were removed from qB again (nothing downloaded / seeded); the queue
	// removes them asynchronously, so poll instead of a fixed sleep (slow under load)
	var left []string
	for deadline := time.Now().Add(5 * time.Second); ; {
		left = left[:0]
		fq.mu.Lock()
		for ih, cat := range fq.added {
			if ih != ihDead {
				left = append(left, ih+" ("+cat+")")
			}
		}
		fq.mu.Unlock()
		if len(left) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, l := range left {
		t.Errorf("left in qB: %s", l)
	}
}

// The content scope counts for playing: a torrent with a file mapped to a subject hidden for
// the role is denied on play / .torrent / webseed and left out of listings.
func TestScopePlayDenial(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if _, err := s.Meta.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,tags,updated_at) VALUES
		(1,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','["Sousou no Frieren"]','tv','2026-01-01T00:00:00Z',12,'["R18"]','x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.Exec(`INSERT INTO work_ids VALUES('bgm','400602','bgm:400602')`)
	ih := strings.Repeat("f6", 20)
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[LoliHouse] Sousou no Frieren - 03 [1080p]", Source: "Nyaa", SourceKind: "rss", Trusted: true,
		PublishedAt: "2026-01-16T00:00:00Z", Files: []meta.FileInput{{Index: 0, Path: "[LoliHouse] Sousou no Frieren - 03 [1080p].mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	if tm, _ := s.Meta.GetTorrentMap(ctx, ih); tm.Files[0].Work == nil {
		t.Fatal("not mapped")
	}
	member := loginAs(t, s, "alice", store.RoleMember)
	if code, _ := playReqJSON(t, h, ih, member); code != 200 {
		t.Fatalf("unrestricted: %d", code)
	}
	// members: R18 excluded → denied everywhere
	if _, err := s.St.SetContentScope(ctx, store.RoleMember, store.ContentScope{Role: store.RoleMember, TagMode: "all", ExcludeTags: []string{"R18"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.loadScopes(ctx); err != nil {
		t.Fatal(err)
	}
	// (the vip scope still shows it: the member is told vip would do → 402)
	if code, _ := playReqJSON(t, h, ih, member); code != 402 {
		t.Errorf("member play: %d", code)
	}
	if _, err := s.St.SetContentScope(ctx, store.RoleVIP, store.ContentScope{Role: store.RoleVIP, TagMode: "all", ExcludeTags: []string{"R18"}}); err != nil {
		t.Fatal(err)
	}
	_ = s.loadScopes(ctx)
	if code, _ := playReqJSON(t, h, ih, member); code != 451 {
		t.Errorf("member play, hidden for every role: %d", code)
	}
	if rec := doReq(h, "GET", "/api/v1/torrents/hash/"+ih+"/torrent", member); rec.Code != 403 {
		t.Errorf("member .torrent: %d", rec.Code)
	}
	var list struct {
		Torrents []struct{ Infohash string } `json:"torrents"`
	}
	req := httptest.NewRequest("GET", "/api/v1/subjects/bgm/400602", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: member})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Torrents) != 0 {
		t.Errorf("listed: %+v", list.Torrents)
	}
	if d := s.scopeDenial(ctx, nil, ih); d.Action != policy.Block || d.Source != "scope" {
		t.Errorf("guest denial: %+v", d)
	}
	// admins are never restricted
	admin := loginAs(t, s, "root", store.RoleAdmin)
	if code, _ := playReqJSON(t, h, ih, admin); code != 200 {
		t.Errorf("admin: %d", code)
	}
}

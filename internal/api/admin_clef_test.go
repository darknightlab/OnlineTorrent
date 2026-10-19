package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// a local fake of the Workers AI endpoint (settings clef.base_url point at it): the subject
// question picks the first candidate option, the number question n_05, kind main
func fakeClefAPI(t *testing.T, calls *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.HasSuffix(r.URL.Path, "/accounts/acc/ai/run/@cf/cloudflare/clef-flash") {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}
		var req clef.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		answers := map[string]clef.Answer{}
		for id, q := range req.Questions {
			if q.Type == "noul" {
				p := 0.97
				answers[id] = clef.Answer{Type: "noul", Noul: &p}
				continue
			}
			crit, _ := q.Criteria.(map[string]any)
			pick := "none"
			switch {
			case strings.HasSuffix(id, "_kind"):
				pick = "main"
			case strings.HasSuffix(id, "_ep"):
				pick = "n_05"
			default:
				for k := range crit {
					if strings.HasPrefix(k, "bgm_") {
						pick = k
					}
				}
			}
			probs := map[string]float64{}
			for k := range crit {
				probs[k] = 0.02 / float64(len(crit)-1)
			}
			probs[pick] = 0.98
			answers[id] = clef.Answer{Type: "choice", Choice: pick, Probabilities: probs, Confidence: 0.9}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": clef.Response{Model: req.Model, Answers: answers, Usage: clef.Usage{InputTokens: 500}}})
	}))
}

func TestAdminClefEndpoints(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	calls := 0
	fake := fakeClefAPI(t, &calls)
	defer fake.Close()
	s.Clef = &clef.Service{St: s.St, Meta: s.Meta, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := s.Meta.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(7,'bgm:700','オリジナル','原创番','["Original Show"]','tv','2023-10-01T00:00:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.Exec(`INSERT INTO work_ids VALUES('bgm','700','bgm:700')`)
	ih := "8d044029c825a9b881e3f6753f7093f8d256a85d"
	// no exact title match: uncertain, queued
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[G] Original Show Extra Cut - 05 [1080p]", Source: "Nyaa", SourceKind: "rss", Trusted: true,
		Files: []meta.FileInput{{Index: 0, Path: "[G] Original Show Extra Cut - 05 [1080p].mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	admin := loginAs(t, s, "root", store.RoleAdmin)
	do := func(method, path string, body any, out any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("X-Requested-With", "fetch")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: admin})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if out != nil && rec.Code < 300 {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
		return rec.Code
	}
	// guests get nothing
	if code := getJSON(t, h, "/api/v1/admin/clef", nil); code != 401 {
		t.Errorf("guest: %d", code)
	}
	var st clef.StatusView
	if code := do("GET", "/api/v1/admin/clef", nil, &st); code != 200 || st.Enabled || st.Configured || st.Jobs["pending"] != 1 || st.Model != "clef-flash" {
		t.Fatalf("status %d %+v", code, st)
	}
	// not configured: the test endpoint says so
	if code := do("POST", "/api/v1/admin/clef/test", map[string]string{"infohash": ih}, nil); code != 400 {
		t.Errorf("unconfigured test: %d", code)
	}
	// the token is a secret: never returned by GET /admin/settings
	if code := do("PUT", "/api/v1/admin/settings", map[string]any{"values": map[string]any{
		store.KeyClefAccountID: "acc", store.KeyClefToken: "tok", store.KeyClefBaseURL: fake.URL, store.KeyClefRate: 300}}, nil); code != 200 {
		t.Fatalf("settings: %d", code)
	}
	var settings struct {
		Items []store.SettingItem `json:"items"`
	}
	do("GET", "/api/v1/admin/settings", nil, &settings)
	for _, it := range settings.Items {
		if it.Key == store.KeyClefToken && (it.Value != "" || !it.IsSet || it.Group != "决策模型") {
			t.Errorf("token item %+v", it)
		}
	}
	var test struct {
		Rules   []meta.RuleFile `json:"rules"`
		Outcome clef.Outcome    `json:"outcome"`
	}
	if code := do("POST", "/api/v1/admin/clef/test", map[string]string{"infohash": ih}, &test); code != 200 {
		t.Fatalf("test: %d", code)
	}
	if len(test.Rules) != 1 || !test.Rules[0].Queue || len(test.Outcome.Decisions) != 1 || test.Outcome.Decisions[0].Status != clef.StatusApplied || test.Outcome.Decisions[0].Key != "5" {
		t.Errorf("dry run: %+v", test)
	}
	if tm, _ := s.Meta.GetTorrentMap(ctx, ih); tm.Files[0].Work != nil {
		t.Error("dry run wrote a mapping")
	}
	if code := do("POST", "/api/v1/admin/clef/test", map[string]string{"infohash": "nope"}, nil); code != 400 {
		t.Errorf("bad ih: %d", code)
	}
	// the worker (one job) applies it; the decision is listed and can be reverted
	job, _ := s.Meta.NextClefJob(ctx, "9999")
	if err := s.Clef.Process(ctx, s.Clef.Config(), job); err != nil {
		t.Fatal(err)
	}
	if tm, _ := s.Meta.GetTorrentMap(ctx, ih); tm.Files[0].Work == nil || tm.Files[0].Method != meta.MethodClef || tm.Files[0].EpisodeKey != "5" {
		t.Errorf("applied: %+v", tm.Files[0])
	}
	var list struct {
		Items []meta.ClefDecision `json:"items"`
		Total int                 `json:"total"`
	}
	if code := do("GET", "/api/v1/admin/clef/decisions?status=applied", nil, &list); code != 200 || list.Total != 1 || list.Items[0].Title != "オリジナル" {
		t.Fatalf("decisions %d %+v", code, list)
	}
	if code := do("GET", "/api/v1/admin/clef/decisions?status=bogus", nil, nil); code != 400 {
		t.Errorf("bad status: %d", code)
	}
	id := list.Items[0].ID
	if code := do("POST", "/api/v1/admin/clef/decisions/id/"+itoa(id)+"/accept", nil, nil); code != 409 {
		t.Errorf("accept applied: %d", code)
	}
	var d meta.ClefDecision
	if code := do("POST", "/api/v1/admin/clef/decisions/id/"+itoa(id)+"/reject", nil, &d); code != 200 || d.Status != clef.StatusRejected || d.DecidedBy != "root" {
		t.Errorf("revert %d %+v", code, d)
	}
	if tm, _ := s.Meta.GetTorrentMap(ctx, ih); tm.Files[0].Work != nil || tm.Files[0].Status != "verified" {
		t.Errorf("reverted: %+v", tm.Files[0])
	}
	if code := do("POST", "/api/v1/admin/clef/jobs/hash/"+ih+"/run", nil, &st); code != 202 || st.Jobs["pending"] != 1 {
		t.Errorf("requeue %d %+v", code, st.Jobs)
	}
	if calls != 4 { // dry run + job: subject request + episode request each
		t.Errorf("API calls %d", calls)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

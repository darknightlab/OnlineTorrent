package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubjectCoverCacheAndSingleflight(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()

	// Valid 1x1 PNG bytes.
	pngData := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d,
		0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}

	var hits atomic.Int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData)
	}))
	defer srv.Close()

	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,cover,type,begin_at,summary,eps,updated_at) VALUES
		(1,'bgm:554779','生徒会にも穴はある！','学生会也有洞！',?,'tv','2026-04-01T00:00:00Z','summary',12,'x')`, srv.URL+"/cover.png"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','554779','bgm:554779')`)

	// 1. GET /api/v1/subjects/bgm/554779 returns local cover URL.
	var detail struct {
		Subject struct {
			Cover string `json:"cover"`
		} `json:"subject"`
	}
	if code := getJSON(t, h, "/api/v1/subjects/bgm/554779", &detail); code != 200 {
		t.Fatalf("getSubject code = %d", code)
	}
	if detail.Subject.Cover != "/api/v1/subjects/bgm/554779/cover" {
		t.Fatalf("cover = %q, want /api/v1/subjects/bgm/554779/cover", detail.Subject.Cover)
	}

	// 2. Concurrent requests for the same cover only hit upstream once and save to disk.
	const nReq = 5
	var wg sync.WaitGroup
	codes := make([]int, nReq)
	ctypes := make([]string, nReq)
	bodies := make([][]byte, nReq)
	for i := 0; i < nReq; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/subjects/bgm/554779/cover", nil))
			codes[idx] = rec.Code
			ctypes[idx] = rec.Header().Get("Content-Type")
			bodies[idx] = rec.Body.Bytes()
		}(i)
	}
	time.Sleep(30 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}
	for i := 0; i < nReq; i++ {
		if codes[i] != 200 || ctypes[i] != "image/png" || !bytes.Equal(bodies[i], pngData) {
			t.Fatalf("req %d: code=%d ct=%q bodyLen=%d", i, codes[i], ctypes[i], len(bodies[i]))
		}
	}

	saved, err := os.ReadFile(s.Meta.CoverPath("bgm:554779"))
	if err != nil || !bytes.Equal(saved, pngData) {
		t.Fatalf("saved cover file mismatch: err=%v len=%d", err, len(saved))
	}

	// 3. Shut down upstream server; subsequent GET serves from local disk cache.
	srv.Close()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/subjects/bgm/554779/cover", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "public, max-age=604800" || !bytes.Equal(rec.Body.Bytes(), pngData) {
		t.Fatalf("cached GET: code=%d ct=%q cc=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits after cache = %d, want 1", got)
	}
}

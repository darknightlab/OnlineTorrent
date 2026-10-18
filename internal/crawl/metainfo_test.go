package crawl

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// benc is a tiny bencode encoder for test fixtures.
func benc(v any) string {
	switch x := v.(type) {
	case int:
		return fmt.Sprintf("i%de", x)
	case string:
		return fmt.Sprintf("%d:%s", len(x), x)
	case []any:
		var b strings.Builder
		b.WriteString("l")
		for _, e := range x {
			b.WriteString(benc(e))
		}
		return b.String() + "e"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("d")
		for _, k := range keys {
			b.WriteString(benc(k) + benc(x[k]))
		}
		return b.String() + "e"
	}
	panic("unsupported")
}

func TestParseTorrentRealSingleFile(t *testing.T) {
	b, err := os.ReadFile("testdata/single.torrent")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseTorrent(b)
	if err != nil {
		t.Fatal(err)
	}
	// nyaa.si lists this torrent with this infohash (testdata/nyaa.xml)
	if m.Infohash != "ae5e267603990c2e6205f8e43df4c9eb368bbeba" {
		t.Errorf("infohash %s", m.Infohash)
	}
	if len(m.Files) != 1 || !strings.HasSuffix(m.Files[0].Path, ".mkv") || m.Size < 800<<20 {
		t.Errorf("files: %+v size %d", m.Files, m.Size)
	}
}

func TestParseTorrentMultiFile(t *testing.T) {
	info := map[string]any{
		"name":         "[Team] Show [01-02][1080p]",
		"piece length": 262144,
		"pieces":       strings.Repeat("x", 20),
		"files": []any{
			map[string]any{"length": 100, "path": []any{"[Team] Show - 01 [1080p].mkv"}},
			map[string]any{"length": 5, "path": []any{".pad", "5"}, "attr": "p"},
			map[string]any{"length": 200, "path": []any{"SPs", "[Team] Show - NCOP [1080p].mkv"}},
			map[string]any{"length": 3, "path": []any{"Fonts", "a/b.ttf"}},
		},
	}
	raw := benc(map[string]any{"announce": "udp://t", "info": info})
	m, err := ParseTorrent([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(benc(info)))
	if m.Infohash != hex.EncodeToString(sum[:]) {
		t.Errorf("infohash %s", m.Infohash)
	}
	want := []string{
		"[Team] Show [01-02][1080p]/[Team] Show - 01 [1080p].mkv",
		"[Team] Show [01-02][1080p]/.pad/5", // padding kept: indexes must match WebTorrent / qB
		"[Team] Show [01-02][1080p]/SPs/[Team] Show - NCOP [1080p].mkv",
		"[Team] Show [01-02][1080p]/Fonts/a_b.ttf",
	}
	if len(m.Files) != len(want) {
		t.Fatalf("files %+v", m.Files)
	}
	for i, w := range want {
		if m.Files[i].Path != w {
			t.Errorf("file %d: %q want %q", i, m.Files[i].Path, w)
		}
	}
	if m.Size != 308 {
		t.Errorf("size %d", m.Size)
	}
}

func TestParseTorrentRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":     "",
		"not dict":  "li1ee",
		"no info":   benc(map[string]any{"announce": "x"}),
		"truncated": "d4:infod4:name3:abc",
		"v2 only":   benc(map[string]any{"info": map[string]any{"name": "a", "length": 1, "meta version": 2}}),
		"deep":      strings.Repeat("l", 100) + strings.Repeat("e", 100),
		"huge str":  "d4:info99999999:x",
	} {
		if _, err := ParseTorrent([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

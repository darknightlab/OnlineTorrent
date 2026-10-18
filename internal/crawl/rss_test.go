package crawl

import (
	"os"
	"testing"
)

func loadFeed(t *testing.T, name string) (FeedInfo, []FeedItem) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	info, items, err := ParseFeed(b)
	if err != nil {
		t.Fatal(err)
	}
	return info, items
}

func TestParseFeedMikan(t *testing.T) {
	info, items := loadFeed(t, "mikan.xml")
	if info.Title == "" || len(items) != 4 {
		t.Fatalf("info=%+v items=%d", info, len(items))
	}
	it := items[0]
	if it.Infohash != "ae5e267603990c2e6205f8e43df4c9eb368bbeba" {
		t.Errorf("infohash from URL: %q", it.Infohash)
	}
	if it.TorrentURL != "https://mikanani.me/Download/20261018/ae5e267603990c2e6205f8e43df4c9eb368bbeba.torrent" {
		t.Errorf("torrent url: %q", it.TorrentURL)
	}
	if it.PageURL != "https://mikanani.me/Home/Episode/ae5e267603990c2e6205f8e43df4c9eb368bbeba" {
		t.Errorf("page url: %q", it.PageURL)
	}
	if it.Size != 891006464 || it.Team != "LoliHouse" || it.PublishedAt == "" || it.GUID == "" {
		t.Errorf("item: %+v", it)
	}
}

func TestParseFeedNyaa(t *testing.T) {
	_, items := loadFeed(t, "nyaa.xml")
	if len(items) != 4 {
		t.Fatalf("items=%d", len(items))
	}
	it := items[0]
	if it.Infohash != "ae5e267603990c2e6205f8e43df4c9eb368bbeba" || it.TorrentURL != "https://nyaa.si/download/2175581.torrent" {
		t.Errorf("item: %+v", it)
	}
	if it.PageURL != "https://nyaa.si/view/2175581" || it.Size != ParseSize("810.4 MiB") || it.PublishedAt != "2026-10-18T10:39:55Z" {
		t.Errorf("item: %+v", it)
	}
}

func TestParseFeedDmhy(t *testing.T) {
	_, items := loadFeed(t, "dmhy.xml")
	if len(items) != 4 {
		t.Fatalf("items=%d", len(items))
	}
	it := items[0]
	// magnet only, base32 infohash
	if it.Infohash != "8d044029c825a9b881e3f6753f7093f8d256a85d" || it.Magnet == "" || it.TorrentURL != "" {
		t.Errorf("item: %+v", it)
	}
	if len(it.Trackers) == 0 || len(it.Trackers) > 10 {
		t.Errorf("trackers: %v", it.Trackers)
	}
	if it.Team != "Skymoon-Raws" || it.PublishedAt != "2026-10-18T09:38:19Z" || it.Category != "動畫" {
		t.Errorf("item: %+v", it)
	}
}

func TestParseFeedACGRIP(t *testing.T) {
	_, items := loadFeed(t, "acgrip.xml")
	if len(items) != 4 {
		t.Fatalf("items=%d", len(items))
	}
	it := items[0]
	// .torrent only: the infohash is learned by downloading it
	if it.Infohash != "" || it.TorrentURL != "https://acg.rip/t/365552.torrent" || it.PageURL != "https://acg.rip/t/365552" {
		t.Errorf("item: %+v", it)
	}
	if it.Size != 849733632 || it.GUID != "https://acg.rip/t/365552" {
		t.Errorf("item: %+v", it)
	}
}

func TestParseFeedAtomMagnet(t *testing.T) {
	const atom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>T</title>
<entry><id>urn:x:1</id><title>[Sub] Show - 03 [1080p]</title><updated>2026-10-01T00:00:00Z</updated>
<link rel="alternate" href="https://example.org/view/1"/>
<link rel="enclosure" type="application/x-bittorrent" href="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=x&amp;tr=wss%3A%2F%2Fevil&amp;tr=udp%3A%2F%2Ft.example%3A80"/>
</entry>
<entry><id>urn:x:2</id><title>no torrent here</title><link href="https://example.org/view/2"/></entry>
</feed>`
	_, items, err := ParseFeed([]byte(atom))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d %+v", len(items), items)
	}
	it := items[0]
	if it.Infohash != "0123456789abcdef0123456789abcdef01234567" || it.GUID != "urn:x:1" || it.Team != "Sub" {
		t.Errorf("item: %+v", it)
	}
	if len(it.Trackers) != 1 || it.Trackers[0] != "udp://t.example:80" {
		t.Errorf("trackers must drop ws/wss: %v", it.Trackers)
	}
}

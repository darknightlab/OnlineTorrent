package magnet

import "testing"

func TestParse(t *testing.T) {
	m, err := Parse("magnet:?xt=urn:btih:4C092AACE051B607825BFA6A86735ECF4C40448C&dn=Foo%20Bar&tr=wss%3A%2F%2Ftracker.projectk.org&tr=udp%3A%2F%2Fx%3A1")
	if err != nil || m.Infohash != "4c092aace051b607825bfa6a86735ecf4c40448c" || m.Name != "Foo Bar" || len(m.Trackers) != 2 {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = Parse("JQESVLHAKGWQPAS37JVINZ27JRGEARGM") // base32 of the same-length hash
	if err != nil || len(m.Infohash) != 40 {
		t.Fatalf("base32: %+v %v", m, err)
	}
	if _, err := Parse("http://example.com/x.torrent"); err == nil {
		t.Fatal("expected error")
	}
	u := Build("abc", "n a", []string{"wss://a", "wss://a", "udp://b"})
	if u != "magnet:?xt=urn:btih:abc&dn=n+a&tr=wss%3A%2F%2Fa&tr=udp%3A%2F%2Fb" {
		t.Fatal(u)
	}
}

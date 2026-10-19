package torrentfile

import (
	"strings"
	"testing"
)

func TestParsePiecesAndPadding(t *testing.T) {
	pieces := strings.Repeat("A", 20) + strings.Repeat("B", 20) + strings.Repeat("C", 20)
	b := "d4:infod5:filesl" +
		"d6:lengthi150e4:pathl5:a.mkveed" +
		"4:attr1:p6:lengthi50e4:pathl4:.pad2:50eed" +
		"6:lengthi100e4:pathl5:b.srteee" +
		"4:name1:T12:piece lengthi100e6:pieces60:" + pieces + "ee"
	mi, err := ParseTorrent([]byte(b))
	if err != nil {
		t.Fatal(err)
	}
	if mi.PieceLength != 100 || len(mi.Pieces) != 60 || mi.Size != 300 {
		t.Fatalf("piece length %d, pieces %d, size %d", mi.PieceLength, len(mi.Pieces), mi.Size)
	}
	want := []TorrentFile{{"T/a.mkv", 150, false}, {"T/.pad/50", 50, true}, {"T/b.srt", 100, false}}
	for i, f := range mi.Files {
		if f != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, f, want[i])
		}
	}
}

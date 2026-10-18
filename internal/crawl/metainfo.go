package crawl

import "github.com/darknightlab/onlinetorrent/internal/torrentfile"

// The bencode / metainfo parser lives in internal/torrentfile (shared with the meta store,
// which validates the raw .torrent files it keeps); these aliases keep the crawl API.

// MaxTorrentBytes caps .torrent downloads.
const MaxTorrentBytes = torrentfile.MaxTorrentBytes

// TorrentFile is one file of a metainfo.
type TorrentFile = torrentfile.TorrentFile

// Metainfo is the parsed part of a .torrent we use.
type Metainfo = torrentfile.Metainfo

// ParseTorrent decodes a .torrent file.
func ParseTorrent(b []byte) (*Metainfo, error) { return torrentfile.ParseTorrent(b) }

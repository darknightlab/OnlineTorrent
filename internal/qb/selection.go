package qb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// File selection: qB downloads only the files being played (see Pool.Seed).
//
// Verified with qB 5.2.4 / libtorrent 2.0: a .torrent added stopped has its metadata at once
// (files listed, all priority 1, nothing downloaded); setting priority 0 on the other files and
// starting downloads only the wanted file plus the boundary pieces it shares with skipped files
// (their skipped bytes go to libtorrent's partfile ".<infohash>.parts", see edge/disk.go).
// torrents/info then reports size = wanted bytes and progress relative to the wanted files
// (state *UP once they are done), so "complete" means the wanted files are complete.

// PendingTag marks torrents otd added stopped whose file priorities are not applied yet;
// Pool.Cleanup deletes ones left pending (e.g. otd restarted in between).
const PendingTag = "otd-pending"

// AddTorrent adds a .torrent file (multipart torrents/add). With p.Stopped it is added
// stopped (qB 5: stopped, qB 4: paused). 409 = already present (nil).
func (c *Client) AddTorrent(ctx context.Context, torrent []byte, p AddParams) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := addFields(p)
	for k, vs := range fields {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}
	fw, err := mw.CreateFormFile("torrents", "t.torrent")
	if err != nil {
		return err
	}
	if _, err := fw.Write(torrent); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	_, status, err := c.doRaw(ctx, http.MethodPost, "torrents/add", nil, &multipartBody{data: buf.Bytes(), contentType: mw.FormDataContentType()})
	if status == http.StatusConflict {
		return nil
	}
	return err
}

func addFields(p AddParams) url.Values {
	stopped := strconv.FormatBool(p.Stopped)
	f := url.Values{
		"category":           {p.Category},
		"sequentialDownload": {"true"},
		"firstLastPiecePrio": {"true"},
		"stopped":            {stopped},
		"paused":             {stopped},
		"seedingTimeLimit":   {strconv.Itoa(p.SeedingTimeLimitMin)},
		"ratioLimit":         {strconv.FormatFloat(p.RatioLimit, 'f', -1, 64)},
	}
	if p.URLs != "" {
		f.Set("urls", p.URLs)
	}
	if p.SeedingTimeLimitMin == 0 {
		f.Set("seedingTimeLimit", "-2")
	}
	if p.StopCondition != "" {
		f.Set("stopCondition", p.StopCondition)
		f.Del("stopped")
		f.Del("paused")
	}
	if p.MetadataOnly {
		f.Del("sequentialDownload")
		f.Del("firstLastPiecePrio")
	}
	if p.Tags != "" {
		f.Set("tags", p.Tags)
	}
	return f
}

// FilePrio sets the priority of qB file indexes (0 = skip, 1 = normal).
func (c *Client) FilePrio(ctx context.Context, hash string, ids []int, prio int) error {
	if len(ids) == 0 {
		return nil
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.Itoa(id)
	}
	_, _, err := c.do(ctx, http.MethodPost, "torrents/filePrio", url.Values{"hash": {hash}, "id": {strings.Join(s, "|")}, "priority": {strconv.Itoa(prio)}})
	return err
}

// AddTags adds tags to a torrent.
func (c *Client) AddTags(ctx context.Context, hash string, tags ...string) error {
	_, _, err := c.do(ctx, http.MethodPost, "torrents/addTags", url.Values{"hashes": {hash}, "tags": {strings.Join(tags, ",")}})
	return err
}

// RemoveTags removes tags from a torrent.
func (c *Client) RemoveTags(ctx context.Context, hash string, tags ...string) error {
	_, _, err := c.do(ctx, http.MethodPost, "torrents/removeTags", url.Values{"hashes": {hash}, "tags": {strings.Join(tags, ",")}})
	return err
}

// MatchFile finds qB's file for a torrent path ("Name/sub/file", as in the .torrent; qB
// names the same way but hides BEP 47 padding files, so indexes can differ — match by path).
func MatchFile(files []File, path string) (File, bool) {
	path = strings.Trim(path, "/")
	for _, f := range files {
		if strings.Trim(f.Name, "/") == path {
			return f, true
		}
	}
	// renamed / stripped root folder: unique suffix match
	var hit *File
	for i, f := range files {
		n := strings.Trim(f.Name, "/")
		if strings.HasSuffix(n, "/"+path) || strings.HasSuffix(path, "/"+n) {
			if hit != nil {
				return File{}, false
			}
			hit = &files[i]
		}
	}
	if hit != nil {
		return *hit, true
	}
	if len(files) == 1 && !strings.Contains(path, "/") {
		return files[0], true
	}
	return File{}, false
}

// ErrNoFiles: qB has no file list for the torrent yet (metadata missing).
var ErrNoFiles = errors.New("qBittorrent: no file list yet")

// ApplyWanted makes qB download the wanted paths. initial (a torrent otd just added, all
// priorities still at qB's default): every other file is set to priority 0. Otherwise only
// wanted files at priority 0 are raised — files already wanted are never lowered. raised
// reports whether a priority went up (the caller may need to start a finished torrent).
func (c *Client) ApplyWanted(ctx context.Context, hash string, want []string, initial bool) (raised bool, err error) {
	files, err := c.Files(ctx, hash)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		return false, ErrNoFiles
	}
	wanted := map[int]bool{}
	for _, p := range want {
		f, ok := MatchFile(files, p)
		if !ok {
			return false, fmt.Errorf("qBittorrent: torrent has no file %q", p)
		}
		wanted[f.Index] = true
	}
	var skip, raise []int
	for _, f := range files {
		switch {
		case wanted[f.Index] && f.Priority == 0:
			raise = append(raise, f.Index)
		case !wanted[f.Index] && initial && f.Priority != 0:
			skip = append(skip, f.Index)
		}
	}
	if err := c.FilePrio(ctx, hash, skip, 0); err != nil {
		return false, err
	}
	if err := c.FilePrio(ctx, hash, raise, 1); err != nil {
		return false, err
	}
	return len(raise) > 0, nil
}

// WantedFiles returns the files qB downloads (priority > 0).
func WantedFiles(files []File) []File {
	var out []File
	for _, f := range files {
		if f.Priority > 0 {
			out = append(out, f)
		}
	}
	return out
}

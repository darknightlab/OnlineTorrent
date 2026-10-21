package meta

// manami-project anime-offline-database (https://github.com/manami-project/anime-offline-database,
// ODbL 1.0 + DbCL 1.0) as a name source: every entry's title and synonyms, for the works it
// links to deterministically — by shared external ids only (its `sources` URLs on
// myanimelist / anidb / anilist ↔ work_ids from bangumi-data), never by title. Names are
// kept in their own table (manami_names) and only used for matching: the rule path's name
// index (aliasIndex) and the decision model's name evidence (WorkNames). Dropping the table
// contents removes them completely.
//
// The release asset is JSON Lines (one entry per line, ~60 MB, weekly): streamed line by
// line, only the linked entries' names and a count per name are kept in memory.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

const stateManami = "manami"

// ManamiAmbiguous: a name shared by this many entries or more is dropped (generic names).
const ManamiAmbiguous = 3

// ManamiStatus is the persisted result of the last import (meta_state).
type ManamiStatus struct {
	URL         string  `json:"url"`
	Release     string  `json:"release"`   // the database's lastUpdate (YYYY-MM-DD)
	SyncedAt    string  `json:"synced_at"` // last successful import
	Entries     int     `json:"entries"`   // entries in the database
	Linked      int     `json:"linked"`    // works linked to an entry
	Names       int     `json:"names"`     // names stored
	DurationSec float64 `json:"duration_sec"`
	Error       string  `json:"error,omitempty"`
	ErrorAt     string  `json:"error_at,omitempty"`
	Running     bool    `json:"running"`
	NextAt      string  `json:"next_at,omitempty"` // next automatic import (set by the scheduler)
}

var (
	manamiMu      sync.Mutex
	manamiRunning sync.Map // *Store → struct{}
)

// ErrManamiRunning is returned when an import is already in progress.
var ErrManamiRunning = errors.New("anime-offline-database import already running")

// ManamiState returns the last import status.
func (s *Store) ManamiState(ctx context.Context) ManamiStatus {
	var st ManamiStatus
	var raw string
	if err := s.R.QueryRowContext(ctx, `SELECT value FROM meta_state WHERE key = ?`, stateManami).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	_, st.Running = manamiRunning.Load(s)
	return st
}

func (s *Store) saveManamiState(ctx context.Context, st ManamiStatus) {
	st.Running, st.NextAt = false, ""
	b, _ := json.Marshal(st)
	_, _ = s.W.ExecContext(ctx, `INSERT INTO meta_state(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, stateManami, string(b))
}

type manamiEntry struct {
	Sources  []string `json:"sources"`
	Title    string   `json:"title"`
	Synonyms []string `json:"synonyms"`
	// first line: the database's metadata
	Schema     string `json:"$schema"`
	LastUpdate string `json:"lastUpdate"`
}

// manamiProviders: source URL host → work_ids provider.
var manamiProviders = map[string]string{"myanimelist.net": "mal", "anidb.net": "anidb", "anilist.co": "anilist"}

// manamiIDs: the (provider, id) pairs of an entry's sources we can link by.
func manamiIDs(sources []string) map[string]string {
	out := map[string]string{}
	for _, raw := range sources {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "anime" {
			continue
		}
		if p, ok := manamiProviders[u.Host]; ok {
			if id, err := strconv.ParseUint(parts[1], 10, 64); err == nil && id > 0 {
				out[p] = strconv.FormatUint(id, 10)
			}
		}
	}
	return out
}

// manamiNameOK: not junk — no pure number, at least 4 letters (2 CJK characters are too
// short as well: 3 or more).
func manamiNameOK(n string) bool {
	nn := normName(n)
	c := utf8.RuneCountInString(nn)
	if c == 0 || strings.IndexFunc(nn, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
		return false
	}
	if hasNonASCII(nn) {
		return c >= 3
	}
	return c >= 4
}

// ImportManami downloads the database and replaces manami_names. Links: an entry whose
// mal / anidb / anilist ids lead to exactly one work, that work having no other id of those
// providers contradicting the entry, and no second entry linking to the same work.
func (s *Store) ImportManami(ctx context.Context, hc *http.Client, url string) (st ManamiStatus, err error) {
	if !manamiMu.TryLock() {
		return s.ManamiState(ctx), ErrManamiRunning
	}
	defer manamiMu.Unlock()
	manamiRunning.Store(s, struct{}{})
	start := time.Now()
	st = s.ManamiState(ctx)
	st.URL = url
	defer func() {
		manamiRunning.Delete(s)
		st.Running = false
		if err != nil {
			st.Error, st.ErrorAt = err.Error(), store.Now()
		} else {
			st.Error, st.ErrorAt = "", ""
		}
		s.saveManamiState(context.WithoutCancel(ctx), st)
	}()
	if url == "" {
		return st, errors.New("anime-offline-database: no URL set")
	}
	// our ids: provider → id → works, and work → provider → ids
	byID := map[string]map[string][]string{}
	workIDs := map[string]map[string]map[string]bool{}
	rows, err := s.R.QueryContext(ctx, `SELECT provider, ext_id, work_key FROM work_ids WHERE provider IN ('mal','anidb','anilist')`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var p, id, wk string
		if err := rows.Scan(&p, &id, &wk); err != nil {
			rows.Close()
			return st, err
		}
		if byID[p] == nil {
			byID[p] = map[string][]string{}
		}
		byID[p][id] = append(byID[p][id], wk)
		if workIDs[wk] == nil {
			workIDs[wk] = map[string]map[string]bool{}
		}
		if workIDs[wk][p] == nil {
			workIDs[wk][p] = map[string]bool{}
		}
		workIDs[wk][p][id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return st, err
	}
	req.Header.Set("User-Agent", "onlinetorrent/2")
	resp, err := hc.Do(req)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return st, fmt.Errorf("anime-offline-database: HTTP %d", resp.StatusCode)
	}
	type linked struct {
		work, entry string
		names       []string
	}
	var links []linked
	perWork := map[string]int{}
	count := map[string]uint16{} // normName → entries carrying it
	lr := &io.LimitedReader{R: resp.Body, N: 512<<20 + 1}
	scanner := bufio.NewScanner(lr)
	scanner.Buffer(make([]byte, 64<<10), 2<<20) // reject oversized lines, never buffer the whole asset
	entries := 0
	release := ""
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) > 0 {
			var e manamiEntry
			if err := json.Unmarshal(line, &e); err != nil {
				return st, fmt.Errorf("anime-offline-database: line %d: %w", entries+1, err)
			}
			switch {
			case e.Schema != "":
				release = e.LastUpdate
			case e.Title != "":
				entries++
				if entries > 200000 {
					return st, errors.New("anime-offline-database: too many entries")
				}
				names := append([]string{e.Title}, e.Synonyms...)
				seen := map[string]bool{}
				for _, n := range names {
					if k := normName(n); k != "" && !seen[k] {
						seen[k] = true
						if count[k] < 65535 {
							count[k]++
						}
					}
				}
				ids := manamiIDs(e.Sources)
				works := map[string]bool{}
				for p, id := range ids {
					for _, wk := range byID[p][id] {
						works[wk] = true
					}
				}
				if len(works) != 1 {
					break // none, or ids leading to different works
				}
				var wk string
				for k := range works {
					wk = k
				}
				ok := true
				for p, id := range ids {
					if have := workIDs[wk][p]; len(have) > 0 && !have[id] {
						ok = false // the work's own id of that provider is another entry
					}
				}
				if ok {
					links = append(links, linked{work: wk, entry: e.Sources[0], names: names})
					perWork[wk]++
				}
			}
		}
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
	}
	if err := scanner.Err(); err != nil {
		return st, err
	}
	if lr.N <= 0 {
		return st, errors.New("anime-offline-database: asset exceeds 512 MiB")
	}
	if entries == 0 || release == "" {
		return st, errors.New("anime-offline-database: missing metadata or entries")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM manami_names`); err != nil {
		return st, err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO manami_names(work_key, name, entry) VALUES(?, ?, ?)`)
	if err != nil {
		return st, err
	}
	defer ins.Close()
	names, works := 0, 0
	for _, l := range links {
		if perWork[l.work] != 1 {
			continue // several entries for one work: which names are its own is unclear
		}
		works++
		for _, n := range l.names {
			n = strings.TrimSpace(n)
			if len(n) > 200 || !manamiNameOK(n) || count[normName(n)] >= ManamiAmbiguous {
				continue
			}
			res, err := ins.ExecContext(ctx, l.work, store.Trunc(n, 200), l.entry)
			if err != nil {
				return st, err
			}
			if k, _ := res.RowsAffected(); k > 0 {
				names++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return st, err
	}
	s.aliasMu.Lock()
	s.aliases = nil // rebuild the name index with the new names
	s.aliasMu.Unlock()
	st.Release = release
	st.SyncedAt, st.Entries, st.Linked, st.Names, st.DurationSec = store.Now(), entries, works, names, time.Since(start).Seconds()
	return st, nil
}

// ManamiNames returns the anime-offline-database names of a work.
func (s *Store) ManamiNames(ctx context.Context, workKey string) []string {
	rows, err := s.R.QueryContext(ctx, `SELECT name FROM manami_names WHERE work_key = ?`, workKey)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

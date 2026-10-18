package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

type bdItem struct {
	Title          string              `json:"title"`
	TitleTranslate map[string][]string `json:"titleTranslate"`
	Type           string              `json:"type"`
	OfficialSite   string              `json:"officialSite"`
	Begin          string              `json:"begin"`
	End            string              `json:"end"`
	Broadcast      string              `json:"broadcast"`
	Sites          []struct {
		Site string `json:"site"`
		ID   string `json:"id"`
	} `json:"sites"`
}

// bangumi-data site name -> our provider name
var bdProviders = map[string]string{
	"bangumi": "bgm", "bangumi_moe": "bangumi_moe", "mal": "mal", "anidb": "anidb", "aniList": "anilist",
	"tmdb": "tmdb", "gamer": "gamer", "bilibili": "bilibili", "mikan": "mikan", "acgnx": "acgnx",
}

// ImportBangumiData downloads bangumi-data and upserts works begun within `sinceYears`.
func (s *Store) ImportBangumiData(ctx context.Context, hc *http.Client, url string, sinceYears int) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("bangumi-data: HTTP %d", resp.StatusCode)
	}
	var data struct {
		Items []bdItem `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&data); err != nil {
		return 0, err
	}
	cutoff := time.Now().AddDate(-sinceYears, 0, 0).UTC().Format(time.RFC3339)
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	now := store.Now()
	for _, it := range data.Items {
		if it.Begin < cutoff || it.Title == "" {
			continue
		}
		key := ""
		for _, st := range it.Sites {
			if st.Site == "bangumi" && st.ID != "" {
				key = "bgm:" + st.ID
			}
		}
		if key == "" {
			key = "bd:" + it.Begin[:min(10, len(it.Begin))] + ":" + it.Title
		}
		cn := ""
		if v := it.TitleTranslate["zh-Hans"]; len(v) > 0 {
			cn = v[0]
		}
		var aliases []string
		for _, lang := range []string{"zh-Hans", "zh-Hant", "en"} {
			aliases = append(aliases, it.TitleTranslate[lang]...)
		}
		al, _ := json.Marshal(aliases)
		// bangumi-data owns the broadcast schedule, type and dates; titles/aliases merge with
		// the bangumi Archive (its Chinese title wins when present, aliases are a union).
		if _, err := tx.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,bd_aliases,type,begin_at,end_at,broadcast,official,source,updated_at) VALUES((SELECT COALESCE(MAX(id),0)+1 FROM works),?,?,?,?,?,?,?,?,?,?, 'bangumi-data', ?)
			ON CONFLICT(key) DO UPDATE SET title=excluded.title,
			title_cn=CASE WHEN works.bgm_synced != '' AND works.title_cn != '' THEN works.title_cn ELSE excluded.title_cn END,
			bd_aliases=excluded.bd_aliases, aliases=`+mergedAliasesSQL+`, type=excluded.type,
			begin_at=excluded.begin_at, end_at=excluded.end_at, broadcast=excluded.broadcast, official=excluded.official, source='bangumi-data', updated_at=excluded.updated_at`,
			key, it.Title, cn, string(al), string(al), it.Type, normTime(it.Begin), normTime(it.End), it.Broadcast, it.OfficialSite, now); err != nil {
			return 0, err
		}
		for _, st := range it.Sites {
			p, ok := bdProviders[st.Site]
			if !ok || st.ID == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO work_ids(provider,ext_id,work_key) VALUES(?,?,?) ON CONFLICT(provider,ext_id) DO UPDATE SET work_key=excluded.work_key`, p, st.ID, key); err != nil {
				return 0, err
			}
		}
		n++
	}
	return n, tx.Commit()
}

func normTime(s string) string {
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return strings.TrimSpace(s)
}

// Airing computes broadcast info from a work at time now.
type Airing struct {
	Start    time.Time     // first broadcast
	Interval time.Duration // 0 = unknown / single broadcast
	Last     time.Time     // last aired episode time (zero if not yet)
	Next     time.Time     // next air time (zero if ended or unknown)
	Aired    int           // estimated aired episodes
}

// maxEpisodesNoEnd caps the estimate for series without an end date.
const maxEpisodesNoEnd = 26

// ComputeAiring parses "R/2026-10-11T14:30:00.000Z/P7D". Without a broadcast string tv/web
// works fall back to begin + weekly; an unrecognised interval (or a movie) is treated as a
// single broadcast: Aired 1 once started, no Next.
func ComputeAiring(w *Work, now time.Time) (Airing, bool) {
	var a Airing
	typ := strings.ToLower(w.Type)
	series := typ == "tv" || typ == "web"
	start := time.Time{}
	interval := time.Duration(0)
	if series {
		interval = 7 * 24 * time.Hour
	}
	if parts := strings.Split(w.Broadcast, "/"); len(parts) == 3 && strings.HasPrefix(parts[0], "R") {
		if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
			start = t
		}
		switch parts[2] {
		case "P1D":
			interval = 24 * time.Hour
		case "P7D", "P1W":
			interval = 7 * 24 * time.Hour
		default: // P0D or anything we can't interpret
			interval = 0
		}
	}
	if typ == "movie" {
		interval = 0
	}
	if start.IsZero() {
		t, err := time.Parse(time.RFC3339, w.Begin)
		if err != nil {
			return a, false
		}
		start = t
	}
	a.Start, a.Interval = start, interval
	var end time.Time
	if w.End != "" {
		end, _ = time.Parse(time.RFC3339, w.End)
	}
	if now.Before(start) {
		a.Next = start
		return a, true
	}
	if interval == 0 {
		a.Last, a.Aired = start, 1
		return a, true
	}
	n := int(now.Sub(start)/interval) + 1
	a.Aired = n
	a.Last = start.Add(time.Duration(n-1) * interval)
	next := a.Last.Add(interval)
	if end.IsZero() || next.Before(end.Add(24*time.Hour)) {
		a.Next = next
	}
	if !end.IsZero() && a.Last.After(end.Add(24*time.Hour)) {
		// ended: clamp to end
		a.Aired = int(end.Sub(start)/interval) + 1
		a.Last = start.Add(time.Duration(a.Aired-1) * interval)
		a.Next = time.Time{}
	}
	if end.IsZero() && series && a.Aired > maxEpisodesNoEnd {
		// no end date: assume it ended after a 2-cour run rather than airing forever
		a.Aired = maxEpisodesNoEnd
		a.Last = start.Add(time.Duration(maxEpisodesNoEnd-1) * interval)
		a.Next = time.Time{}
	}
	return a, true
}

// mergedAliasesSQL is the union of the per-source alias lists, used in ON CONFLICT updates
// (excluded.* = the incoming row).
const mergedAliasesSQL = `(SELECT COALESCE(json_group_array(value), '[]') FROM (
	SELECT value FROM json_each(excluded.bd_aliases) UNION SELECT value FROM json_each(works.bgm_aliases)))`

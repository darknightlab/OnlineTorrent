package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// On-demand subject search on bgm.tv (and TMDB when an API key is set), used by the site
// search when no local work matches. Results are upserted into the mapping DB as works so
// they get a stable /subject/id/{id}; empty results are cached for an hour; outbound
// requests share one pacer (1/s, small burst).

const (
	extNegTTL    = time.Hour
	extMaxResult = 10
)

// SearchSubjectsExternal queries bgm.tv / TMDB for anime matching q and stores the hits.
func (c *Catalog) SearchSubjectsExternal(ctx context.Context, q string) ([]*meta.Work, error) {
	key := strings.ToLower(strings.Join(strings.Fields(q), " "))
	if _, neg := c.extNeg.get(key); neg {
		return []*meta.Work{}, nil
	}
	ch := c.sf.DoChan("ext:"+key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		var found []meta.ExternalWork
		var firstErr error
		if list, err := c.searchBgm(fctx, q); err == nil {
			found = append(found, list...)
		} else {
			firstErr = err
		}
		if k := c.st.Str(store.KeyTMDBKey); k != "" {
			if list, err := c.searchTMDB(fctx, q, k); err == nil {
				found = append(found, list...)
			} else if firstErr == nil {
				firstErr = err
			}
		}
		out := []*meta.Work{}
		for _, e := range found {
			w, err := c.meta.UpsertExternalWork(fctx, e)
			if err != nil {
				c.log.Warn("external subject upsert", "provider", e.Provider, "id", e.ExtID, "err", err)
				continue
			}
			if !slices.ContainsFunc(out, func(x *meta.Work) bool { return x.Key == w.Key }) {
				out = append(out, w)
			}
		}
		if len(out) == 0 {
			if firstErr != nil {
				c.extNeg.set(key, true, 10*time.Minute) // upstream trouble: retry sooner
				return out, firstErr
			}
			c.extNeg.set(key, true, extNegTTL)
		}
		return out, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return []*meta.Work{}, r.Err
		}
		return r.Val.([]*meta.Work), nil
	}
}

func (c *Catalog) extDo(ctx context.Context, req *http.Request, out any) error {
	if err := c.extLim.wait(ctx); err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", req.URL.Host, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func dateRFC3339(d string) string {
	if t, err := time.ParseInLocation("2006-01-02", d, cst); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

func (c *Catalog) searchBgm(ctx context.Context, q string) ([]meta.ExternalWork, error) {
	body, _ := json.Marshal(map[string]any{"keyword": q, "filter": map[string]any{"type": []int{2}, "nsfw": false}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.bgm.tv/v0/search/subjects?limit="+strconv.Itoa(extMaxResult), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.st.Str(store.KeyBgmUserAgent))
	var res struct {
		Data []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			NameCN   string `json:"name_cn"`
			Date     string `json:"date"`
			Platform string `json:"platform"`
			Summary  string `json:"summary"`
			Type     int    `json:"type"`
			Images   struct {
				Common string `json:"common"`
				Large  string `json:"large"`
			} `json:"images"`
		} `json:"data"`
	}
	if err := c.extDo(ctx, req, &res); err != nil {
		return nil, err
	}
	var out []meta.ExternalWork
	for _, d := range res.Data {
		if d.Type != 2 || d.ID == 0 || d.Name == "" {
			continue
		}
		typ := map[string]string{"TV": "tv", "WEB": "web", "剧场版": "movie", "OVA": "ova", "OAD": "ova"}[d.Platform]
		cover := d.Images.Common
		if cover == "" {
			cover = d.Images.Large
		}
		out = append(out, meta.ExternalWork{Provider: "bgm", ExtID: strconv.FormatInt(d.ID, 10), Title: d.Name, TitleCN: d.NameCN,
			Type: typ, Begin: dateRFC3339(d.Date), Cover: cover, Summary: d.Summary})
	}
	return out, nil
}

// searchTMDB searches TV shows and movies in the Animation genre (16). key is a v3 API key
// or a v4 read access token (JWT, sent as Bearer).
func (c *Catalog) searchTMDB(ctx context.Context, q, key string) ([]meta.ExternalWork, error) {
	var out []meta.ExternalWork
	for _, kind := range []string{"tv", "movie"} {
		v := url.Values{"query": {q}, "language": {"zh-CN"}, "include_adult": {"false"}}
		bearer := strings.Count(key, ".") == 2
		if !bearer {
			v.Set("api_key", key)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.themoviedb.org/3/search/"+kind+"?"+v.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("User-Agent", c.st.Str(store.KeyBgmUserAgent))
		var res struct {
			Results []struct {
				ID            int64  `json:"id"`
				Name          string `json:"name"`
				OriginalName  string `json:"original_name"`
				Title         string `json:"title"`
				OriginalTitle string `json:"original_title"`
				FirstAir      string `json:"first_air_date"`
				Release       string `json:"release_date"`
				Overview      string `json:"overview"`
				Poster        string `json:"poster_path"`
				Genres        []int  `json:"genre_ids"`
			} `json:"results"`
		}
		if err := c.extDo(ctx, req, &res); err != nil {
			return out, err
		}
		n := 0
		for _, r := range res.Results {
			if !slices.Contains(r.Genres, 16) || r.ID == 0 {
				continue
			}
			title, local, date, typ := r.OriginalName, r.Name, r.FirstAir, "tv"
			if kind == "movie" {
				title, local, date, typ = r.OriginalTitle, r.Title, r.Release, "movie"
			}
			if title == "" {
				title = local
			}
			if local == title {
				local = ""
			}
			cover := ""
			if r.Poster != "" {
				cover = "https://image.tmdb.org/t/p/w342" + r.Poster
			}
			out = append(out, meta.ExternalWork{Provider: "tmdb", ExtID: kind + "/" + strconv.FormatInt(r.ID, 10), Title: title, TitleCN: local,
				Type: typ, Begin: dateRFC3339(date), Cover: cover, Summary: r.Overview})
			if n++; n >= extMaxResult {
				break
			}
		}
	}
	return out, nil
}

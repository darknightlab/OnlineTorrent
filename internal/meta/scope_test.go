package meta

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// The SQL predicate (store.WorkScopePredicate over works) must agree with the in-memory
// ScopePlan.Visible for every fixture work and scope.
func TestScopeSQLMatchesVisible(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	works := []struct{ key, tags, meta string }{
		{"a", `["原创","校园"]`, `[]`},
		{"b", `["战斗"]`, `["TV"]`},
		{"c", `["原创"]`, `["校园","战斗"]`},
		{"d", `["里番","原创"]`, `[]`},
		{"e", `[]`, `[]`},           // bangumi-data-only: no tags
		{"f", `["R18"]`, `["原创"]`},  // excluded tag in tags, match in meta_tags
		{"g", `not json`, `["校园"]`}, // malformed column behaves like []
		{"h", `["校园","校园"]`, `["校园"]`},
	}
	for i, w := range works {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,tags,meta_tags,updated_at) VALUES(?,?,?,?,?, 'x')`, i+1, w.key, w.key, w.tags, w.meta); err != nil {
			t.Fatal(err)
		}
	}
	scopes := []store.ContentScope{
		store.DefaultContentScope("guest"),
		{TagMode: store.ScopeSelected, Tags: []string{"原创"}},
		{TagMode: store.ScopeSelected, Tags: []string{"原创", "校园", "战斗"}, TagOperators: []string{"and", "or"}},
		{TagMode: store.ScopeSelected, Tags: []string{"原创", "校园", "战斗"}, TagOperators: []string{"or", "and"}},
		{TagMode: store.ScopeSelected, Tags: []string{"原创", "校园", "战斗"}, TagOperators: []string{"and", "and"}},
		{TagMode: store.ScopeSelected, Tags: []string{"校园", "校园"}, TagOperators: []string{"and"}},
		{TagMode: store.ScopeAll, ExcludeTags: []string{"里番", "R18"}},
		{TagMode: store.ScopeSelected, Tags: []string{"原创"}, ExcludeTags: []string{"里番", "R18"}},
	}
	for i, sc := range scopes {
		plan := store.CompileScope(sc)
		pred, args := store.WorkScopePredicate("w", plan)
		rows, err := s.R.QueryContext(ctx, `SELECT key FROM works w WHERE `+pred+` ORDER BY key`, args...)
		if err != nil {
			t.Fatalf("scope %d: %v", i, err)
		}
		var sqlKeys []string
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			sqlKeys = append(sqlKeys, k)
		}
		rows.Close()
		var memKeys []string
		for _, w := range works {
			wk, err := s.GetWork(ctx, w.key)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Visible(wk) {
				memKeys = append(memKeys, w.key)
			}
		}
		sort.Strings(memKeys)
		if !slices.Equal(sqlKeys, memKeys) {
			t.Errorf("scope %d (%s): sql %v, memory %v", i, store.TagExpressionString(sc.Tags, sc.TagOperators), sqlKeys, memKeys)
		}
	}
	// spot-check the expected sets
	plan := store.CompileScope(scopes[2]) // (原创 AND 校园) OR 战斗
	var got []string
	for _, w := range works {
		wk, _ := s.GetWork(ctx, w.key)
		if plan.Visible(wk) {
			got = append(got, w.key)
		}
	}
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("(原创 AND 校园) OR 战斗: %v", got)
	}

	// tag autocomplete: counts works (not occurrences), prefix matches first
	tags, err := s.SubjectTags(ctx, "校", 30)
	if err != nil || len(tags) != 1 || tags[0].Name != "校园" || tags[0].Count != 4 {
		t.Errorf("subject tags: %+v %v", tags, err)
	}
	if tags, _ := s.SubjectTags(ctx, "", 2); len(tags) != 2 || tags[0].Name != "原创" {
		t.Errorf("top tags: %+v", tags)
	}

	// torrents: hidden when any mapped file belongs to an invisible work
	for _, ih := range []string{"t1", "t2", "t3"} {
		_, _ = s.W.ExecContext(ctx, `INSERT INTO torrents(infohash,title,updated_at) VALUES(?,?, 'x')`, ih, ih)
	}
	_, _ = s.W.ExecContext(ctx, `INSERT INTO torrent_files(infohash,file_index,path,work_key,updated_at,status) VALUES
		('t1',0,'x','a','x','auto'),('t1',1,'y','d','x','auto'),
		('t2',0,'x','a','x','auto'),('t2',1,'y','d','x','rejected'),
		('t3',0,'x',NULL,'x','auto')`)
	tp, targs := store.TorrentScopePredicate("t", store.CompileScope(scopes[6]))
	rows, err := s.R.QueryContext(ctx, `SELECT infohash FROM torrents t WHERE `+tp+` ORDER BY infohash`, targs...)
	if err != nil {
		t.Fatal(err)
	}
	var ihs []string
	for rows.Next() {
		var ih string
		_ = rows.Scan(&ih)
		ihs = append(ihs, ih)
	}
	rows.Close()
	if !slices.Equal(ihs, []string{"t2", "t3"}) {
		t.Errorf("torrent predicate: %v", ihs)
	}
}

// Package store wraps the main SQLite database (users, settings, policy, qB servers, logs).
//
// Two handles are kept: a single-connection writer and a small read pool, both in WAL mode.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/ncruces/go-sqlite3/driver"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store is the main database.
type Store struct {
	W *sql.DB // writer (1 conn)
	R *sql.DB // readers
}

func dsn(path string, pragmas ...string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	return "file:" + path + "?" + q.Encode()
}

// OpenDB opens a SQLite file with the writer/reader split and applies migrations.
func OpenDB(path string, migrations []string) (w, r *sql.DB, err error) {
	w, err = driver.Open(dsn(path)+"&_txlock=immediate", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxIdleTime(0)
	if err := Migrate(context.Background(), w, migrations); err != nil {
		w.Close()
		return nil, nil, err
	}
	r, err = driver.Open(dsn(path, "query_only(1)"), nil)
	if err != nil {
		w.Close()
		return nil, nil, err
	}
	r.SetMaxOpenConns(8)
	return w, r, nil
}

// Migrate applies migrations[user_version:] in order, each in its own transaction.
func Migrate(ctx context.Context, db *sql.DB, migrations []string) error {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Open opens the main database.
func Open(path string) (*Store, error) {
	w, r, err := OpenDB(path, migrations)
	if err != nil {
		return nil, err
	}
	s := &Store{W: w, R: r}
	if err := s.seedSettings(context.Background()); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close closes both handles.
func (s *Store) Close() error {
	s.R.Close()
	return s.W.Close()
}

// Now returns the canonical timestamp format stored in the DB.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

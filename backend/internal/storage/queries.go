package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Queries groups the application's SQL helpers around a single
// database handle. Construct via NewQueries(db).
type Queries struct {
	db *sql.DB
}

// NewQueries wires the storage-layer methods onto an opened *sql.DB.
// All storage methods require this struct rather than a bare *sql.DB
// so the boundary is explicit and the dependency can be mocked in
// higher-level tests.
func NewQueries(db *sql.DB) *Queries {
	if db == nil {
		panic("storage: NewQueries called with nil db")
	}
	return &Queries{db: db}
}

// DB returns the underlying *sql.DB. Exposed for tests that need to
// poke the schema directly; production code should use the methods
// on Queries.
func (q *Queries) DB() *sql.DB {
	if q == nil {
		return nil
	}
	return q.db
}

// OpenInMemoryForTest returns a fully-migrated *sql.DB backed by a
// unique shared-cache in-memory connection. Each test gets its own
// database; safe under `t.Parallel()`.
func OpenInMemoryForTest(t interface {
	Helper()
	Name() string
	Cleanup(func())
}) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:test-%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := Open(context.Background(), dsn, Options{InMemory: true})
	if err != nil {
		panic(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ErrNotFound is the canonical "row not found" error used by Query
// helpers that wrap sql.ErrNoRows.
var ErrNotFound = errors.New("storage: not found")

// IsNoRows reports whether err is sql.ErrNoRows. Useful at call sites
// that want to convert to a typed not-found error.
func IsNoRows(err error) bool { return err == sql.ErrNoRows }

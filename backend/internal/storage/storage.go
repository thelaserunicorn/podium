package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	// Pure-Go SQLite driver. No CGo. Pinned in go.mod.
	_ "modernc.org/sqlite"
)

//go:embed all:migrations_embed
var migrationsFS embed.FS

// Options tunes how Open initialises the SQLite database. The zero value is
// suitable for production use (file-backed, WAL, foreign keys on).
type Options struct {
	// InMemory opens the database in memory instead of writing to disk.
	// Useful for tests; see storage_test.go.
	InMemory bool
}

// Open returns a *sql.DB for the Podium SQLite database, creating parent
// directories and applying any pending migrations. It is safe to call more
// than once against the same path; subsequent calls will be no-ops at the
// schema level (PRAGMA user_version gates migration re-runs).
//
// DSN handling: when InMemory is true the caller-supplied DSN is used as-is
// (typically a unique-per-test shared-cache in-memory URI). When InMemory is
// false the path is treated as a regular file; parent directories are created
// with 0o755 and the file itself is opened with SQLite defaults (WAL is
// enabled by us below).
//
// Absolute paths without an explicit URI scheme are promoted to
// `file:<path>` automatically. modernc.org/sqlite otherwise mis-parses
// the leading `/` as a scheme-relative URI and fails with
// SQLITE_CANTOPEN (14). Relative paths (`data/podium.db`) and explicit
// URI DSNs (`file:/data/podium.db?cache=shared`) pass through unchanged.
func Open(ctx context.Context, dsn string, opts Options) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("storage: empty DSN")
	}

	if !opts.InMemory {
		if err := ensureParentDir(dsn); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite", NormalizeDSN(dsn, opts.InMemory))
	if err != nil {
		return nil, fmt.Errorf("storage: sql.Open: %w", err)
	}

	// One connection at a time keeps SQLite's locking model sane and avoids
	// "database is locked" surprises on macOS where fcntl locks are coarse.
	// Writes are still fast enough for a one-week project; revisit if a
	// benchmark says otherwise.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}

	// PRAGMAs we always want, regardless of caller. Order matters: WAL
	// before synchronous=NORMAL, foreign_keys after attach.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("storage: %s: %w", pragma, err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// Migrate applies every embedded migration whose version is greater than the
// database's current PRAGMA user_version. Each migration runs inside its own
// transaction; on error the version is left unchanged so the next Open() call
// can retry safely (AGENTS.md §7: "Database initialization should be safe to
// run more than once.").
func Migrate(ctx context.Context, db *sql.DB) error {
	current, err := readUserVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("storage: read user_version: %w", err)
	}

	pending, err := loadPendingMigrations(current)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	for _, m := range pending {
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("storage: apply %s: %w", m.name, err)
		}
	}
	return nil
}

// migration is a single .sql file under migrations/.
type migration struct {
	version int
	name    string
	body    string
}

func loadPendingMigrations(current int) ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations_embed")
	if err != nil {
		return nil, fmt.Errorf("storage: read embedded migrations: %w", err)
	}

	var all []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := parseVersionPrefix(e.Name())
		if err != nil {
			return nil, fmt.Errorf("storage: parse migration %q: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(migrationsFS, filepath.Join("migrations_embed", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("storage: read %s: %w", e.Name(), err)
		}
		all = append(all, migration{version: v, name: e.Name(), body: string(body)})
	}

	sort.Slice(all, func(i, j int) bool { return all[i].version < all[j].version })

	var pending []migration
	for _, m := range all {
		if m.version > current {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

func parseVersionPrefix(name string) (int, error) {
	// Expect "<4-digit version>_<slug>.sql", e.g. "0001_init.sql".
	idx := strings.IndexByte(name, '_')
	if idx <= 0 {
		return 0, fmt.Errorf("missing version prefix")
	}
	v, err := strconv.Atoi(name[:idx])
	if err != nil {
		return 0, fmt.Errorf("non-numeric version %q", name[:idx])
	}
	return v, nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return err
	}
	// modernc.org/sqlite does not support `?` placeholders inside PRAGMA
	// statements, so the version is inlined. Safe: m.version is an int parsed
	// from the migration filename by parseVersionPrefix.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return err
	}
	return tx.Commit()
}

func readUserVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// ensureParentDir creates the parent directory of a SQLite file path so
// `Open(ctx, "data/podium.db", Options{})` Just Works on a fresh checkout.
// No-op for in-memory DSNs (the caller passed InMemory=true) and for paths
// that have no directory component.
func ensureParentDir(dsn string) error {
	dir := filepath.Dir(dsn)
	if dir == "" || dir == "." {
		return nil
	}
	// Strip a sqlite URI prefix so filepath.Dir sees the path part only.
	if i := strings.Index(dsn, "?"); i >= 0 {
		dir = filepath.Dir(dsn[:i])
		if dir == "" || dir == "." {
			return nil
		}
	}
	return mkdirAll(dir)
}

// mkdirAll is os.MkdirAll(..., 0o755) lifted into a function so the call site
// doesn't import "os" directly. Keeping the helper local avoids needing
// build-tagged files for a single, portable call.
func mkdirAll(path string) error { return os.MkdirAll(path, 0o755) }

// NormalizeDSN rewrites an absolute filesystem path into the modernc/sqlite
// URI form when no scheme is already present. The driver otherwise interprets
// the leading `/` as a scheme-relative URI (e.g. `/data/podium.db` becomes
// `data/podium.db` under the current schema), which fails with
// SQLITE_CANTOPEN. In-memory DSNs (containing `?mode=memory` or starting
// with `file::memory:`) and explicit `file:` / `file:` schemes pass through
// untouched.
//
// Exported so unit tests can verify the rewrite rules directly without
// having to round-trip through Open().
func NormalizeDSN(dsn string, inMemory bool) string {
	if inMemory {
		return dsn
	}
	if strings.HasPrefix(dsn, "file:") {
		return dsn
	}
	// Treat any DSN with a scheme:// as already URI-form (e.g.
	// `https://...`) — we don't expect such inputs but be conservative.
	if i := strings.Index(dsn, "://"); i >= 0 && i < 16 {
		return dsn
	}
	if strings.HasPrefix(dsn, "/") {
		return "file:" + dsn
	}
	return dsn
}

package dash

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no extra C toolchain for the dashboard
)

// memSeq names in-memory databases uniquely. See Open.
var memSeq atomic.Uint64

// DB is the dashboard's durable store. All writes go through one goroutine (see
// capture.go), so the only concurrency here is reads racing that writer, which
// SQLite in WAL mode handles.
type DB struct {
	sql  *sql.DB
	path string // "" for the in-memory store
}

// Open opens (creating if needed) the dashboard database at path. path ":memory:"
// or "" yields an ephemeral in-memory database, which is also the fallback the
// proxy uses when the configured path is unwritable — the proxy must keep serving
// traffic whatever the disk says.
//
// On a schema-version mismatch the existing file is renamed aside
// (<path>.v<old>.bak) and a fresh database is created: the dashboard is a derived
// view, so discarding history beats refusing to boot, and renaming beats deleting.
func Open(path string) (*DB, error) {
	if path == "" || path == ":memory:" {
		// A UNIQUE name per Open, not a bare `file::memory:`.
		//
		// `cache=shared` is required: database/sql keeps a connection POOL and a private
		// in-memory database exists per connection, so every pooled connection would
		// otherwise see its own empty database. But under `cache=shared` the NAME
		// identifies the database, and `file::memory:` is a single name — so every
		// in-memory dashboard in the process WAS the same database. Two proxies falling
		// back to :memory: silently merged their history, and :memory: tests leaked rows
		// into each other (the flakiest possible failure). A per-instance name keeps the
		// pooling behaviour and removes the collision.
		return openDSN(fmt.Sprintf("file:dashmem%d?mode=memory&cache=shared", memSeq.Add(1)), "")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	db, err := openDSN(dsn(path), path)
	var mismatch *versionMismatch
	if errors.As(err, &mismatch) {
		aside := fmt.Sprintf("%s.v%s.bak", path, sanitizeVersion(mismatch.have))
		slog.Warn("dash: schema version changed; preserving the old database and starting fresh",
			"old_version", mismatch.have, "new_version", schemaVersion, "preserved_at", aside)
		if rerr := os.Rename(path, aside); rerr != nil {
			return nil, fmt.Errorf("dash: %w (and could not preserve it: %v)", err, rerr)
		}
		return openDSN(dsn(path), path)
	}
	return db, err
}

// dsn builds the driver DSN: WAL for concurrent reads while the writer commits,
// NORMAL synchronous (a lost tail of observability rows on power loss is
// acceptable; halving write cost is not), a busy timeout so a read never errors
// out under a concurrent commit, and foreign keys on for the ON DELETE CASCADEs
// retention relies on.
func dsn(path string) string {
	return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}

func openDSN(d, path string) (*DB, error) {
	sdb, err := sql.Open("sqlite", d)
	if err != nil {
		return nil, err
	}
	if err := sdb.Ping(); err != nil {
		sdb.Close()
		return nil, err
	}
	if err := migrate(sdb); err != nil {
		sdb.Close()
		return nil, err
	}
	return &DB{sql: sdb, path: path}, nil
}

// sanitizeVersion keeps a version string safe to embed in a filename.
func sanitizeVersion(v string) string {
	v = strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '-'
	}, v)
	if v == "" {
		return "unknown"
	}
	if len(v) > 16 {
		v = v[:16]
	}
	return v
}

// Close releases the database.
func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

// Path returns the on-disk path ("" when in-memory).
func (d *DB) Path() string { return d.path }

// insertBatch writes a batch of captured events in ONE transaction — the whole
// point of batching: a per-request fsync would make the writer the bottleneck
// under agent traffic. A failed batch is logged and dropped; observability never
// retries into a growing backlog.
func (d *DB) insertBatch(evs []*Event) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	reqStmt, err := tx.Prepare(`INSERT INTO requests(
		ts, session_id, model, provider, agent, preset, mode, route, status, bypassed, cache_aware,
		messages, tokens_before, tokens_after, attempted_tokens, frozen_tokens, saved_unique,
		fresh_input, cache_read, cache_write, output_tokens,
		cost_usd, baseline_cost_usd, cg_llm_cost_usd, cg_latency_ms, upstream_ms,
		expands, expand_tokens, reverts, token_accounting, cache_miss_reason, uncompressed_reason
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer reqStmt.Close()
	compStmt, err := tx.Prepare(`INSERT INTO request_components(
		request_id, component, kind, acted, mutated, reverted, skipped, saved_gross, saved_unique, duration_ms, err
	) VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer compStmt.Close()
	contentStmt, err := tx.Prepare(`INSERT INTO request_content(
		request_id, seq, path, before_tokens, after_tokens, before_gz, after_gz
	) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer contentStmt.Close()

	for _, e := range evs {
		res, err := reqStmt.Exec(
			e.TS, e.SessionID, e.Model, e.Provider, e.Agent, e.Preset, e.Mode, e.Route, e.Status,
			boolInt(e.Bypassed), boolInt(e.CacheAware),
			e.Messages, e.TokensBefore, e.TokensAfter, e.AttemptedTokens, e.FrozenTokens, e.SavedUnique,
			e.FreshInput, e.CacheRead, e.CacheWrite, e.OutputTokens,
			e.CostUSD, e.BaselineCostUSD, e.CGLLMCostUSD, e.CGLatencyMs, e.UpstreamMs,
			e.Expands, e.ExpandTokens, e.Reverts, e.TokenAccounting, e.CacheMissReason, e.UncompressedReason,
		)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		e.ID = id
		for _, c := range e.Components {
			if _, err := compStmt.Exec(id, c.Component, c.Kind,
				boolInt(c.Acted), boolInt(c.Mutated), boolInt(c.Reverted), boolInt(c.Skipped),
				c.SavedGross, c.SavedUnique, c.DurationMs, c.Err); err != nil {
				return err
			}
		}
		for i, c := range e.Content {
			if _, err := contentStmt.Exec(id, i, c.Path, c.BeforeTokens, c.AfterTokens,
				gzipText(c.Before), gzipText(c.After)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// gzipText compresses one captured before/after blob. Content is the bulk of the
// database, it is highly repetitive agent transcript text, and it is only ever
// read one request at a time by the diff view — so paying CPU on the writer
// goroutine to keep the file small is the right trade.
func gzipText(s string) []byte {
	if s == "" {
		return nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := io.WriteString(zw, s); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

func gunzipText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return ""
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return ""
	}
	return string(out)
}

// Prune enforces retention by BOTH age and size, in that order: drop everything
// older than maxAge, then — if the file is still over maxBytes — drop the oldest
// requests until it fits. Age alone cannot bound a burst; size alone silently
// erases a quiet week. Content rows and component rows go with their request via
// ON DELETE CASCADE. Returns how many request rows were deleted.
//
// maxAge <= 0 disables the age rule; maxBytes <= 0 disables the size rule.
func (d *DB) Prune(now time.Time, maxAge time.Duration, maxBytes int64) (int64, error) {
	var deleted int64
	if maxAge > 0 {
		cutoff := now.Add(-maxAge).UnixMilli()
		res, err := d.sql.Exec(`DELETE FROM requests WHERE ts < ?`, cutoff)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	if maxBytes <= 0 {
		return deleted, nil
	}
	// Size rule. Loop because deleting a slice does not immediately shrink the
	// file (SQLite reuses freed pages), so we bound the work: at most a few
	// rounds, each dropping the oldest 10% of rows, and stop as soon as the
	// estimated payload fits.
	for round := 0; round < 8; round++ {
		size, err := d.sizeBytes()
		if err != nil || size <= maxBytes {
			return deleted, err
		}
		var total int64
		if err := d.sql.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&total); err != nil {
			return deleted, err
		}
		if total == 0 {
			return deleted, nil
		}
		drop := total / 10
		if drop < 1 {
			drop = 1
		}
		res, err := d.sql.Exec(
			`DELETE FROM requests WHERE id IN (SELECT id FROM requests ORDER BY ts ASC LIMIT ?)`, drop)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
		// Reclaim the pages so the next sizeBytes reflects the deletion.
		if _, err := d.sql.Exec(`VACUUM`); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// sizeBytes reports the database's payload size. page_count*page_size is exact
// for the main file and works for an in-memory database too (where a stat would
// have nothing to look at).
func (d *DB) sizeBytes() (int64, error) {
	var pages, pageSize int64
	if err := d.sql.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := d.sql.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	return pages * pageSize, nil
}

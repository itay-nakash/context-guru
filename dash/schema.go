// Package dash is context-guru's persistent observability layer: the durable
// per-request store behind the dashboard, the off-hot-path capture pipeline that
// fills it, the JSON/SSE API the UI reads, and the embedded single-file UI.
//
// Layering, and why it is boring on purpose:
//
//   - metrics.Aggregator stays the fast in-process counter behind /stats. dash
//     never replaces it and never changes its shape — the benchmark harnesses
//     (deploy/harbor/*.py) parse that payload.
//   - Capture is strictly out of band. A request handler builds one Event and
//     hands it to a buffered channel; when the channel is full the event is
//     DROPPED and counted. Observability can never add latency to, or fail, a
//     request — the property gateway gets right and the one worth keeping.
//   - One writer goroutine owns the database. It batches inserts in a
//     transaction and fans a summary row out to SSE clients. Nothing else writes.
//   - Percentages are derived at read time; COST is computed at write time, so
//     history does not silently reprice when a model's published rate changes.
//   - No rollup tables. Time series are bucketed in SQL at query time
//     (ts/bucket*bucket GROUP BY 1). SQLite handles millions of rows; a
//     pre-aggregation layer is the speculative complexity to skip until a query
//     is measurably slow.
//
// The driver is modernc.org/sqlite (pure Go), so a dashboard build needs no C
// toolchain beyond the one tree-sitter already forces.
package dash

import (
	"database/sql"
	"fmt"
	"strings"
)

// schemaVersion is bumped whenever the DDL below changes incompatibly. On a
// mismatch Open PRESERVES the old file (renamed with its version suffix) and
// starts a fresh database: a dashboard is a derived view, so discarding history
// beats refusing to boot, and keeping the file beats deleting a user's data.
const schemaVersion = 1

// ddl is the whole schema. Timestamps are epoch MILLISECONDS everywhere — never
// a formatted locale string, which cannot be range-queried, sorted portably, or
// bucketed (gateway's mistake).
const ddl = `
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- One row per proxied request. Cost columns are USD, priced at write time from
-- the model's rates; NULL-equivalent (0) with token_accounting<>'complete' means
-- "we could not price this", which the UI must render as unknown, not as free.
CREATE TABLE IF NOT EXISTS requests (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  ts                 INTEGER NOT NULL,          -- epoch ms
  session_id         TEXT    NOT NULL DEFAULT '',
  model              TEXT    NOT NULL DEFAULT '',
  provider           TEXT    NOT NULL DEFAULT '',
  agent              TEXT    NOT NULL DEFAULT '', -- client user-agent family (claude-code, codex, …)
  preset             TEXT    NOT NULL DEFAULT '',
  mode               TEXT    NOT NULL DEFAULT '', -- operating mode (active|bypass|observe)
  route              TEXT    NOT NULL DEFAULT '',
  status             INTEGER NOT NULL DEFAULT 0,  -- upstream HTTP status (0 = no upstream)
  bypassed           INTEGER NOT NULL DEFAULT 0,
  cache_aware        INTEGER NOT NULL DEFAULT 0,
  messages           INTEGER NOT NULL DEFAULT 0,
  tokens_before      INTEGER NOT NULL DEFAULT 0,
  tokens_after       INTEGER NOT NULL DEFAULT 0,
  attempted_tokens   INTEGER NOT NULL DEFAULT 0, -- denominator: what we were allowed to compact
  frozen_tokens      INTEGER NOT NULL DEFAULT 0, -- cost of cache safety: what we deliberately did not touch
  saved_unique       INTEGER NOT NULL DEFAULT 0, -- this request's NEW (not re-sent) savings
  fresh_input        INTEGER NOT NULL DEFAULT 0,
  cache_read         INTEGER NOT NULL DEFAULT 0,
  cache_write        INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cost_usd           REAL    NOT NULL DEFAULT 0,
  baseline_cost_usd  REAL    NOT NULL DEFAULT 0, -- what the same request would have cost uncompacted
  cg_llm_cost_usd    REAL    NOT NULL DEFAULT 0, -- context-guru's OWN model spend attributable here
  cg_latency_ms      REAL    NOT NULL DEFAULT 0,
  upstream_ms        REAL    NOT NULL DEFAULT 0,
  expands            INTEGER NOT NULL DEFAULT 0,
  expand_tokens      INTEGER NOT NULL DEFAULT 0, -- restoration: content we offloaded and had to serve back
  reverts            INTEGER NOT NULL DEFAULT 0,
  token_accounting   TEXT    NOT NULL DEFAULT 'missing', -- complete|partial|missing
  cache_miss_reason  TEXT    NOT NULL DEFAULT '',        -- cold_start|ttl_expiry|prefix_change|unknown|hit
  uncompressed_reason TEXT   NOT NULL DEFAULT ''         -- why we did not compact: '' = we did
);
CREATE INDEX IF NOT EXISTS idx_requests_ts       ON requests(ts DESC);
CREATE INDEX IF NOT EXISTS idx_requests_session  ON requests(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_requests_model    ON requests(model, ts);

-- One row per component per request: the answer to "which components earn their
-- place". saved_gross is what the component removed THIS turn (re-counted every
-- turn the agent re-sends the transcript); saved_unique counts each distinct
-- compaction once.
CREATE TABLE IF NOT EXISTS request_components (
  request_id   INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  component    TEXT    NOT NULL,
  kind         TEXT    NOT NULL DEFAULT '',
  acted        INTEGER NOT NULL DEFAULT 0,
  mutated      INTEGER NOT NULL DEFAULT 0,
  reverted     INTEGER NOT NULL DEFAULT 0,
  skipped      INTEGER NOT NULL DEFAULT 0,
  saved_gross  INTEGER NOT NULL DEFAULT 0,
  saved_unique INTEGER NOT NULL DEFAULT 0,
  duration_ms  REAL    NOT NULL DEFAULT 0,
  err          TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_rc_request ON request_components(request_id);
CREATE INDEX IF NOT EXISTS idx_rc_comp    ON request_components(component);

-- Before/after text of each rewritten message — the diff view's data. Stored
-- gzip-compressed and size-capped, and skippable entirely (content capture is
-- opt-out). Redaction happens BEFORE the insert, never on read.
CREATE TABLE IF NOT EXISTS request_content (
  request_id    INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,
  path          TEXT    NOT NULL DEFAULT '',
  before_tokens INTEGER NOT NULL DEFAULT 0,
  after_tokens  INTEGER NOT NULL DEFAULT 0,
  before_gz     BLOB,
  after_gz      BLOB
);
CREATE INDEX IF NOT EXISTS idx_content_request ON request_content(request_id);

-- Ingested benchmark runs (deploy/harbor's summary.json + rows-*.json).
CREATE TABLE IF NOT EXISTS bench_runs (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  name     TEXT NOT NULL UNIQUE,   -- run directory name, so re-ingesting replaces
  ts       INTEGER NOT NULL,
  dataset  TEXT NOT NULL DEFAULT '',
  model    TEXT NOT NULL DEFAULT '',
  summary  TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS bench_tasks (
  run_id            INTEGER NOT NULL REFERENCES bench_runs(id) ON DELETE CASCADE,
  arm               TEXT    NOT NULL,   -- config name: off|codesmart|headroom|rtk|…
  task              TEXT    NOT NULL,
  reward            REAL    NOT NULL DEFAULT 0,
  steps             INTEGER NOT NULL DEFAULT 0,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read        INTEGER NOT NULL DEFAULT 0,
  cache_write       INTEGER NOT NULL DEFAULT 0,
  fresh_input       INTEGER NOT NULL DEFAULT 0,
  cost_usd          REAL    NOT NULL DEFAULT 0,
  norm_cost_usd     REAL    NOT NULL DEFAULT 0,
  wall_s            REAL    NOT NULL DEFAULT 0,
  exception         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_bt_run ON bench_tasks(run_id, arm);
`

// migrate creates the schema and validates its version. A version mismatch is
// reported to the caller, which renames the old file aside and retries — see Open.
func migrate(db *sql.DB) error {
	var have string
	err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&have)
	switch {
	case err == sql.ErrNoRows || isMissingTable(err):
		// Fresh (or pre-meta) database: create everything and stamp the version.
	case err != nil:
		return err
	case have != fmt.Sprint(schemaVersion):
		return &versionMismatch{have: have}
	default:
		return nil // already at this version
	}
	if _, err := db.Exec(ddl); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('schema_version',?)`, fmt.Sprint(schemaVersion))
	return err
}

// versionMismatch signals that the file on disk was written by another schema.
type versionMismatch struct{ have string }

func (e *versionMismatch) Error() string {
	return fmt.Sprintf("dash: database schema version %s, want %d", e.have, schemaVersion)
}

func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

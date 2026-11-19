// Package store persists normalized metrics, process snapshots and diagnosis
// reports in a local SQLite database. It uses only the pure-Go modernc.org
// driver.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
)

const (
	DefaultMetricRetention    = 7 * 24 * time.Hour
	DefaultDiagnosisRetention = 30 * 24 * time.Hour
)

// Options controls retention. Zero values use the product defaults.
type Options struct {
	MetricRetention    time.Duration
	DiagnosisRetention time.Duration
}

// Status is a serializable storage health snapshot.
type Status struct {
	Path        string `json:"path"`
	Ready       bool   `json:"ready"`
	LastWriteAt int64  `json:"last_write_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
}

// Store is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
	opts Options

	mu          sync.RWMutex
	lastWriteAt int64
	lastErr     string
	ready       bool
	closed      bool
}

// Open creates or opens the database, applies migrations and verifies WAL.
func Open(path string, opts Options) (*Store, error) {
	if opts.MetricRetention <= 0 {
		opts.MetricRetention = DefaultMetricRetention
	}
	if opts.DiagnosisRetention <= 0 {
		opts.DiagnosisRetention = DefaultDiagnosisRetention
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := "file:///" + strings.TrimPrefix(filepath.ToSlash(path), "/") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db, path: path, opts: opts}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at_ms INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	var current int
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current)
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range m.statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at_ms) VALUES(?, ?)`, m.version, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// LoadCounters returns the last cumulative disk counters. The normalizer calls
// this once per process so rates continue across a restart.
func (s *Store) LoadCounters(ctx context.Context) (map[string]ingest.CounterState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT series, ts_ms, read_bytes, write_bytes, reads, writes FROM counter_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ingest.CounterState)
	for rows.Next() {
		var series string
		var c ingest.CounterState
		if err := rows.Scan(&series, &c.TS, &c.ReadBytes, &c.WriteBytes, &c.Reads, &c.Writes); err != nil {
			return nil, err
		}
		out[series] = c
	}
	return out, rows.Err()
}

// InsertBatch writes samples, process snapshots and counters in one
// transaction. A failure is visible through Status.LastError and returned to
// the ingest caller.
func (s *Store) InsertBatch(ctx context.Context, b ingest.Batch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.recordError(err)
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO metric_samples(ts_ms, kind, series, a, b, c, d) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		s.recordError(err)
		return err
	}
	for _, sm := range b.Samples {
		if _, err := stmt.ExecContext(ctx, sm.TS, sm.Kind, sm.Series, nullable(sm.A), nullable(sm.B), nullable(sm.C), nullable(sm.D)); err != nil {
			stmt.Close()
			s.recordError(err)
			return err
		}
	}
	stmt.Close()

	if len(b.Counters) > 0 {
		cstmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO counter_state(series, ts_ms, read_bytes, write_bytes, reads, writes) VALUES(?,?,?,?,?,?)`)
		if err != nil {
			s.recordError(err)
			return err
		}
		for series, c := range b.Counters {
			if _, err := cstmt.ExecContext(ctx, series, c.TS, c.ReadBytes, c.WriteBytes, c.Reads, c.Writes); err != nil {
				cstmt.Close()
				s.recordError(err)
				return err
			}
		}
		cstmt.Close()
	}

	if len(b.Processes) > 0 {
		if err := s.insertProcesses(ctx, tx, b.Processes); err != nil {
			s.recordError(err)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		s.recordError(err)
		return err
	}
	committed = true
	s.mu.Lock()
	s.lastWriteAt = time.Now().UnixMilli()
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

func (s *Store) insertProcesses(ctx context.Context, tx *sql.Tx, procs []ingest.ProcessSample) error {
	// Latest snapshot: one row per PID, top 200 by CPU then working set.
	latest := dedupeProcesses(procs)
	sort.SliceStable(latest, func(i, j int) bool {
		ci, cj := latest[i].CPU, latest[j].CPU
		if ci != nil && cj != nil && *ci != *cj {
			return *ci > *cj
		}
		if (ci != nil) != (cj != nil) {
			return ci != nil
		}
		wi, wj := latest[i].WorkingSet, latest[j].WorkingSet
		if wi != nil && wj != nil && *wi != *wj {
			return *wi > *wj
		}
		return latest[i].PID < latest[j].PID
	})
	if len(latest) > 200 {
		latest = latest[:200]
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM process_latest`); err != nil {
		return err
	}
	lstmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO process_latest(pid, name, cpu_pct, working_set, ts_ms) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	for _, p := range latest {
		if _, err := lstmt.ExecContext(ctx, p.PID, p.Name, nullable(p.CPU), nullable(p.WorkingSet), p.TS); err != nil {
			lstmt.Close()
			return err
		}
	}
	lstmt.Close()

	// History: union of each snapshot's top-10 CPU and top-10 memory. This
	// bounds per-snapshot storage while retaining what was busy.
	union := topUnion(procs, 10)
	hstmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO process_samples(ts_ms, name, pid, cpu_pct, working_set, top_cpu, top_mem) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer hstmt.Close()
	for _, p := range union {
		if _, err := hstmt.ExecContext(ctx, p.row.TS, p.row.Name, p.row.PID, nullable(p.row.CPU), nullable(p.row.WorkingSet), boolInt(p.cpuTop), boolInt(p.memTop)); err != nil {
			return err
		}
	}
	return nil
}

type topProcess struct {
	row    ingest.ProcessSample
	cpuTop bool
	memTop bool
}

func topUnion(procs []ingest.ProcessSample, n int) []topProcess {
	byPID := make(map[int32]*topProcess)
	order := make([]*topProcess, 0, len(procs))
	for _, p := range procs {
		if existing, ok := byPID[p.PID]; ok {
			if p.TS >= existing.row.TS {
				existing.row = p
			}
			continue
		}
		x := &topProcess{row: p}
		byPID[p.PID] = x
		order = append(order, x)
	}
	mark := func(key func(ingest.ProcessSample) *float64, set func(*topProcess)) {
		sorted := make([]*topProcess, len(order))
		copy(sorted, order)
		sort.SliceStable(sorted, func(i, j int) bool {
			a, b := key(sorted[i].row), key(sorted[j].row)
			if a == nil || b == nil {
				return a != nil
			}
			return *a > *b
		})
		for i := 0; i < len(sorted) && i < n; i++ {
			if key(sorted[i].row) != nil {
				set(sorted[i])
			}
		}
	}
	mark(func(p ingest.ProcessSample) *float64 { return p.CPU }, func(p *topProcess) { p.cpuTop = true })
	mark(func(p ingest.ProcessSample) *float64 { return p.WorkingSet }, func(p *topProcess) { p.memTop = true })
	out := make([]topProcess, 0, len(order))
	for _, p := range order {
		if p.cpuTop || p.memTop {
			out = append(out, *p)
		}
	}
	return out
}

func dedupeProcesses(procs []ingest.ProcessSample) []ingest.ProcessSample {
	byPID := make(map[int32]ingest.ProcessSample, len(procs))
	for _, p := range procs {
		if prev, ok := byPID[p.PID]; !ok || p.TS >= prev.TS {
			byPID[p.PID] = p
		}
	}
	out := make([]ingest.ProcessSample, 0, len(byPID))
	for _, p := range byPID {
		out = append(out, p)
	}
	return out
}

func nullable(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// QueryProcesses returns the latest snapshot with an optional local name/PID
// filter. Sorting is by CPU or working set.
func (s *Store) QueryProcesses(ctx context.Context, sortBy, query string, limit int) ([]ProcessRow, int64, error) {
	if sortBy != "memory" {
		sortBy = "cpu"
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pid, name, cpu_pct, working_set, ts_ms FROM process_latest`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	q := strings.ToLower(strings.TrimSpace(query))
	var out []ProcessRow
	var snapshot int64
	for rows.Next() {
		var p ProcessRow
		var cpu, ws sql.NullFloat64
		if err := rows.Scan(&p.PID, &p.Name, &cpu, &ws, &p.TS); err != nil {
			return nil, 0, err
		}
		if cpu.Valid {
			v := cpu.Float64
			p.CPU = &v
		}
		if ws.Valid {
			v := ws.Float64
			p.WorkingSet = &v
		}
		if p.TS > snapshot {
			snapshot = p.TS
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Name), q) && !strings.Contains(fmt.Sprint(p.PID), q) {
			continue
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sortBy == "memory" {
			a, b := out[i].WorkingSet, out[j].WorkingSet
			if a == nil || b == nil {
				return a != nil
			}
			if *a != *b {
				return *a > *b
			}
		} else {
			a, b := out[i].CPU, out[j].CPU
			if a == nil || b == nil {
				return a != nil
			}
			if *a != *b {
				return *a > *b
			}
		}
		return out[i].PID < out[j].PID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, snapshot, nil
}

// TopProcesses returns processes observed anywhere in the window, aggregated
// by name from the top-10 union history. CPU-heavy and memory-heavy names are
// both represented by scoring each against the window maxima.
func (s *Store) TopProcesses(ctx context.Context, from, to int64, limit int) ([]ProcessAgg, error) {
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name,
		MAX(cpu_pct) AS max_cpu,
		MAX(working_set) AS max_ws,
		COUNT(*) AS samples
		FROM process_samples
		WHERE ts_ms >= ? AND ts_ms <= ?
		GROUP BY name`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessAgg
	var maxCPU, maxWS float64
	for rows.Next() {
		var p ProcessAgg
		var cpu, ws sql.NullFloat64
		if err := rows.Scan(&p.Name, &cpu, &ws, &p.Samples); err != nil {
			return nil, err
		}
		if cpu.Valid {
			v := cpu.Float64
			p.MaxCPU = &v
			if v > maxCPU {
				maxCPU = v
			}
		}
		if ws.Valid {
			v := ws.Float64
			p.MaxWorkingSet = &v
			if v > maxWS {
				maxWS = v
			}
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	score := func(p ProcessAgg) float64 {
		var best float64
		if p.MaxCPU != nil && maxCPU > 0 {
			best = *p.MaxCPU / maxCPU
		}
		if p.MaxWorkingSet != nil && maxWS > 0 {
			if v := *p.MaxWorkingSet / maxWS; v > best {
				best = v
			}
		}
		return best
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := score(out[i]), score(out[j])
		if si != sj {
			return si > sj
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Maintain purges expired data and checkpoints WAL. Errors are recorded for
// the status endpoint.
func (s *Store) Maintain(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM metric_samples WHERE ts_ms < ?`, now.Add(-s.opts.MetricRetention).UnixMilli()); err != nil {
		s.recordError(err)
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM process_samples WHERE ts_ms < ?`, now.Add(-s.opts.MetricRetention).UnixMilli()); err != nil {
		s.recordError(err)
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM diagnoses WHERE created_at_ms < ?`, now.Add(-s.opts.DiagnosisRetention).UnixMilli()); err != nil {
		s.recordError(err)
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		s.recordError(err)
		return err
	}
	return nil
}

// Status returns storage health. Size is read from disk on each call.
func (s *Store) Status() Status {
	s.mu.RLock()
	st := Status{Path: s.path, Ready: s.ready, LastWriteAt: s.lastWriteAt, LastError: s.lastErr}
	s.mu.RUnlock()
	if fi, err := os.Stat(s.path); err == nil {
		st.SizeBytes = fi.Size()
	}
	return st
}

func (s *Store) recordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// PurgeExpired is a convenience wrapper used by tests.
func (s *Store) PurgeExpired(ctx context.Context, now time.Time) error {
	return s.Maintain(ctx, now)
}

// Close checkpoints and closes the database. It is idempotent. The *sql.DB is
// retained so concurrent in-flight queries return a closed-database error
// instead of panicking on a nil handle.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.ready = false
	db := s.db
	s.mu.Unlock()
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return db.Close()
}

// DBPath returns the database path.
func (s *Store) DBPath() string { return s.path }

// ErrClosed is returned by operations on a closed store.
var ErrClosed = errors.New("store is closed")

// CountSamples returns row counts by kind inside [from,to].
func (s *Store) CountSamples(ctx context.Context, from, to int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM metric_samples WHERE ts_ms >= ? AND ts_ms <= ? GROUP BY kind`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// CountProcessSamples returns the number of distinct process snapshots.
func (s *Store) CountProcessSamples(ctx context.Context, from, to int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT ts_ms) FROM process_samples WHERE ts_ms >= ? AND ts_ms <= ?`, from, to).Scan(&n)
	return n, err
}

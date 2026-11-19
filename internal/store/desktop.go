package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/ingest"
)

// InsertDesktopSnapshot writes one in-process sampler snapshot. Metrics,
// process ranking history and the real PID/PPID tree are committed together so
// the UI never sees half a snapshot.
func (s *Store) InsertDesktopSnapshot(ctx context.Context, ts int64, samples []ingest.Sample, procs []ingest.ProcessSample, tree []ProcessTreeRow) error {
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

	if len(samples) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO metric_samples(ts_ms, kind, series, a, b, c, d) VALUES(?,?,?,?,?,?,?)`)
		if err != nil {
			s.recordError(err)
			return err
		}
		for _, sm := range samples {
			if _, err := stmt.ExecContext(ctx, sm.TS, sm.Kind, sm.Series, nullable(sm.A), nullable(sm.B), nullable(sm.C), nullable(sm.D)); err != nil {
				stmt.Close()
				s.recordError(err)
				return err
			}
		}
		stmt.Close()
	}

	if len(procs) > 0 {
		if err := s.insertProcesses(ctx, tx, procs); err != nil {
			s.recordError(err)
			return err
		}
	}

	if tree != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM process_tree_latest`); err != nil {
			s.recordError(err)
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO process_tree_latest
			(pid, ppid, name, start_ms, cpu_pct, working_set, ts_ms) VALUES(?,?,?,?,?,?,?)`)
		if err != nil {
			s.recordError(err)
			return err
		}
		for _, p := range tree {
			if p.PID <= 0 {
				continue
			}
			if _, err := stmt.ExecContext(ctx, p.PID, p.PPID, p.Name, p.StartMS, nullable(p.CPU), nullable(p.WorkingSet), p.TS); err != nil {
				stmt.Close()
				s.recordError(err)
				return err
			}
		}
		stmt.Close()
	}

	if err := tx.Commit(); err != nil {
		s.recordError(err)
		return err
	}
	committed = true
	s.mu.Lock()
	if ts > s.lastWriteAt {
		s.lastWriteAt = ts
	}
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

// LoadProcessTree returns the last full process snapshot.
func (s *Store) LoadProcessTree(ctx context.Context) ([]ProcessTreeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pid, ppid, name, start_ms, cpu_pct, working_set, ts_ms
		FROM process_tree_latest ORDER BY ppid, pid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessTreeRow
	for rows.Next() {
		var p ProcessTreeRow
		var cpu, ws sql.NullFloat64
		if err := rows.Scan(&p.PID, &p.PPID, &p.Name, &p.StartMS, &cpu, &ws, &p.TS); err != nil {
			return nil, err
		}
		if cpu.Valid {
			v := cpu.Float64
			p.CPU = &v
		}
		if ws.Valid {
			v := ws.Float64
			p.WorkingSet = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// QueryRTTTargets returns downsampled RTT series for each configured target.
// Target names are local settings values and are never used by the model;
// diagnostic summaries map them to aliases.
func (s *Store) QueryRTTTargets(ctx context.Context, from, to int64, targets []string) (map[string][]RTTPoint, error) {
	out := make(map[string][]RTTPoint, len(targets))
	if len(targets) == 0 || to <= from {
		return out, nil
	}
	if len(targets) > 8 {
		targets = targets[:8]
	}
	placeholders := make([]string, len(targets))
	args := make([]any, 0, len(targets)+2)
	args = append(args, from, to)
	for i, t := range targets {
		placeholders[i] = "?"
		args = append(args, t)
		out[t] = nil
	}
	q := `SELECT ts_ms, series, a, b, c FROM metric_samples
		WHERE kind='rtt' AND ts_ms >= ? AND ts_ms <= ? AND series IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY ts_ms ASC LIMIT 100000`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTarget := make(map[string][]rawPoint)
	for rows.Next() {
		var r rawPoint
		var target string
		var a, b, c sql.NullFloat64
		if err := rows.Scan(&r.ts, &target, &a, &b, &c); err != nil {
			return nil, err
		}
		if a.Valid {
			v := a.Float64
			r.a = &v
		}
		if b.Valid {
			v := b.Float64
			r.b = &v
		}
		if c.Valid {
			v := c.Float64
			r.c = &v
		}
		byTarget[target] = append(byTarget[target], r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	width := (to - from) / overviewBuckets
	if width < 1 {
		width = 1
	}
	for _, t := range targets {
		if pts, ok := byTarget[t]; ok {
			out[t] = rttPoints(pts, from, width)
		}
	}
	return out, nil
}

// CountRTTTargets returns raw RTT sample rows for the current target list.
// Unlike the downsampled series this preserves coverage accounting.
func (s *Store) CountRTTTargets(ctx context.Context, from, to int64, targets []string) (int, error) {
	if len(targets) == 0 || to <= from {
		return 0, nil
	}
	if len(targets) > 8 {
		targets = targets[:8]
	}
	placeholders := make([]string, len(targets))
	args := make([]any, 0, len(targets)+2)
	args = append(args, from, to)
	for i, t := range targets {
		placeholders[i] = "?"
		args = append(args, t)
	}
	q := `SELECT COUNT(*) FROM metric_samples WHERE kind='rtt' AND ts_ms >= ? AND ts_ms <= ? AND series IN (` + strings.Join(placeholders, ",") + `)`
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// LatestTimestampForTarget returns the newest RTT sample timestamp for a target.
func (s *Store) LatestTimestampForTarget(ctx context.Context, target string) (int64, error) {
	var ts sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(ts_ms) FROM metric_samples WHERE kind='rtt' AND series=?`, target).Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("latest rtt timestamp: %w", err)
	}
	if !ts.Valid {
		return 0, nil
	}
	return ts.Int64, nil
}

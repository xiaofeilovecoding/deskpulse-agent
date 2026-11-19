package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateDiagnosis inserts a new running record.
func (s *Store) CreateDiagnosis(ctx context.Context, d Diagnosis) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO diagnoses
		(id, created_at_ms, updated_at_ms, window_minutes, window_seconds, include_process_names, model, status, error_code, error_message, report_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.CreatedAt, d.UpdatedAt, d.WindowMinutes, d.WindowSeconds, boolInt(d.IncludeProcessNames), d.Model, d.Status,
		d.ErrorCode, d.ErrorMessage, d.ReportJSON)
	if err != nil {
		s.recordError(err)
	}
	return err
}

// UpdateDiagnosis updates status/error/report and the modified timestamp.
func (s *Store) UpdateDiagnosis(ctx context.Context, id, status, code, message, reportJSON string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE diagnoses
		SET updated_at_ms=?, status=?, error_code=?, error_message=?, report_json=?
		WHERE id=?`, now.UnixMilli(), status, code, message, reportJSON, id)
	if err != nil {
		s.recordError(err)
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetDiagnosis returns one record, including its report JSON.
func (s *Store) GetDiagnosis(ctx context.Context, id string) (Diagnosis, error) {
	var d Diagnosis
	var include int
	err := s.db.QueryRowContext(ctx, `SELECT id, created_at_ms, updated_at_ms, window_minutes,
		window_seconds, include_process_names, model, status, error_code, error_message, report_json
		FROM diagnoses WHERE id=?`, id).Scan(
		&d.ID, &d.CreatedAt, &d.UpdatedAt, &d.WindowMinutes, &d.WindowSeconds, &include, &d.Model,
		&d.Status, &d.ErrorCode, &d.ErrorMessage, &d.ReportJSON)
	if err != nil {
		return d, err
	}
	d.IncludeProcessNames = include != 0
	return d, nil
}

// ListDiagnoses returns recent metadata without report bodies.
func (s *Store) ListDiagnoses(ctx context.Context, limit int) ([]Diagnosis, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at_ms, updated_at_ms, window_minutes,
		window_seconds, include_process_names, model, status, error_code, error_message
		FROM diagnoses ORDER BY created_at_ms DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Diagnosis
	for rows.Next() {
		var d Diagnosis
		var include int
		if err := rows.Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt, &d.WindowMinutes, &d.WindowSeconds, &include,
			&d.Model, &d.Status, &d.ErrorCode, &d.ErrorMessage); err != nil {
			return nil, err
		}
		d.IncludeProcessNames = include != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkInterrupted marks analyses left running by a previous process.
func (s *Store) MarkInterrupted(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE diagnoses
		SET status='interrupted', error_code='interrupted', error_message=?, updated_at_ms=?
		WHERE status='running'`, "应用重启，分析已中断", now.UnixMilli())
	if err != nil {
		s.recordError(err)
		return err
	}
	return nil
}

// LatestTimestamp returns the newest metric timestamp (diagnostics/tests).
func (s *Store) LatestTimestamp(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(ts_ms) FROM metric_samples`).Scan(&ts); err != nil {
		return 0, fmt.Errorf("latest metric timestamp: %w", err)
	}
	if !ts.Valid {
		return 0, nil
	}
	return ts.Int64, nil
}

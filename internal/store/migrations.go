package store

type migration struct {
	version    int
	statements []string
}

var migrations = []migration{
	{
		version: 1,
		statements: []string{

			`CREATE TABLE IF NOT EXISTS metric_samples (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				ts_ms INTEGER NOT NULL,
				kind TEXT NOT NULL,
				series TEXT NOT NULL,
				a REAL,
				b REAL,
				c REAL,
				d REAL,
				UNIQUE(ts_ms, kind, series)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_metric_ts ON metric_samples(ts_ms)`,
			`CREATE INDEX IF NOT EXISTS idx_metric_kind_ts ON metric_samples(kind, ts_ms)`,
			`CREATE TABLE IF NOT EXISTS counter_state (
				series TEXT PRIMARY KEY,
				ts_ms INTEGER NOT NULL,
				read_bytes REAL NOT NULL,
				write_bytes REAL NOT NULL,
				reads REAL NOT NULL,
				writes REAL NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS process_samples (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				ts_ms INTEGER NOT NULL,
				name TEXT NOT NULL,
				pid INTEGER NOT NULL,
				cpu_pct REAL,
				working_set REAL,
				top_cpu INTEGER NOT NULL DEFAULT 0,
				top_mem INTEGER NOT NULL DEFAULT 0,
				UNIQUE(ts_ms, pid)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_process_ts ON process_samples(ts_ms)`,
			`CREATE INDEX IF NOT EXISTS idx_process_name_ts ON process_samples(name, ts_ms)`,
			`CREATE TABLE IF NOT EXISTS process_latest (
				pid INTEGER PRIMARY KEY,
				name TEXT NOT NULL,
				cpu_pct REAL,
				working_set REAL,
				ts_ms INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS diagnoses (
				id TEXT PRIMARY KEY,
				created_at_ms INTEGER NOT NULL,
				updated_at_ms INTEGER NOT NULL,
				window_minutes INTEGER NOT NULL,
				include_process_names INTEGER NOT NULL,
				model TEXT NOT NULL,
				status TEXT NOT NULL,
				error_code TEXT NOT NULL DEFAULT '',
				error_message TEXT NOT NULL DEFAULT '',
				report_json TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_diag_created ON diagnoses(created_at_ms)`,
		},
	},
	{
		version: 2,
		statements: []string{
			`CREATE TABLE IF NOT EXISTS process_tree_latest (
				pid INTEGER PRIMARY KEY,
				ppid INTEGER NOT NULL DEFAULT 0,
				name TEXT NOT NULL,
				start_ms INTEGER NOT NULL DEFAULT 0,
				cpu_pct REAL,
				working_set REAL,
				ts_ms INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_process_tree_ppid ON process_tree_latest(ppid)`,
			`ALTER TABLE diagnoses ADD COLUMN window_seconds INTEGER NOT NULL DEFAULT 0`,
		},
	},
}

package store

// Point is one downsampled bucket. Avg is the line value, Max preserves the
// visible peak and Count is the number of raw samples in the bucket.
type Point struct {
	TS    int64    `json:"t"`
	Avg   *float64 `json:"v"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	Count int      `json:"n"`
}

// DiskSeries is one logical volume.
type DiskSeries struct {
	Series  string   `json:"series"`
	Used    *float64 `json:"used_bytes"`
	Total   *float64 `json:"total_bytes"`
	Percent *float64 `json:"used_percent"`
	Points  []Point  `json:"points"`
}

// IOSeries is one physical disk. Read/Write points carry B/s averages and
// peaks; IOPS are the latest observed values.
type IOSeries struct {
	Series    string   `json:"series"`
	Read      []Point  `json:"read"`
	Write     []Point  `json:"write"`
	ReadIOPS  *float64 `json:"read_iops"`
	WriteIOPS *float64 `json:"write_iops"`
	LastRead  *float64 `json:"last_read_bps"`
	LastWrite *float64 `json:"last_write_bps"`
}

// RTTPoint keeps gaps explicit: Avg/Max are nil on total packet loss.
type RTTPoint struct {
	TS   int64    `json:"t"`
	Avg  *float64 `json:"avg_ms"`
	Max  *float64 `json:"max_ms"`
	Loss *float64 `json:"loss_percent"`
}

// Window is the bounded data required by the overview and the AI summarizer.
type Window struct {
	From       int64          `json:"from"`
	To         int64          `json:"to"`
	CPU        []Point        `json:"cpu"`
	Memory     []Point        `json:"memory"`
	MemUsed    *float64       `json:"mem_used_bytes"`
	MemTotal   *float64       `json:"mem_total_bytes"`
	MemPercent *float64       `json:"mem_used_percent"`
	Disks      []DiskSeries   `json:"disks"`
	DiskIO     []IOSeries     `json:"disk_io"`
	RTT        []RTTPoint     `json:"rtt"`
	Counts     map[string]int `json:"counts"`
	Truncated  bool           `json:"truncated"`
}

// ProcessRow is one row of the latest process snapshot.
type ProcessRow struct {
	Name       string   `json:"name"`
	PID        int32    `json:"pid"`
	CPU        *float64 `json:"cpu_percent"`
	WorkingSet *float64 `json:"working_set_bytes"`
	TS         int64    `json:"ts"`
}

// ProcessTreeRow is one real Windows process snapshot row. PPID and start time
// are persisted so the UI can rebuild the actual tree and disambiguate PID
// reuse without guessing from names.
type ProcessTreeRow struct {
	PID        int32    `json:"pid"`
	PPID       int32    `json:"ppid"`
	Name       string   `json:"name"`
	StartMS    int64    `json:"start_ms"`
	CPU        *float64 `json:"cpu_percent"`
	WorkingSet *float64 `json:"working_set_bytes"`
	TS         int64    `json:"ts"`
}

// RTTSnapshot is the newest probe result for one target.
type RTTSnapshot struct {
	Target string   `json:"target"`
	TS     int64    `json:"ts"`
	Avg    *float64 `json:"avg_ms"`
	Max    *float64 `json:"max_ms"`
	Loss   *float64 `json:"loss_percent"`
}

// ProcessAgg is a window aggregate of a process name from top-10 history.
type ProcessAgg struct {
	Name          string   `json:"name"`
	MaxCPU        *float64 `json:"max_cpu_percent"`
	MaxWorkingSet *float64 `json:"max_working_set_bytes"`
	Samples       int      `json:"samples"`
}

// Diagnosis is the persisted metadata and validated report JSON. ReportJSON
// never contains an API key.
type Diagnosis struct {
	ID                  string `json:"id"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
	WindowMinutes       int    `json:"window_minutes"`
	WindowSeconds       int    `json:"window_seconds,omitempty"`
	IncludeProcessNames bool   `json:"include_process_names"`
	Model               string `json:"model"`
	Status              string `json:"status"`
	ErrorCode           string `json:"error_code,omitempty"`
	ErrorMessage        string `json:"error_message,omitempty"`
	ReportJSON          string `json:"-"`
}

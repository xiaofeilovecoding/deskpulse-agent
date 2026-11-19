package diagnose

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Finding is one possible cause. Unverified means no valid evidence ID
// remained after validation, so the UI must show "待验证".
type Finding struct {
	Cause       string   `json:"cause"`
	EvidenceIDs []string `json:"evidence_ids"`
	Unverified  bool     `json:"unverified"`
}

// Report is the validated, local-only diagnosis result. Model text is always
// rendered as plain text; no field is interpreted as HTML or executed.
// Preset is the v0.3 diagnostic preset ID; records stored before v0.3 leave it
// empty and ParseStoredReport reads them as the overview preset.
type Report struct {
	ID                  string            `json:"id"`
	Model               string            `json:"model"`
	CreatedAt           int64             `json:"created_at"`
	WindowMinutes       int               `json:"window_minutes"`
	WindowSeconds       int               `json:"window_seconds,omitempty"`
	IncludeProcessNames bool              `json:"include_process_names"`
	Preset              string            `json:"preset,omitempty"`
	Scope               string            `json:"scope"`
	Summary             string            `json:"summary"`
	Findings            []Finding         `json:"findings"`
	NextSteps           []string          `json:"next_steps"`
	DataGaps            []string          `json:"data_gaps"`
	Aliases             map[string]string `json:"aliases"`
	Evidence            []Evidence        `json:"evidence"`
	DurationMS          int64             `json:"duration_ms"`
}

// ReportMeta carries non-model metadata into a validated report.
type ReportMeta struct {
	ID                  string
	Model               string
	CreatedAt           int64
	WindowMinutes       int
	WindowSeconds       int
	IncludeProcessNames bool
	Preset              string
	DurationMS          int64
}

type modelFinding struct {
	Cause       string   `json:"cause"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type modelResult struct {
	Summary   string         `json:"summary"`
	Findings  []modelFinding `json:"findings"`
	NextSteps []string       `json:"next_steps"`
	DataGaps  []string       `json:"data_gaps"`
}

// BuildReport strictly validates model JSON, filters evidence IDs against the
// local catalog and marks unsupported causes as unverified.
func BuildReport(raw []byte, catalog map[string]Evidence, aliases map[string]string, meta ReportMeta, summaryGaps []string) (Report, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Report{}, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var mr modelResult
	if err := dec.Decode(&mr); err != nil {
		return Report{}, ErrInvalid
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Report{}, ErrInvalid
	}

	summary := strings.TrimSpace(mr.Summary)
	if summary == "" {
		return Report{}, ErrInvalid
	}
	if len(summary) > 4000 {
		summary = summary[:4000]
	}
	if len(mr.Findings) > 20 {
		mr.Findings = mr.Findings[:20]
	}
	if len(mr.NextSteps) > 20 {
		mr.NextSteps = mr.NextSteps[:20]
	}
	if len(mr.DataGaps) > 20 {
		mr.DataGaps = mr.DataGaps[:20]
	}
	if len(mr.Findings) == 0 && len(mr.NextSteps) == 0 && len(mr.DataGaps) == 0 {
		return Report{}, ErrInvalid
	}

	unknown := false
	findings := make([]Finding, 0, len(mr.Findings))
	for _, f := range mr.Findings {
		cause := strings.TrimSpace(f.Cause)
		if cause == "" {
			continue
		}
		if len(cause) > 1000 {
			cause = cause[:1000]
		}
		ids := make([]string, 0, len(f.EvidenceIDs))
		seen := make(map[string]bool)
		for _, id := range f.EvidenceIDs {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if _, ok := catalog[id]; ok {
				ids = append(ids, id)
			} else {
				unknown = true
			}
		}
		findings = append(findings, Finding{Cause: cause, EvidenceIDs: ids, Unverified: len(ids) == 0})
	}
	if len(findings) == 0 && len(mr.NextSteps) == 0 && len(mr.DataGaps) == 0 {
		return Report{}, ErrInvalid
	}
	steps := cleanStrings(mr.NextSteps, 500)
	gaps := cleanStrings(mr.DataGaps, 500)
	gaps = append(summaryGaps, gaps...)
	if unknown {
		gaps = append(gaps, "部分模型引用未通过本地证据校验，已忽略")
	}
	gaps = dedupeStrings(gaps)
	if len(gaps) > 20 {
		gaps = gaps[:20]
	}
	ev := make([]Evidence, 0, len(catalog))
	for _, e := range catalog {
		ev = append(ev, e)
	}
	// Deterministic report output.
	sortEvidence(ev)
	if aliases == nil {
		aliases = map[string]string{}
	}
	return Report{
		ID:                  meta.ID,
		Model:               meta.Model,
		CreatedAt:           meta.CreatedAt,
		WindowMinutes:       meta.WindowMinutes,
		WindowSeconds:       meta.WindowSeconds,
		IncludeProcessNames: meta.IncludeProcessNames,
		Preset:              meta.Preset,
		Scope:               summaryScope,
		Summary:             summary,
		Findings:            findings,
		NextSteps:           steps,
		DataGaps:            gaps,
		Aliases:             aliases,
		Evidence:            ev,
		DurationMS:          meta.DurationMS,
	}, nil
}

func cleanStrings(in []string, max int) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > max {
			s = s[:max]
		}
		out = append(out, s)
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func sortEvidence(in []Evidence) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j].ID < in[j-1].ID; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

// MarshalReport is a compact helper for the service.
func MarshalReport(r Report) (string, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}
	return string(data), nil
}

// ParseStoredReport validates that stored JSON is an object; it is used by the
// export endpoint without re-running model validation. Reports stored before
// v0.3 have no preset field and read as the overview preset.
func ParseStoredReport(raw string) (Report, error) {
	var r Report
	if strings.TrimSpace(raw) == "" {
		return r, errors.New("报告为空")
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	if r.Preset == "" {
		r.Preset = PresetOverview
	}
	return r, nil
}

// ModelPrompt is the fixed system prompt. It contains no local data.
func ModelPrompt() string {
	return `你是 Windows 云桌面性能诊断助手。你只会收到选定时间窗的聚合摘要。请只根据摘要中的证据判断，不要臆造主机名、用户名、路径、命令行或日志。证据不足时明确写出 data_gaps。只输出一个 JSON 对象，字段为 summary（字符串）、findings（数组，每项含 cause 和 evidence_ids）、next_steps（字符串数组）、data_gaps（字符串数组）。findings 的每个 cause 必须引用至少一个摘要 evidence 中存在的 id；没有证据的原因不要写成确定结论。不要输出 Markdown 或代码围栏。`
}

// UserPrompt contains the bounded summary JSON only.
func UserPrompt(summaryJSON []byte) string {
	return "以下是本机时间窗摘要 JSON。请按系统要求输出 JSON 诊断：\n" + string(summaryJSON)
}

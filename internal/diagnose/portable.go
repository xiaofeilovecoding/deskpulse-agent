package diagnose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
)

// PortableRequest is the complete native-to-worker diagnostic contract.
// Numeric samples intentionally exclude hostnames and process or file data.
type PortableRequest struct {
	Profile       settings.ModelProfile `json:"profile"`
	Key           string                `json:"key"`
	Language      string                `json:"language"`
	WindowSeconds int                   `json:"window_seconds"`
	Samples       []PortableSample      `json:"samples"`
}

type PortableSample struct {
	CPU             *float64 `json:"cpu_percent"`
	EstimatedMemory *float64 `json:"estimated_memory_percent"`
}

// AnalyzePortable uses the same protocol adapters, preset and evidence
// validation as the Windows UI. It does no monitoring or credential lookup.
func AnalyzePortable(ctx context.Context, req PortableRequest) (Report, error) {
	profile, err := req.Profile.Normalize()
	if err != nil {
		return Report{}, err
	}
	if err := validateKey(req.Key); err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(req.Key) == "" {
		return Report{}, ErrNoKey
	}
	now := time.Now()
	summary, err := portableSummary(req, now)
	if err != nil {
		return Report{}, err
	}
	wire, err := json.Marshal(summary)
	if err != nil {
		return Report{}, err
	}
	return requestPortable(ctx, newProfileClient(profile, 80*time.Second), profile, req, summary, wire, now)
}

func portableSummary(req PortableRequest, now time.Time) (Summary, error) {
	if settings.ValidateAIWindow(req.WindowSeconds) != nil || len(req.Samples) > (req.WindowSeconds/5+2) {
		return Summary{}, errors.New("invalid sample window")
	}
	summary := Summary{
		WindowSeconds: req.WindowSeconds,
		WindowStart:   now.Add(-time.Duration(req.WindowSeconds) * time.Second).UTC().Format(time.RFC3339),
		WindowEnd:     now.UTC().Format(time.RFC3339),
		Scope:         "Aggregate CPU and estimated memory only, from the current computer. No identifiers or network targets.",
	}
	for _, field := range []struct {
		key, label string
		stat       *MetricStat
		value      func(PortableSample) *float64
	}{
		{"cpu", "CPU share of the computer", &summary.CPU, func(s PortableSample) *float64 { return s.CPU }},
		{"estimated_memory", "Estimated occupied physical memory, without pressure counters", &summary.Memory, func(s PortableSample) *float64 { return s.EstimatedMemory }},
	} {
		var sum, peak, minValue float64
		minValue = 100
		for _, input := range req.Samples {
			value := field.value(input)
			if value == nil {
				continue
			}
			if *value < 0 || *value > 100 || math.IsNaN(*value) || math.IsInf(*value, 0) {
				return Summary{}, errors.New("invalid sample range")
			}
			sum, peak, minValue = sum+*value, max(peak, *value), min(minValue, *value)
			field.stat.Samples++
		}
		if field.stat.Samples == 0 {
			summary.DataGaps = append(summary.DataGaps, "No "+field.key+" samples")
			continue
		}
		avg := sum / float64(field.stat.Samples)
		field.stat.Avg, field.stat.Peak, field.stat.Min = &avg, &peak, &minValue
		expected := max(1, req.WindowSeconds/5)
		missing := 100 * float64(max(0, expected-field.stat.Samples)) / float64(expected)
		summary.Coverage = append(summary.Coverage, Coverage{Metric: field.key, CadenceSeconds: 5,
			Expected: expected, Observed: field.stat.Samples, MissingPercent: &missing})
		summary.Evidence = append(summary.Evidence,
			Evidence{ID: fmt.Sprintf("E%d", len(summary.Evidence)+1), Label: field.label + " average", Value: avg, Unit: "%"},
			Evidence{ID: fmt.Sprintf("E%d", len(summary.Evidence)+2), Label: field.label + " peak", Value: peak, Unit: "%"},
		)
	}
	summary.DataGaps = append(summary.DataGaps, "Disk, network, GPU and process attribution were not part of this diagnostic snapshot")
	if len(summary.Evidence) == 0 {
		return Summary{}, errors.New("no samples available")
	}
	return summary, nil
}

func requestPortable(ctx context.Context, client *Client, profile settings.ModelProfile, req PortableRequest, summary Summary, wire []byte, now time.Time) (Report, error) {
	id, err := newID()
	if err != nil {
		return Report{}, err
	}
	task := "Interpret the brief macOS sample. State what is measurable and what requires more evidence."
	if req.Language != "en" {
		task = "解释这段 macOS 样本。说明可以测量的迹象及仍须补充的证据。"
	}
	raw, err := client.Chat(ctx, req.Key, profile.Model, desktopPrompt(req.Language), PresetUserMessage(task, wire))
	if err != nil {
		return Report{}, err
	}
	report, err := BuildReport(raw, catalogFor(summary.Evidence), nil, ReportMeta{
		ID: id, Model: profile.Model, CreatedAt: now.UnixMilli(),
		WindowSeconds: req.WindowSeconds, Preset: PresetOverview, DurationMS: time.Since(now).Milliseconds(),
	}, summary.DataGaps)
	report.Scope = summary.Scope
	return report, err
}

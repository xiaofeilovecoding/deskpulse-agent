package diagnose

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/credential"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/settings"
)

// This opt-in test uses the user's DeepSeek key only inside the Go process
// and sends the same synthetic snapshot to DeepSeek's documented endpoints.
func TestDeepSeekLive(t *testing.T) {
	if os.Getenv("DESKPULSE_DEEPSEEK_SMOKE") != "1" {
		t.Skip("optional provider smoke")
	}
	key := credential.UserEnvKey()
	if key == "" {
		t.Fatal("DeepSeek key unavailable in the process or user environment")
	}
	cpu, memory := 91.0, 39.0
	samples := make([]PortableSample, 6)
	for i := range samples {
		samples[i] = PortableSample{CPU: &cpu, EstimatedMemory: &memory}
	}
	for _, profile := range []settings.ModelProfile{
		{Provider: settings.DeepSeek, BaseURL: "https://api.deepseek.com", Model: "deepseek-flash"},
		{Provider: settings.Responses, BaseURL: "https://api.deepseek.com", Model: "deepseek-flash"},
		{Provider: settings.Messages, BaseURL: "https://api.deepseek.com/anthropic/v1", Model: "deepseek-flash"},
	} {
		t.Run(string(profile.Provider), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			report, err := AnalyzePortable(ctx, PortableRequest{
				Profile: profile, Key: key, Language: "zh-CN", WindowSeconds: 30, Samples: samples,
			})
			if err != nil {
				t.Fatalf("DeepSeek diagnostic failed with code %s", CodeOf(err))
			}
			if report.Summary == "" || len(report.Evidence) != 4 || len(report.Findings) > 3 ||
				len(report.NextSteps) == 0 || len(report.NextSteps) > 3 || len(report.DataGaps) == 0 {
				t.Fatal("report did not meet the bounded diagnostic contract")
			}
			count := 0
			for _, f := range report.Findings {
				count += len(f.EvidenceIDs)
			}
			if count == 0 {
				t.Fatal("synthetic sustained-CPU case included no validated evidence references")
			}
			t.Logf("model=%s duration_ms=%d evidence=%d valid_references=%d next_steps=%d",
				report.Model, report.DurationMS, len(report.Evidence), count, len(report.NextSteps))
		})
	}
}

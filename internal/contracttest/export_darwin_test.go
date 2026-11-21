//go:build darwin

package contracttest

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

func TestMacMetricsExportMatchesPublicContract(t *testing.T) {
	app := os.Getenv("DESKPULSE_MACOS_TEST_APP")
	if app == "" {
		t.Skip("set DESKPULSE_MACOS_TEST_APP to the built app executable")
	}
	payload, err := exec.Command(app, "--check-export").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := Validator(t, "metrics-v1")(payload); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Metrics []struct {
			Value *float64 `json:"value"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Metrics) != 5 || snapshot.Metrics[0].Value == nil || *snapshot.Metrics[0].Value != 0.3 {
		t.Fatalf("unexpected metrics export: %s", payload)
	}
}

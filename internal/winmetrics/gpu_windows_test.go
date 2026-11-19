//go:build windows

package winmetrics

import (
	"os"
	"testing"
	"time"
)

func TestGPUEngineKeyScopesTheAdapterAndEngine(t *testing.T) {
	a := gpuEngineKey("pid_120_luid_0x00000000_0x0000da20_phys_0_eng_1_engtype_Copy")
	b := gpuEngineKey("pid_172_luid_0x00000000_0x0000da20_phys_0_eng_1_engtype_Copy")
	other := gpuEngineKey("pid_172_luid_0x00000000_0x0000da20_phys_1_eng_1_engtype_Copy")
	if a == "" || a != b || a == other || gpuEngineKey("_Total") != "" {
		t.Fatal("instance grouping must combine process contributions on each distinct engine")
	}
}

func TestGPULiveSmoke(t *testing.T) {
	if os.Getenv("DESKPULSE_GPU_SMOKE") != "1" {
		t.Skip("optional hardware smoke")
	}
	var gpu gpuCounter
	defer gpu.close()
	if v := gpu.sample(time.Now(), true); v != nil || gpu.query == 0 {
		t.Fatal("GPU query unavailable or unprimed reading published")
	}
	time.Sleep(time.Second)
	if v := gpu.sample(time.Now(), true); v == nil || *v < 0 || *v > 100 {
		t.Fatal("missing or out-of-range hardware sample")
	}
	gpu.sample(time.Now(), false)
	if gpu.query != 0 {
		t.Fatal("query remained open after the UI stopped requesting it")
	}
}

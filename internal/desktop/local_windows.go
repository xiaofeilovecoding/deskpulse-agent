//go:build windows

package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/diagnose"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/maintenance"
	"github.com/xiaofeilovecoding/deskpulse-agent/internal/winmetrics"
)

var systemLanguage = sync.OnceValue(func() string {
	id, _, _ := kernel32.NewProc("GetUserDefaultUILanguage").Call()
	if id&0x3ff == 0x04 {
		return "zh-CN"
	}
	return "en"
})

func (a *App) tr(zh, en string) string {
	var language string
	if a.cfg != nil {
		language = a.cfg.Language()
	}
	if language == "" {
		language = systemLanguage()
	}
	if language == "zh-CN" {
		return zh
	}
	return en
}

func (a *App) localizedWindow(seconds int) string {
	english := fmt.Sprintf("Last %d sec", seconds)
	if seconds >= 60 && seconds%60 == 0 {
		english = fmt.Sprintf("Last %d min", seconds/60)
	}
	return a.tr(fmtWindow(seconds), english)
}

func (a *App) localizedPreset(id string) string {
	label := "Overview"
	switch id {
	case diagnose.PresetSlowDesktop:
		label = "Slow computer"
	case diagnose.PresetAppUsage:
		label = "App activity"
	case diagnose.PresetNetworkLag:
		label = "Network delays"
	}
	return a.tr(diagnose.PresetName(id), label)
}

func (a *App) confirm(title, text string) bool {
	t, _ := syscall.UTF16PtrFromString(title)
	body, _ := syscall.UTF16PtrFromString(text)
	result, _, _ := user32.NewProc("MessageBoxW").Call(a.ball, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(t)), 0x4|0x20|0x100)
	return result == 6
}

func (a *App) localFinding(snapshot winmetrics.Snapshot) (string, string, string) {
	if snapshot.Memory != nil && sustainedHigh(snapshot.MemHistory, 90) {
		return a.tr("系统内存偏紧", "System memory is under pressure"),
			a.tr("最近连续三个样本的物理内存使用率偏高。", "Physical memory usage stayed high across three samples."),
			a.tr("查看本机进程并复测。当前证据无法定位到具体网页。", "Review local processes and sample again. Page-level evidence is unavailable.")
	}
	if snapshot.CPU != nil && sustainedHigh(snapshot.CPUHistory, 90) {
		return a.tr("处理器可能是瓶颈", "The CPU may be a bottleneck"),
			a.tr("最近连续三个样本的 CPU 使用率偏高。", "CPU usage stayed high across three samples."),
			a.tr("在进程页查看同一时间段的占用。", "Review process activity during the same period.")
	}
	return a.tr("证据还不够，暂时无法定位", "There is not enough evidence to locate the slowdown yet"),
		a.tr("当前没有持续的高 CPU 或内存压力证据。", "The current samples do not establish sustained CPU or memory pressure."),
		a.tr("卡顿再次出现时保留窗口并复测。", "Keep monitoring and sample again when the slowdown recurs.")
}

func sustainedHigh(values []float64, threshold float64) bool {
	if len(values) < 3 {
		return false
	}
	for _, value := range values[len(values)-3:] {
		if value < threshold {
			return false
		}
	}
	return true
}

func (a *App) openLocalDiagnosis() {
	where, evidence, next := a.localFinding(a.source.Latest())
	prompt := a.tr("可能哪里卡", "Possible source") + "\n" + where +
		"\n\n" + a.tr("依据", "Evidence") + "\n" + evidence +
		"\n\n" + a.tr("下一步", "Next step") + "\n" + next
	if a.confirm("DeskPulse Agent", prompt+"\n\n"+a.tr("打开模型分析页面？", "Open the model analysis page?")) {
		a.openAnalysis(false)
	}
}

type exportMetric struct {
	ID      string   `json:"metric_id"`
	Value   *float64 `json:"value"`
	Unit    string   `json:"unit"`
	Quality string   `json:"quality"`
}

func metric(id, unit string, value *float64) exportMetric {
	m := exportMetric{id, value, unit, "unknown"}
	if value != nil {
		m.Quality = "ok"
	}
	return m
}

func exportSnapshot(s winmetrics.Snapshot) ([]byte, error) {
	if s.TS <= 0 {
		return nil, fmt.Errorf("no local samples yet")
	}
	var cpu, available, total *float64
	if s.CPU != nil {
		value := *s.CPU / 100
		cpu = &value
	}
	if s.Memory != nil {
		free := s.Memory.Total - s.Memory.Used
		available, total = &free, &s.Memory.Total
	}
	return json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"device_alias":   "local-export",
		"captured_at":    time.UnixMilli(s.TS).UTC().Format(time.RFC3339Nano),
		"scope":          map[string]string{"kind": "windows_session", "scope_id": "local"},
		"window_seconds": 5,
		"metrics": []exportMetric{
			metric("cpu.used_ratio", "ratio", cpu), metric("memory.available_bytes", "bytes", available),
			metric("memory.total_bytes", "bytes", total), metric("disk.read_bytes_per_sec", "bytes_per_sec", s.Disk.ReadBPS),
			metric("disk.write_bytes_per_sec", "bytes_per_sec", s.Disk.WriteBPS),
		},
	}, "", "  ")
}

func (a *App) exportMetrics() {
	data, err := exportSnapshot(a.source.Latest())
	dir := filepath.Join(a.dataDir, "exports")
	if err == nil {
		err = os.MkdirAll(dir, 0700)
	}
	path := filepath.Join(dir, "metrics-v1.json")
	if err == nil {
		f, fileErr := os.CreateTemp(dir, ".metrics-*.tmp")
		err = fileErr
		if err == nil {
			defer os.Remove(f.Name())
			_, err = f.Write(data)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(f.Name(), path)
			}
		}
	}
	if err != nil {
		ShowError(err.Error())
		return
	}
	ShowInfo(a.tr("只读指标文件已导出", "Read-only metric snapshot exported") + "\n" + path)
}

func (a *App) cacheAction(command uintptr) {
	if a.cacheBusy {
		ShowInfo(a.tr("上一项缓存操作仍在进行。", "A cache operation is already running."))
		return
	}
	cache, err := maintenance.Open(a.dataDir)
	if err != nil {
		ShowError(err.Error())
		return
	}
	defer cache.Close()
	recoveryFolder := a.cfg.Get().RecoveryFolder
	switch command {
	case trayCacheStage:
		var payload []byte
		payload, err = exportSnapshot(a.source.Latest())
		if err == nil {
			_, err = cache.StageSnapshot(payload)
		}
		if err == nil {
			ShowInfo(a.tr("已在本机保存诊断快照。", "A diagnostic snapshot was saved locally."))
		}
	case trayCacheClear:
		var plan []maintenance.Entry
		plan, err = cache.Preview(time.Now())
		if err != nil {
			break
		}
		if len(plan) == 0 {
			ShowInfo(a.tr("没有超过 7 天的 DeskPulse 缓存。", "No DeskPulse cache snapshots older than seven days."))
			return
		}
		plan = plan[:min(len(plan), 12)]
		var bytes int64
		for _, item := range plan {
			bytes += item.Size
		}
		preview := fmt.Sprintf(a.tr("归档 %d 个过期的 DeskPulse 快照到同盘恢复目录。原始大小 %s。同盘归档不会腾出空间。",
			"Archive %d expired DeskPulse snapshots on the same volume. Original size %s. Same-volume recovery does not free space."), len(plan), winmetrics.HumanBytes(float64(bytes)))
		if recoveryFolder != "" {
			preview = fmt.Sprintf(a.tr("归档 %d 个过期的 DeskPulse 快照，原始大小 %s。副本写入并校验后才移除来源快照。\n\n备份路径 %s",
				"Archive %d expired DeskPulse snapshots, %s in total. Source snapshots are removed only after backup verification.\n\nBackup folder %s"),
				len(plan), winmetrics.HumanBytes(float64(bytes)), maintenance.VaultPath(recoveryFolder))
		}
		for _, item := range plan {
			preview += fmt.Sprintf("\n%s · %d B", item.Name, item.Size)
		}
		if a.confirm("DeskPulse Agent", preview) {
			a.beginCacheWork(func(work *maintenance.Cache) (string, error) {
				if recoveryFolder != "" {
					result, err := work.ArchiveTo(plan, recoveryFolder)
					return fmt.Sprintf(a.tr("已校验备份并移除 %d 个来源快照，合计 %s。可从托盘恢复。",
						"Verified backups and removed %d source snapshots, %s in total. Restore them from the tray."),
						result.Files, winmetrics.HumanBytes(float64(result.SourceBytes))), err
				}
				moved, err := work.Archive(plan)
				return fmt.Sprintf(a.tr("已归档 %d 个文件。可用托盘菜单恢复。", "Archived %d files. Restore them from the tray menu."), moved), err
			})
		}
	case trayCacheRestore:
		prompt := a.tr("恢复 DeskPulse 已归档的诊断文件？同名文件保持原样。", "Restore archived DeskPulse diagnostic files? Existing files are preserved.")
		if recoveryFolder != "" {
			prompt += "\n\n" + maintenance.VaultPath(recoveryFolder)
		}
		if a.confirm("DeskPulse Agent", prompt) {
			a.beginCacheWork(func(work *maintenance.Cache) (string, error) {
				moved, err := work.Restore()
				if recoveryFolder != "" {
					fromBackup, backupErr := work.RestoreFrom(recoveryFolder)
					if !errors.Is(backupErr, os.ErrNotExist) {
						err = errors.Join(err, backupErr)
					}
					moved += fromBackup
				}
				return fmt.Sprintf(a.tr("已恢复 %d 个文件。", "Restored %d files."), moved), err
			})
		}
	}
	if err != nil {
		ShowError(err.Error())
	}
}

func (a *App) beginCacheWork(work func(*maintenance.Cache) (string, error)) {
	a.cacheBusy = true
	a.services.Go(func() {
		cache, err := maintenance.Open(a.dataDir)
		var result string
		if err == nil {
			result, err = work(cache)
			cache.Close()
		}
		a.cacheMu.Lock()
		a.cacheResult = result
		a.cacheError = ""
		if err != nil {
			a.cacheError = err.Error()
		}
		a.cacheMu.Unlock()
		procPostMessageW.Call(a.ball, wmAppCache, 0, 0)
	})
}

func (a *App) finishCacheWork() {
	a.cacheMu.Lock()
	result, detail := a.cacheResult, a.cacheError
	a.cacheResult, a.cacheError = "", ""
	a.cacheMu.Unlock()
	a.cacheBusy = false
	if detail != "" {
		if chars := []rune(detail); len(chars) > 1400 {
			detail = string(chars[:1400]) + "…"
		}
		ShowError(result + "\n\n" + detail)
	} else if result != "" {
		ShowInfo(result)
	}
}

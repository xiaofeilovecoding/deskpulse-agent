//go:build windows

package winmetrics

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

type procCPUState struct {
	startMS  int64
	cpu100ns uint64
	at       time.Time
}

func sampleProcesses(now time.Time, prev map[int32]procCPUState) ([]ProcessInfo, map[int32]procCPUState, error) {
	entries, err := snapshotProcessEntries()
	if err != nil {
		return nil, prev, err
	}
	cores := activeProcessorCount()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, prev, err
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return nil, prev, err
	}
	if cores < 1 {
		cores = 1
	}
	out := make([]ProcessInfo, 0, len(entries))
	next := make(map[int32]procCPUState, len(entries))
	ts := now.UnixMilli()
	deadline := time.Now().Add(350 * time.Millisecond)
	lookups := 0
	for _, e := range entries {
		if lookups >= 300 || time.Now().After(deadline) {
			break
		}
		if e.ProcessID == 0 || e.ProcessID > 0x7fffffff {
			continue
		}
		var entrySession uint32
		if windows.ProcessIdToSessionId(e.ProcessID, &entrySession) != nil || entrySession != session {
			continue
		}
		lookups++
		name := syscall.UTF16ToString(e.ExeFile[:])
		if name == "" {
			name = "未知进程"
		}
		if len(name) > 260 {
			name = name[:260]
		}
		p := ProcessInfo{
			PID:  int32(e.ProcessID),
			PPID: int32(e.ParentProcessID),
			Name: name,
			TS:   ts,
		}
		startMS, cpu, ws, private, qerr := queryProcess(e.ProcessID, user.User.Sid)
		if qerr == nil {
			p.StartMS = startMS
			p.WorkingSet = ws
			p.PrivateCommit = private
			state := procCPUState{startMS: startMS, cpu100ns: cpu, at: now}
			if old, ok := prev[p.PID]; ok && old.startMS == startMS && startMS > 0 {
				elapsed := now.Sub(old.at).Seconds()
				if cpu >= old.cpu100ns {
					p.CPU = processCPUPercent(float64(cpu-old.cpu100ns), elapsed, cores)
				}
			}
			next[p.PID] = state
			out = append(out, p)
		}
	}
	return out, next, nil
}

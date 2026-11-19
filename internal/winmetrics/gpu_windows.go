//go:build windows

package winmetrics

import (
	"math"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	pdhDLL        = syscall.NewLazyDLL("pdh.dll")
	pdhOpenQuery  = pdhDLL.NewProc("PdhOpenQueryW")
	pdhAddCounter = pdhDLL.NewProc("PdhAddEnglishCounterW")
	pdhCollect    = pdhDLL.NewProc("PdhCollectQueryData")
	pdhReadArray  = pdhDLL.NewProc("PdhGetFormattedCounterArrayW")
	pdhCloseQuery = pdhDLL.NewProc("PdhCloseQuery")
)

const (
	pdhMoreData  = 0x800007d2
	pdhFmtDouble = 0x00000200
)

type gpuCounter struct {
	query, counter uintptr
	retryAfter     time.Time
	primed         bool
}

// This collector is owned by the metric loop. On idle it releases the query,
// and on unsupported hosts it backs off while demand remains visible.
func (g *gpuCounter) sample(now time.Time, active bool) *float64 {
	if !active {
		g.close()
		return nil
	}
	if now.Before(g.retryAfter) {
		return nil
	}
	if g.query == 0 {
		if !g.open() {
			g.retryAfter = now.Add(time.Minute)
			return nil
		}
	}
	if status, _, _ := pdhCollect.Call(g.query); uint32(status) != 0 {
		g.close()
		g.retryAfter = now.Add(time.Minute)
		return nil
	}
	if !g.primed {
		g.primed = true
		return nil
	}
	return gpuUtilization(g.counter)
}

func (g *gpuCounter) open() bool {
	path := syscall.StringToUTF16Ptr(`\GPU Engine(*)\Utilization Percentage`)
	if status, _, _ := pdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&g.query))); uint32(status) != 0 {
		g.query = 0
		return false
	}
	status, _, _ := pdhAddCounter.Call(g.query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&g.counter)))
	if uint32(status) != 0 {
		g.close()
		return false
	}
	return true
}

func (g *gpuCounter) close() {
	if g.query != 0 {
		pdhCloseQuery.Call(g.query)
		g.query, g.counter = 0, 0
	}
	g.primed = false
}

type pdhValueItem struct {
	name   uintptr
	status uint32
	_      uint32
	value  float64
}

func gpuUtilization(counter uintptr) *float64 {
	var size, count uint32
	status, _, _ := pdhReadArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(status) != pdhMoreData || size == 0 || size > 4<<20 || count > 8192 {
		return nil
	}
	buffer := make([]uint64, (size+7)/8)
	start := unsafe.Pointer(&buffer[0])
	status, _, _ = pdhReadArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(start))
	itemSize := unsafe.Sizeof(pdhValueItem{})
	if uint32(status) != 0 || size > uint32(len(buffer))*8 || count > 8192 || uintptr(count)*itemSize > uintptr(size) {
		return nil
	}
	engines := make(map[string]float64)
	for _, row := range unsafe.Slice((*pdhValueItem)(start), count) {
		if row.status > 1 || row.value < 0 || math.IsNaN(row.value) || math.IsInf(row.value, 0) {
			continue
		}
		base := uintptr(start)
		if row.name < base+uintptr(count)*itemSize || row.name >= base+uintptr(size) || row.name%2 != 0 {
			continue
		}
		offset := row.name - base
		raw := unsafe.Slice((*uint16)(unsafe.Add(start, offset)), min(256, (uintptr(size)-offset)/2))
		if name := gpuEngineKey(utf16CounterName(raw)); name != "" {
			engines[name] += row.value
		}
	}
	runtime.KeepAlive(buffer)
	if len(engines) == 0 {
		return nil
	}
	busiest := 0.0
	for _, engine := range engines {
		busiest = max(busiest, engine)
	}
	busiest = min(100, busiest)
	return &busiest
}

func utf16CounterName(raw []uint16) string {
	for i, ch := range raw {
		if ch == 0 {
			return string(utf16.Decode(raw[:i]))
		}
	}
	return ""
}

func gpuEngineKey(instance string) string {
	if !strings.HasPrefix(instance, "pid_") {
		return ""
	}
	_, device, ok := strings.Cut(instance, "_luid_")
	if !ok {
		return ""
	}
	engine, _, ok := strings.Cut(device, "_engtype_")
	if !ok || !strings.Contains(engine, "_eng_") || !strings.Contains(engine, "_phys_") {
		return ""
	}
	return engine
}

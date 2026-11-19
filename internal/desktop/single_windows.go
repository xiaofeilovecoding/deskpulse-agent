//go:build windows

package desktop

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var singleInstanceMutex syscall.Handle

// ensureSingleInstance creates a per-session mutex. A second launch asks the
// existing GUI to show its panel and exits without starting samplers. The
// mutex is version-specific so v0.2 and v0.3 can coexist for side-by-side
// acceptance runs.
func ensureSingleInstance() (bool, error) {
	name, err := syscall.UTF16PtrFromString(`Local\DeskPulseAgentDesktop`)
	if err != nil {
		return false, err
	}
	h, _, e := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return false, fmt.Errorf("CreateMutexW: %w", callError(e))
	}
	if errno, ok := e.(syscall.Errno); ok && errno == 183 { // ERROR_ALREADY_EXISTS
		procCloseHandle.Call(h)
		className, _ := syscall.UTF16PtrFromString(windowClassName)
		for i := 0; i < 10; i++ {
			w, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
			if w != 0 {
				procPostMessageW.Call(w, wmAppShow, 0, 0)
				return true, nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return true, nil
	}
	singleInstanceMutex = syscall.Handle(h)
	return false, nil
}

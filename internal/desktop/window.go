//go:build windows

package desktop

// windowClassName is version-specific: the v0.3 EXE must never find or be
// found by a running v0.2 instance through FindWindowW.
const windowClassName = "DeskPulseAgentDesktop"

func windowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	if activeApp != nil {
		if handled, result := activeApp.handle(hwnd, message, wparam, lparam); handled {
			return result
		}
	}
	return defWindowProc(hwnd, message, wparam, lparam)
}

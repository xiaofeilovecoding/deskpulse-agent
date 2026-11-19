//go:build windows

package credential

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	advapi32             = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyExW    = advapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
	procRegCloseKey      = advapi32.NewProc("RegCloseKey")
)

const (
	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
	regSZ           = 1
	regExpandSZ     = 2
	envKeyName      = "DEEPSEEK_API_KEY"
)

// envKeySubKey is the registry subkey read for the user-level fallback. It is
// a variable only so tests can point it at a scratch key instead of the real
// user environment.
var envKeySubKey = `Environment`

// UserEnvKey reads DEEPSEEK_API_KEY from the process environment and, when
// empty, from the Windows user environment (HKCU\Environment). The value is
// resolved at startup and before every analysis so a key configured after the
// process started is still found. The value is never logged or written to disk.
func UserEnvKey() string {
	if v := strings.TrimSpace(os.Getenv(envKeyName)); v != "" {
		return v
	}
	return readUserRegistryKey()
}

// readUserRegistryKey reads the user-level environment variable from the
// configured subkey with the exact RegQueryValueExW signature:
// (HKEY, LPCWSTR name, DWORD reserved=0, DWORD *type, BYTE *data, DWORD *cbData).
// REG_SZ and REG_EXPAND_SZ are both accepted (setx and the System control
// panel write REG_SZ; some tools write REG_EXPAND_SZ). Failure or absence
// returns an empty string — the process must always start without the value.
func readUserRegistryKey() string {
	subKey, err := syscall.UTF16PtrFromString(envKeySubKey)
	if err != nil {
		return ""
	}
	valueName, err := syscall.UTF16PtrFromString(envKeyName)
	if err != nil {
		return ""
	}
	var h uintptr
	r1, _, _ := procRegOpenKeyExW.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(subKey)),
		0,
		uintptr(keyQueryValue),
		uintptr(unsafe.Pointer(&h)),
	)
	if r1 != 0 {
		return ""
	}
	defer procRegCloseKey.Call(h)

	// First query for the required size and the actual type.
	var size uint32
	var typ uint32
	r1, _, _ = procRegQueryValueExW.Call(
		h,
		uintptr(unsafe.Pointer(valueName)),
		0,
		uintptr(unsafe.Pointer(&typ)),
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if r1 != 0 || size == 0 || size > 4096 {
		return ""
	}
	if typ != regSZ && typ != regExpandSZ {
		return ""
	}
	buf := make([]uint16, size/2+1)
	read := size
	r1, _, _ = procRegQueryValueExW.Call(
		h,
		uintptr(unsafe.Pointer(valueName)),
		0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&read)),
	)
	if r1 != 0 {
		return ""
	}
	return strings.TrimSpace(syscall.UTF16ToString(buf[:read/2]))
}

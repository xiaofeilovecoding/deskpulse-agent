//go:build windows

package credential

import (
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The test writes synthetic values into a scratch key under HKCU\Software and
// points the lookup at it. The real user Environment key is never read or
// written here.

const scratchSubKey = `Software\DeskPulseAgentEnvKeyTest`

var (
	procRegCreateKeyExW = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteKeyW   = advapi32.NewProc("RegDeleteKeyW")
)

const (
	hkeyCurrentUsertest  = 0x80000001
	keyCreateLink        = 0x00000002
	keyAllAccessTest     = 0x000F003F
	regOptionNonVolatile = 0
)

func openScratch(t *testing.T) uintptr {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(scratchSubKey)
	if err != nil {
		t.Fatal(err)
	}
	var h uintptr
	r1, _, e := procRegCreateKeyExW.Call(
		uintptr(hkeyCurrentUsertest),
		uintptr(unsafe.Pointer(name)),
		0, 0, 0,
		uintptr(keyAllAccessTest),
		0,
		uintptr(unsafe.Pointer(&h)),
		0,
	)
	if r1 != 0 {
		t.Fatalf("create scratch key: %v", e)
	}
	return h
}

func setScratchValue(t *testing.T, h uintptr, name string, typ uint32, data []byte) {
	t.Helper()
	namePtr, _ := syscall.UTF16PtrFromString(name)
	r1, _, e := procRegSetValueExW.Call(
		h,
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(typ),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
	)
	if r1 != 0 {
		t.Fatalf("set scratch value %s: %v", name, e)
	}
}

func withScratchKey(t *testing.T) {
	t.Helper()
	// Clean up any leftover scratch key from a previous failed run.
	sub, _ := syscall.UTF16PtrFromString(scratchSubKey)
	procRegDeleteKeyW.Call(uintptr(hkeyCurrentUsertest), uintptr(unsafe.Pointer(sub)))
	old := envKeySubKey
	envKeySubKey = scratchSubKey
	t.Cleanup(func() {
		envKeySubKey = old
		sub, _ := syscall.UTF16PtrFromString(scratchSubKey)
		procRegDeleteKeyW.Call(uintptr(hkeyCurrentUsertest), uintptr(unsafe.Pointer(sub)))
	})
}

func TestUserEnvKeyReadsScratchRegistry(t *testing.T) {
	withScratchKey(t)
	h := openScratch(t)
	defer procRegCloseKey.Call(h)

	// REG_SZ, as written by setx.
	setScratchValue(t, h, envKeyName, regSZ, utf16Bytes("sk-synthetic-registry-key"))

	// The process environment must be empty for the registry path to run;
	// UserEnvKey checks os.Getenv first, so clear it for this call.
	t.Setenv(envKeyName, "")
	if got := readUserRegistryKey(); got != "sk-synthetic-registry-key" {
		t.Fatalf("registry read = %q", got)
	}

	// REG_EXPAND_SZ must be accepted too.
	setScratchValue(t, h, envKeyName, regExpandSZ, utf16Bytes("sk-synthetic-expand"))
	if got := readUserRegistryKey(); got != "sk-synthetic-expand" {
		t.Fatalf("expand read = %q", got)
	}

	// Wrong type (DWORD) is ignored, not a crash.
	setScratchValue(t, h, envKeyName, 4, []byte{1, 0, 0, 0})
	if got := readUserRegistryKey(); got != "" {
		t.Fatalf("dword read = %q, want empty", got)
	}

	// Missing value is an empty string, not an error or crash.
	procRegDeleteValueW := advapi32.NewProc("RegDeleteValueW")
	namePtr, _ := syscall.UTF16PtrFromString(envKeyName)
	procRegDeleteValueW.Call(h, uintptr(unsafe.Pointer(namePtr)))
	if got := readUserRegistryKey(); got != "" {
		t.Fatalf("missing read = %q", got)
	}
}

func TestUserEnvKeyMissingSubKeyIsSafe(t *testing.T) {
	withScratchKey(t)
	// The scratch key has not been created: the lookup must return "" without
	// crashing (regression for the 0xc0000005 from the wrong RegQueryValueExW
	// signature).
	if got := readUserRegistryKey(); got != "" {
		t.Fatalf("missing subkey read = %q", got)
	}
}

func TestUserEnvKeyPrefersProcessEnvironment(t *testing.T) {
	withScratchKey(t)
	h := openScratch(t)
	defer procRegCloseKey.Call(h)
	setScratchValue(t, h, envKeyName, regSZ, utf16Bytes("sk-from-registry"))
	t.Setenv(envKeyName, "sk-from-process")
	if got := UserEnvKey(); got != "sk-from-process" {
		t.Fatalf("process env should win, got %q", got)
	}
	t.Setenv(envKeyName, "")
	if got := UserEnvKey(); !strings.Contains(got, "sk-from-registry") {
		t.Fatalf("registry fallback = %q", got)
	}
}

// utf16Bytes encodes s as a UTF-16 payload including the terminator, matching
// what RegSetValueExW expects for REG_SZ data.
func utf16Bytes(s string) []byte {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(u)*2)
	for i, v := range u {
		out[i*2] = byte(v)
		out[i*2+1] = byte(v >> 8)
	}
	return out
}

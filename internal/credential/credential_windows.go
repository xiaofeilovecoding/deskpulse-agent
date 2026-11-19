//go:build windows

package credential

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modCrypt32    = syscall.NewLazyDLL("crypt32.dll")
	modLocal32    = syscall.NewLazyDLL("kernel32.dll")
	procProtect   = modCrypt32.NewProc("CryptProtectData")
	procUnprotect = modCrypt32.NewProc("CryptUnprotectData")
	procLocalFree = modLocal32.NewProc("LocalFree")
)

const cryptProtectUIFORBIDDEN = 0x1

func protect(data, entropy []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty data")
	}
	in := dataBlob{Size: uint32(len(data)), Data: &data[0]}
	var ent dataBlob
	if len(entropy) > 0 {
		ent = dataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	}
	var out dataBlob
	r1, _, e := procProtect.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		uintptr(unsafe.Pointer(&ent)),
		0,
		0,
		cryptProtectUIFORBIDDEN,
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, callError("CryptProtectData", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.Data)))
	if out.Data == nil || out.Size == 0 {
		return nil, errors.New("DPAPI returned empty output")
	}
	result := make([]byte, int(out.Size))
	copy(result, unsafe.Slice(out.Data, int(out.Size)))
	return result, nil
}

func unprotect(data, entropy []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty ciphertext")
	}
	in := dataBlob{Size: uint32(len(data)), Data: &data[0]}
	var ent dataBlob
	if len(entropy) > 0 {
		ent = dataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	}
	var out dataBlob
	r1, _, e := procUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		uintptr(unsafe.Pointer(&ent)),
		0,
		0,
		cryptProtectUIFORBIDDEN,
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, callError("CryptUnprotectData", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.Data)))
	if out.Data == nil || out.Size == 0 {
		return nil, errors.New("DPAPI returned empty plaintext")
	}
	result := make([]byte, int(out.Size))
	copy(result, unsafe.Slice(out.Data, int(out.Size)))
	return result, nil
}

func callError(op string, e error) error {
	if e == nil {
		return fmt.Errorf("%s failed", op)
	}
	if errno, ok := e.(syscall.Errno); ok && errno == 0 {
		return fmt.Errorf("%s failed", op)
	}
	return fmt.Errorf("%s failed: %w", op, e)
}

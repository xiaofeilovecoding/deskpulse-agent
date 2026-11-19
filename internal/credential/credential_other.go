//go:build !windows

package credential

import "errors"

func protect(_, _ []byte) ([]byte, error) {
	return nil, errors.New("DPAPI 仅支持 Windows")
}

func unprotect(_, _ []byte) ([]byte, error) {
	return nil, errors.New("DPAPI 仅支持 Windows")
}

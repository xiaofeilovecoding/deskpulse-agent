// Package credential stores the DeepSeek API key with Windows DPAPI in the
// CurrentUser scope. The plaintext key is never written to settings, SQLite,
// logs, reports or returned to the UI.
package credential

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var magic = []byte("DPDPAPI1\n")

// Store owns one credentials.dat file.
type Store struct {
	mu      sync.Mutex
	path    string
	entropy []byte
}

// Status is safe to show in the UI.
type Status struct {
	Present bool   `json:"present"`
	Path    string `json:"path"`
	Error   string `json:"error,omitempty"`
}

// New creates a store at dir/credentials.dat.
func New(dir string) *Store {
	return &Store{
		path:    filepath.Join(dir, "credentials.dat"),
		entropy: []byte("DeskPulse Agent/v0.2/DeepSeek/DPAPI"),
	}
}

// ForEndpoint binds an encrypted key to both the provider and API base URL.
// Moving its ciphertext to a different slot will fail decryption.
func ForEndpoint(dir, scope string) *Store {
	digest := sha256.Sum256([]byte(scope))
	return &Store{
		path:    filepath.Join(dir, "model-credentials", fmt.Sprintf("%x.dat", digest)),
		entropy: []byte("DeskPulse Agent/Model/DPAPI\n" + scope),
	}
}

// Path returns the ciphertext file path.
func (s *Store) Path() string { return s.path }

// Set encrypts key and atomically writes the ciphertext.
func (s *Store) Set(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API Key 不能为空")
	}
	if len(key) > 512 {
		return errors.New("API Key 过长")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return errors.New("API Key 含非法控制字符")
		}
	}
	plain := []byte(key)
	defer zero(plain)
	ciphertext, err := protect(plain, s.entropy)
	if err != nil {
		return fmt.Errorf("DPAPI 加密失败: %w", err)
	}
	if len(ciphertext) == 0 {
		return errors.New("DPAPI 加密返回空数据")
	}
	buf := make([]byte, 0, len(magic)+len(ciphertext))
	buf = append(buf, magic...)
	buf = append(buf, ciphertext...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".credentials-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

// Get decrypts the current key. A missing file returns os.ErrNotExist.
func (s *Store) Get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return "", err
	}
	if !bytes.HasPrefix(data, magic) {
		return "", errors.New("凭据文件格式无效")
	}
	plain, err := unprotect(data[len(magic):], s.entropy)
	if err != nil {
		return "", fmt.Errorf("DPAPI 解密失败: %w", err)
	}
	defer zero(plain)
	if len(plain) == 0 {
		return "", errors.New("凭据内容为空")
	}
	return string(plain), nil
}

// Clear removes the DPAPI ciphertext. Missing files are not an error.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Status reports presence and size without decrypting or exposing the key.
func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Path: s.path}
	fi, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return st
	}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Present = fi.Size() > int64(len(magic))
	// Do not include the ciphertext size; it is not needed by the UI.
	return st
}

// protect/unprotect are implemented in credential_windows.go and
// credential_other.go.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// dataBlob and helpers are kept here so the platform file only contains the
// syscall-specific calls.
type dataBlob struct {
	Size uint32
	Data *byte
}

func readUint32(data []byte) uint32 {
	if len(data) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(data)
}

// Package logging provides a small size-based rotating log writer. It never
// receives request headers, request bodies, API keys or report contents from
// callers.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingWriter is a concurrency-safe append-only log writer that rotates by
// size and keeps a bounded number of archives.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

// New opens (or creates) path. maxBytes <= 0 defaults to 2 MiB; maxFiles < 1
// defaults to 3.
func New(path string, maxBytes int64, maxFiles int) (*RotatingWriter, error) {
	if maxBytes <= 0 {
		maxBytes = 2 << 20
	}
	if maxFiles < 1 {
		maxFiles = 3
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w := &RotatingWriter{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// Path returns the active log file path.
func (w *RotatingWriter) Path() string { return w.path }

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.size = st.Size()
	return nil
}

// Write implements io.Writer and rotates before a write that would exceed the
// size budget.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotateLocked() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	// Keep archives .1 through .maxFiles, newest at .1.
	_ = os.Remove(archivePath(w.path, w.maxFiles))
	for i := w.maxFiles - 1; i >= 1; i-- {
		next := archivePath(w.path, i+1)
		prev := archivePath(w.path, i)
		_ = os.Remove(next)
		if _, err := os.Stat(prev); err == nil {
			if err := os.Rename(prev, next); err != nil {
				return fmt.Errorf("rotate log: %w", err)
			}
		}
	}
	if err := os.Remove(archivePath(w.path, 1)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(w.path, archivePath(w.path, 1)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

func archivePath(path string, n int) string {
	return fmt.Sprintf("%s.%d", path, n)
}

// Close closes the active file. It is idempotent.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Package maintenance manages DeskPulse's own diagnostic scratch files.
package maintenance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const cacheDir = "diagnostic-cache"
const recoveryDir = "diagnostic-recovery"

type Entry struct {
	Name string
	Size int64
	info fs.FileInfo
}

type Cache struct {
	root *os.Root
}

func Open(dataDir string) (*Cache, error) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{cacheDir, recoveryDir} {
		if err := root.Mkdir(dir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			root.Close()
			return nil, err
		}
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("unsafe working directory: %s", dir)
		}
	}
	return &Cache{root}, nil
}

func (c *Cache) Close() error { return c.root.Close() }

func cacheName(name string) bool {
	if filepath.Base(name) != name || !strings.HasPrefix(name, "snapshot-") || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, "snapshot-"), ".tmp")
	_, err := hex.DecodeString(id)
	return len(id) == 16 && err == nil
}

func (c *Cache) StageSnapshot(data []byte) (string, error) {
	if len(data) > 1<<20 {
		return "", errors.New("snapshot is larger than 1 MiB")
	}
	name := "snapshot-" + newID() + ".tmp"
	rel := filepath.Join(cacheDir, name)
	f, err := c.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	return rel, errors.Join(err, f.Close())
}

func newID() string {
	var value [8]byte
	rand.Read(value[:])
	return hex.EncodeToString(value[:])
}

func (c *Cache) Preview(now time.Time) ([]Entry, error) {
	items, err := fs.ReadDir(c.root.FS(), cacheDir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, item := range items {
		if !cacheName(item.Name()) {
			continue
		}
		info, err := c.root.Lstat(filepath.Join(cacheDir, item.Name()))
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < 7*24*time.Hour {
			continue
		}
		out = append(out, Entry{item.Name(), info.Size(), info})
		if len(out) >= 100 {
			break
		}
	}
	return out, nil
}

// Archive moves eligible files into the recovery directory on the same volume.
// The original bytes remain available, so no space savings are claimed.
func (c *Cache) Archive(plan []Entry) (int, error) {
	if len(plan) == 0 {
		return 0, nil
	}
	batch := filepath.Join(recoveryDir, newID())
	if err := c.root.Mkdir(batch, 0700); err != nil {
		return 0, err
	}
	var problems []error
	var moved int
	for _, entry := range plan {
		if !cacheName(entry.Name) || entry.info == nil {
			problems = append(problems, errors.New("invalid plan entry"))
			continue
		}
		source := filepath.Join(cacheDir, entry.Name)
		info, err := c.root.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, entry.info) ||
			info.Size() != entry.Size || !info.ModTime().Equal(entry.info.ModTime()) {
			problems = append(problems, fmt.Errorf("cache file changed: %s", entry.Name))
			continue
		}
		if err := c.root.Rename(source, filepath.Join(batch, entry.Name)); err != nil {
			problems = append(problems, err)
			continue
		}
		moved++
	}
	return moved, errors.Join(problems...)
}

func (c *Cache) Restore() (int, error) {
	batches, err := fs.ReadDir(c.root.FS(), recoveryDir)
	if err != nil {
		return 0, err
	}
	var restored int
	var problems []error
	for _, batch := range batches {
		if !batch.IsDir() || len(batch.Name()) != 16 {
			continue
		}
		if _, err := hex.DecodeString(batch.Name()); err != nil {
			continue
		}
		dir := filepath.Join(recoveryDir, batch.Name())
		items, err := fs.ReadDir(c.root.FS(), filepath.ToSlash(dir))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		for _, item := range items {
			if !cacheName(item.Name()) {
				continue
			}
			source := filepath.Join(dir, item.Name())
			info, err := c.root.Lstat(source)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			target := filepath.Join(cacheDir, item.Name())
			// Link creation fails atomically if a destination already exists.
			// Only remove the recovery entry after the original name is restored.
			if err := c.root.Link(source, target); err != nil {
				problems = append(problems, err)
				continue
			}
			if err := c.root.Remove(source); err != nil {
				problems = append(problems, err)
			}
			restored++
		}
	}
	return restored, errors.Join(problems...)
}

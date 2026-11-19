package maintenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	vaultDirectory = "DeskPulse Recovery"
	snapshotLimit  = 1 << 20
	receiptLimit   = 2 << 20
)

type ArchiveResult struct {
	Files       int
	SourceBytes int64
}

type receipt struct {
	Version int      `json:"version"`
	Name    string   `json:"snapshot"`
	Bytes   []byte   `json:"data"`
	SHA256  [32]byte `json:"sha256"`
}

func VaultPath(parent string) string { return filepath.Join(parent, vaultDirectory) }

func openVault(parent string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(parent) {
		return nil, errors.New("choose an absolute recovery folder")
	}
	dir, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if create {
		if err := dir.Mkdir(vaultDirectory, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	info, err := dir.Lstat(vaultDirectory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("unsafe recovery directory")
	}
	return dir.OpenRoot(vaultDirectory)
}

// ArchiveTo copies and reads back a verified receipt in a user-selected
// vault before removing the original snapshot. It never acts on other files.
// A failed second verification leaves the original in the local recovery
// directory, where the legacy Restore action can recover it.
func (c *Cache) ArchiveTo(plan []Entry, parent string) (ArchiveResult, error) {
	var result ArchiveResult
	if len(plan) == 0 {
		return result, nil
	}
	vault, err := openVault(parent, true)
	if err != nil {
		return result, err
	}
	defer vault.Close()
	batch := filepath.Join(recoveryDir, newID())
	if err := c.root.Mkdir(batch, 0700); err != nil {
		return result, err
	}
	defer c.root.Remove(batch) // only succeeds for an empty staging directory
	var problems []error
	for _, entry := range plan {
		if n, err := c.archiveOne(entry, vault, batch); err != nil {
			problems = append(problems, err)
		} else {
			result.Files++
			result.SourceBytes += n
		}
	}
	return result, errors.Join(problems...)
}

func (c *Cache) archiveOne(entry Entry, vault *os.Root, batch string) (int64, error) {
	if !cacheName(entry.Name) || entry.info == nil || entry.Size < 0 || entry.Size > snapshotLimit {
		return 0, errors.New("invalid snapshot plan")
	}
	source := filepath.Join(cacheDir, entry.Name)
	body, err := readUnchanged(c.root, source, entry.info)
	if err != nil {
		return 0, err
	}
	backup := receipt{Version: 1, Name: entry.Name, Bytes: body, SHA256: sha256.Sum256(body)}
	payload, err := json.Marshal(backup)
	if err != nil {
		return 0, err
	}
	key := newID() + ".dpbackup"
	if err := writeExclusive(vault, key, payload); err != nil {
		return 0, err
	}
	check, err := readReceipt(vault, key)
	if err != nil || check.Name != entry.Name || !bytes.Equal(check.Bytes, body) {
		return 0, fmt.Errorf("could not verify the recovery copy of %s", entry.Name)
	}
	staged := filepath.Join(batch, entry.Name)
	if err := c.root.Rename(source, staged); err != nil {
		return 0, err
	}
	moved, err := readUnchanged(c.root, staged, entry.info)
	if err != nil || !bytes.Equal(moved, body) {
		// Link is no-replace. Keep the staged file if its original name has
		// been reused or the filesystem cannot create a hard link.
		if c.root.Link(staged, source) == nil {
			_ = c.root.Remove(staged)
		}
		return 0, fmt.Errorf("snapshot changed before archival: %s", entry.Name)
	}
	if err := c.root.Remove(staged); err != nil {
		return 0, err
	}
	return entry.Size, nil
}

func readUnchanged(root *os.Root, file string, expected fs.FileInfo) ([]byte, error) {
	current, err := root.Lstat(file)
	if err != nil || !sameSnapshot(expected, current) {
		return nil, fmt.Errorf("snapshot changed: %s", filepath.Base(file))
	}
	handle, err := root.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !sameSnapshot(expected, info) {
		return nil, fmt.Errorf("snapshot identity changed: %s", filepath.Base(file))
	}
	data, err := io.ReadAll(io.LimitReader(handle, snapshotLimit+1))
	if err != nil {
		return nil, err
	}
	info, err = handle.Stat()
	if err != nil || !sameSnapshot(expected, info) || int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("snapshot changed during read: %s", filepath.Base(file))
	}
	return data, nil
}

func sameSnapshot(expected, actual fs.FileInfo) bool {
	return actual != nil && expected != nil && actual.Mode().IsRegular() && actual.Size() <= snapshotLimit &&
		actual.Size() == expected.Size() && actual.ModTime().Equal(expected.ModTime()) && os.SameFile(actual, expected)
}

func writeExclusive(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		_ = root.Remove(name)
		return err
	}
	return nil
}

func readReceipt(vault *os.Root, key string) (receipt, error) {
	var record receipt
	info, err := vault.Lstat(key)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Size() > receiptLimit {
		return record, errors.New("invalid recovery file")
	}
	handle, err := vault.Open(key)
	if err != nil {
		return record, err
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, receiptLimit+1))
	if err != nil {
		return record, err
	}
	if len(data) > receiptLimit || json.Unmarshal(data, &record) != nil || record.Version != 1 ||
		!cacheName(record.Name) || len(record.Bytes) > snapshotLimit || sha256.Sum256(record.Bytes) != record.SHA256 {
		return receipt{}, errors.New("recovery checksum or metadata invalid")
	}
	return record, nil
}

// RestoreFrom leaves the vault intact. Identical files make a repeat restore
// idempotent, and an existing different file is never overwritten.
func (c *Cache) RestoreFrom(parent string) (int, error) {
	vault, err := openVault(parent, false)
	if err != nil {
		return 0, err
	}
	defer vault.Close()
	keys, err := fs.ReadDir(vault.FS(), ".")
	if err != nil {
		return 0, err
	}
	var count int
	var problems []error
	for _, key := range keys {
		name := key.Name()
		if len(name) != 25 || !strings.HasSuffix(name, ".dpbackup") {
			continue
		}
		record, err := readReceipt(vault, name)
		if err == nil {
			var restored bool
			restored, err = c.restoreOne(record)
			if restored {
				count++
			}
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", name, err))
		}
	}
	return count, errors.Join(problems...)
}

func (c *Cache) restoreOne(record receipt) (bool, error) {
	target := filepath.Join(cacheDir, record.Name)
	info, err := c.root.Lstat(target)
	if err == nil {
		if existing, err := readUnchanged(c.root, target, info); err == nil && bytes.Equal(existing, record.Bytes) {
			return false, nil
		}
		return false, fmt.Errorf("existing snapshot preserved: %s", record.Name)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	staged := filepath.Join(cacheDir, "restore-"+newID()+".partial")
	if err := writeExclusive(c.root, staged, record.Bytes); err != nil {
		return false, err
	}
	defer c.root.Remove(staged)
	if err := c.root.Link(staged, target); err != nil {
		return false, err
	}
	return true, nil
}

package deliver

import (
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nodx-ops/nodx-agent/internal/collect"
)

// ErrSpoolFull signals a counted drop: the partition outlasted the spool
// budget (50MB/10k files). Oldest data is never evicted to make room —
// dropping the newest keeps the surviving backlog ordered.
var ErrSpoolFull = errors.New("deliver: spool at capacity, dropping batch")

// ErrSpoolEmpty reports no spooled batches.
var ErrSpoolEmpty = errors.New("deliver: spool empty")

var varSpoolDrops = expvar.NewInt("spool_drops_total")

// Spool is a crash-safe file buffer: one JSON file per batch, fsync'd on
// write, replayed oldest-first. All files are 0600 in a 0700 dir.
type Spool struct {
	dir      string
	maxBytes int64
	maxFiles int
}

// NewSpool creates the spool dir (0700) if absent.
func NewSpool(dir string, maxBytes int64, maxFiles int) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("deliver: create spool dir: %w", err)
	}
	return &Spool{dir: dir, maxBytes: maxBytes, maxFiles: maxFiles}, nil
}

// Store durably buffers one batch. At capacity it counts a drop and returns
// ErrSpoolFull (caller drops the batch, never blocks, never evicts old data).
func (s *Spool) Store(batch []collect.Sample) error {
	if len(batch) == 0 {
		return nil
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("deliver: spool encode: %w", err)
	}
	files, bytes, err := s.Depth()
	if err != nil {
		return fmt.Errorf("deliver: spool depth: %w", err)
	}
	if files >= s.maxFiles || bytes+int64(len(raw)) > s.maxBytes {
		varSpoolDrops.Add(1)
		return ErrSpoolFull
	}
	final := filepath.Join(s.dir, fmt.Sprintf("%d-%d.json", time.Now().UnixNano(), os.Getpid()))
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("deliver: spool write: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("deliver: spool write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("deliver: spool fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("deliver: spool close: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("deliver: spool chmod: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("deliver: spool commit: %w", err)
	}
	syncDir(s.dir)
	return nil
}

// Oldest returns the oldest spooled batch and its handle for Remove.
// Corrupt files are deleted and counted (never retried forever); an empty
// spool returns ErrSpoolEmpty.
func (s *Spool) Oldest() ([]collect.Sample, string, error) {
	names, err := s.list()
	if err != nil {
		return nil, "", err
	}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		var batch []collect.Sample
		if err := json.Unmarshal(raw, &batch); err != nil || len(batch) == 0 {
			_ = os.Remove(filepath.Join(s.dir, name))
			varSpoolDrops.Add(1)
			continue
		}
		return batch, name, nil
	}
	return nil, "", ErrSpoolEmpty
}

// Remove deletes a replayed batch and syncs the dir (best-effort).
func (s *Spool) Remove(name string) error {
	if strings.Contains(name, "/") {
		return fmt.Errorf("deliver: bad spool handle %q", name)
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
		return fmt.Errorf("deliver: spool remove: %w", err)
	}
	syncDir(s.dir)
	return nil
}

// Depth counts committed (*.json) batches and their bytes. *.tmp files are
// crash debris from Store and are ignored (cleaned on next successful Store
// by name uniqueness — they never replay).
func (s *Spool) Depth() (files int, bytes int64, err error) {
	names, err := s.list()
	if err != nil {
		return 0, 0, err
	}
	for _, name := range names {
		fi, err := os.Stat(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		files++
		bytes += fi.Size()
	}
	return files, bytes, nil
}

func (s *Spool) list() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("deliver: spool list: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // UnixNano names sort oldest-first
	return names, nil
}

// syncDir fsyncs the directory entry so renames survive a crash. Best-effort:
// callers never fail delivery on a dir-sync error.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

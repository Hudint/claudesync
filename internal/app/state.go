package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// State is the per-device record of the last successful sync. It is the
// common ancestor for detecting local and remote changes.
type State struct {
	Version int    `json:"version"`
	Host    string `json:"host"`
	// Files maps paths relative to the Claude directory to the sha256 of the
	// content last synced.
	Files map[string]string `json:"files"`
	// Settings is the settings.json content last written (or accepted) here.
	Settings json.RawMessage `json:"settings,omitempty"`

	LastSync   time.Time `json:"lastSync,omitempty"`
	LastResult string    `json:"lastResult,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
	Conflict   string    `json:"conflict,omitempty"`
	Blocked    []string  `json:"blocked,omitempty"`
}

func loadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Version: 1, Files: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Files == nil {
		s.Files = map[string]string{}
	}
	return &s, nil
}

func (s *State) save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// writeFileAtomic writes via a temp file and rename so readers never see a
// partially written file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claudesync-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	_ = os.Chmod(name, mode)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// --- lock ---

const staleLockAge = 10 * time.Minute

// acquireLock takes an exclusive lock file, waiting up to wait for another
// claudesync process to finish. Locks older than staleLockAge are broken.
func acquireLock(path string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) > staleLockAge {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errLocked
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var errLocked = errors.New("another claudesync process is running")

// --- backups ---

const keepBackups = 30

// backup lazily creates one timestamped directory per run and copies files
// into it before they are overwritten or deleted.
type backup struct {
	root string
	dir  string
}

func newBackup(root string) *backup { return &backup{root: root} }

func (b *backup) save(rel, src string) error {
	data, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.dir == "" {
		id := time.Now().Format("20060102-150405")
		dir := filepath.Join(b.root, id)
		for i := 2; ; i++ {
			if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
				break
			}
			dir = filepath.Join(b.root, id+"-"+strconv.Itoa(i))
		}
		b.dir = dir
	}
	dst := filepath.Join(b.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func listBackups(root string) []string {
	entries, _ := os.ReadDir(root)
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

func pruneBackups(root string) {
	ids := listBackups(root)
	for len(ids) > keepBackups {
		os.RemoveAll(filepath.Join(root, ids[0]))
		ids = ids[1:]
	}
}

// --- log ---

const maxLogSize = 1 << 20

func appendLog(path, line string) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), strings.TrimSpace(line))
}

package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hudint/claudesync/internal/jsonx"
)

// Status describes pending work without changing anything.
type Status struct {
	Host         string
	Repo         string
	Remote       string
	LastSync     time.Time
	LastResult   string
	LastError    string
	Conflict     string
	Blocked      []string
	LocalChanges []string // files changed locally since the last sync
	SettingsOps  []string // settings.json changes since the last sync
	Ahead        int      // local commits not pushed yet
	Behind       int      // remote commits not applied yet (after fetch)
	FetchError   string
}

// Status computes pending local and remote changes. With fetch, it contacts
// the remote to count incoming commits.
func (a *App) Status(ctx context.Context, fetch bool) (*Status, error) {
	if !a.Initialized() {
		return nil, ErrNotInitialized
	}
	if err := a.loadRepoConfig(); err != nil {
		return nil, err
	}
	s := &Status{
		Host: a.cfg.Host, Repo: a.cfg.RepoDir(),
		LastSync: a.st.LastSync, LastResult: a.st.LastResult, LastError: a.st.LastError,
		Conflict: a.st.Conflict, Blocked: a.st.Blocked,
	}
	s.Remote = a.git.out(ctx, "remote", "get-url", "origin")
	local, _, err := a.m.collect(a.cfg.ClaudeDir)
	if err != nil {
		return nil, err
	}
	for _, rel := range unionKeys(local, a.st.Files) {
		if !a.m.managed(rel) || local[rel].hash == a.st.Files[rel] {
			continue
		}
		switch {
		case local[rel].hash == "":
			s.LocalChanges = append(s.LocalChanges, "deleted  "+rel)
		case a.st.Files[rel] == "":
			s.LocalChanges = append(s.LocalChanges, "added    "+rel)
		default:
			s.LocalChanges = append(s.LocalChanges, "modified "+rel)
		}
	}
	if _, ls, err := a.readLocalSettings(); err != nil {
		s.SettingsOps = append(s.SettingsOps, "error: "+err.Error())
	} else {
		for _, op := range a.settingsOps(ls) {
			s.SettingsOps = append(s.SettingsOps, op.String())
		}
	}
	if fetch {
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := a.git.run(fctx, "fetch", "--quiet", "origin"); err != nil {
			s.FetchError = err.Error()
		}
	}
	remote := "refs/remotes/origin/" + a.git.branch(ctx)
	if a.git.hasRef(ctx, remote) && a.git.hasRef(ctx, "HEAD") {
		fmt.Sscan(a.git.out(ctx, "rev-list", "--count", remote+"..HEAD"), &s.Ahead)
		fmt.Sscan(a.git.out(ctx, "rev-list", "--count", "HEAD.."+remote), &s.Behind)
	} else if a.git.hasRef(ctx, "HEAD") {
		fmt.Sscan(a.git.out(ctx, "rev-list", "--count", "HEAD"), &s.Ahead)
	}
	return s, nil
}

// Pin makes a settings key specific to this device by copying its current
// value into hosts/<host>.json. With onlyHere, the key is also removed from
// the shared and OS layers, so other devices drop it. Unpin removes the
// device-specific value so the shared one applies again.
func (a *App) Pin(ctx context.Context, key string, unpin, onlyHere bool, opts SyncOptions) (*Result, error) {
	if !a.Initialized() {
		return nil, ErrNotInitialized
	}
	release, err := acquireLock(a.cfg.lockPath(), opts.LockWait)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := a.loadRepoConfig(); err != nil {
		return nil, err
	}
	path := strings.Split(key, ".")
	for _, k := range a.rc.localOnly() {
		if path[0] == k {
			return nil, fmt.Errorf("%q never leaves this device (localOnlyKeys), so it does not need pinning", k)
		}
	}
	l, err := a.loadLayers()
	if err != nil {
		return nil, err
	}
	if unpin {
		if !jsonx.Delete(l.host, path) {
			return nil, fmt.Errorf("%s is not pinned on %s", key, a.cfg.Host)
		}
	} else {
		_, ls, err := a.readLocalSettings()
		if err != nil {
			return nil, err
		}
		v, ok := jsonx.Get(ls, path)
		if !ok {
			return nil, fmt.Errorf("%s is not set in %s", key, a.cfg.settingsPath())
		}
		jsonx.Set(l.host, path, a.collapseValue(v))
		if onlyHere {
			jsonx.Delete(l.shared, path)
			jsonx.Delete(l.os, path)
		}
	}
	if err := a.saveLayers(l); err != nil {
		return nil, err
	}
	return a.syncLocked(ctx, opts)
}

// Resolve recovers from a conflict by resetting to the remote state and
// merging the local configuration again, preferring one side.
func (a *App) Resolve(ctx context.Context, prefer string, opts SyncOptions) (*Result, error) {
	if prefer != "local" && prefer != "repo" {
		return nil, fmt.Errorf("--prefer must be local or repo")
	}
	if !a.Initialized() {
		return nil, ErrNotInitialized
	}
	release, err := acquireLock(a.cfg.lockPath(), opts.LockWait)
	if err != nil {
		return nil, err
	}
	defer release()
	g := a.git
	if g.rebaseInProgress() {
		g.run(ctx, "rebase", "--abort")
	}
	if _, err := g.run(ctx, "fetch", "--quiet", "origin"); err != nil {
		return nil, err
	}
	remote := "refs/remotes/origin/" + g.branch(ctx)
	if g.hasRef(ctx, remote) {
		if _, err := g.run(ctx, "reset", "-q", "--hard", remote); err != nil {
			return nil, err
		}
		g.run(ctx, "clean", "-q", "-fd")
	}
	if err := a.rebaseline(prefer); err != nil {
		return nil, err
	}
	return a.syncLocked(ctx, opts)
}

// Backups lists backup ids, oldest first.
func (a *App) Backups() []string { return listBackups(a.cfg.backupDir()) }

// Restore copies a backup back into the Claude directory. The next sync
// pushes the restored files to the other devices.
func (a *App) Restore(id string) ([]string, error) {
	dir := filepath.Join(a.cfg.backupDir(), filepath.Base(id))
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("backup %s not found", id)
	}
	var restored []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if err := copyFile(p, filepath.Join(a.cfg.ClaudeDir, rel)); err != nil {
			return err
		}
		restored = append(restored, filepath.ToSlash(rel))
		return nil
	})
	return restored, err
}

// Check is one doctor result.
type Check struct {
	OK     bool
	Name   string
	Detail string
}

// Doctor checks the setup of this device.
func (a *App) Doctor(ctx context.Context) []Check {
	var cs []Check
	add := func(ok bool, name, detail string) { cs = append(cs, Check{ok, name, detail}) }

	if v, err := runGit(ctx, "", "--version"); err != nil {
		add(false, "git", "git not found in PATH")
	} else {
		add(true, "git", strings.TrimSpace(v))
	}
	if _, err := os.Stat(a.cfg.ClaudeDir); err != nil {
		add(false, "claude dir", a.cfg.ClaudeDir+" does not exist")
	} else {
		add(true, "claude dir", a.cfg.ClaudeDir)
	}
	add(true, "host", a.cfg.Host+" ("+a.cfg.OS+")")
	if !a.Initialized() {
		add(false, "repo", "not initialized — run `claudesync init --repo <owner/name>`")
		return cs
	}
	add(true, "repo", a.cfg.RepoDir()+" → "+a.git.out(ctx, "remote", "get-url", "origin"))
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := a.git.run(lctx, "ls-remote", "--heads", "origin"); err != nil {
		add(false, "remote access", err.Error())
	} else {
		add(true, "remote access", "ok")
	}
	if err := a.loadRepoConfig(); err != nil {
		add(false, "claudesync.json", err.Error())
	}
	for _, w := range a.warnings {
		add(false, "claudesync.json", w)
	}
	if _, _, err := a.readLocalSettings(); err != nil {
		add(false, "settings.json", err.Error())
	}
	exe, _ := os.Executable()
	if p, err := exec.LookPath(binaryName()); err != nil {
		add(false, "PATH", "claudesync is not in PATH; the Claude Code plugin hooks cannot run it")
	} else if rp, _ := filepath.EvalSymlinks(p); rp != "" {
		re, _ := filepath.EvalSymlinks(exe)
		add(rp == re, "PATH", p)
	}
	_, ls, _ := a.readLocalSettings()
	if v, ok := ls["enabledPlugins"].(jsonx.Object); ok && v[pluginID] == true {
		add(true, "plugin", pluginID+" enabled")
	} else {
		add(false, "plugin", pluginID+" not enabled; sync only runs via the timer or by hand")
	}
	if ok, detail := timerStatus(a.cfg); ok {
		add(true, "timer", detail)
	} else {
		add(false, "timer", detail)
	}
	if a.st.Conflict != "" {
		add(false, "conflict", a.st.Conflict)
	}
	if a.st.LastError != "" {
		add(false, "last sync", a.st.LastError)
	} else if !a.st.LastSync.IsZero() {
		add(true, "last sync", a.st.LastSync.Format(time.RFC1123)+": "+a.st.LastResult)
	}
	return cs
}

func binaryName() string {
	if isWindows() {
		return "claudesync.exe"
	}
	return "claudesync"
}

func isWindows() bool { return filepath.Separator == '\\' }

// ErrIsLocked reports whether err means another run holds the lock.
func ErrIsLocked(err error) bool { return errors.Is(err, errLocked) }

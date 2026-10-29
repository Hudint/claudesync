package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Hudint/claudesync/internal/jsonx"
)

// App ties the configuration, state and repository of one device together.
type App struct {
	cfg *Config
	st  *State
	rc  *RepoConfig
	m   *matcher
	git gitRepo
	Out io.Writer

	warnings []string
}

// ErrNotInitialized means claudesync init has not run on this device.
var ErrNotInitialized = errors.New("claudesync is not set up on this device; run `claudesync init --repo <owner/name>`")

// New loads the state of the device described by cfg.
func New(cfg *Config) (*App, error) {
	st, err := loadState(cfg.statePath())
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.Host != "":
	case st.Host != "":
		cfg.Host = st.Host
	default:
		cfg.Host = defaultHost()
	}
	a := &App{cfg: cfg, st: st, git: gitRepo{dir: cfg.RepoDir()}, Out: os.Stdout}
	return a, nil
}

// Config returns the device configuration.
func (a *App) Config() *Config { return a.cfg }

// State returns the device state.
func (a *App) State() *State { return a.st }

// Initialized reports whether the device has a config repository.
func (a *App) Initialized() bool { return a.git.exists() }

func (a *App) loadRepoConfig() error {
	rc, err := loadRepoConfig(a.cfg.RepoDir())
	if err != nil {
		return err
	}
	a.rc = rc
	var warns []string
	a.m, warns = newMatcher(rc)
	a.warnings = append(a.warnings, warns...)
	return nil
}

// SyncOptions control one sync run.
type SyncOptions struct {
	Offline    bool          // skip fetch and push
	NetTimeout time.Duration // limit for network operations
	LockWait   time.Duration // how long to wait for another run
}

// Result describes what a sync did.
type Result struct {
	Pushed   []string // local changes committed to the repository
	Pulled   []string // local files updated from the repository
	Removed  []string // local files deleted because they were deleted elsewhere
	Blocked  []string // local changes held back by the secret scan
	Secrets  []string // secret scan findings
	Warnings []string
	Conflict string
	NetError string
	Reload   bool // enabled plugins or marketplaces changed
}

// Changed reports whether the run changed anything locally.
func (r *Result) Changed() bool { return len(r.Pulled)+len(r.Removed) > 0 }

// Summary is a short human-readable description of the run.
func (r *Result) Summary() string {
	var parts []string
	if n := len(r.Pushed); n > 0 {
		parts = append(parts, fmt.Sprintf("pushed %s", list(r.Pushed)))
	}
	if n := len(r.Pulled); n > 0 {
		parts = append(parts, fmt.Sprintf("updated %s", list(r.Pulled)))
	}
	if n := len(r.Removed); n > 0 {
		parts = append(parts, fmt.Sprintf("removed %s", list(r.Removed)))
	}
	if len(parts) == 0 {
		parts = append(parts, "up to date")
	}
	s := strings.Join(parts, "; ")
	if r.NetError != "" {
		s += " (offline: changes are committed locally and pushed next time)"
	}
	return s
}

func list(items []string) string {
	const max = 4
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:max], ", "), len(items)-max)
}

// Notices are the parts of a result the user should act on.
func (r *Result) Notices() []string {
	var n []string
	if r.Conflict != "" {
		n = append(n, "conflict with changes from another device: "+r.Conflict+" — run `claudesync resolve --prefer local` or `--prefer repo`")
	}
	if len(r.Blocked) > 0 {
		n = append(n, fmt.Sprintf("not synced because they look like they contain secrets: %s (see `claudesync status`)", strings.Join(r.Blocked, ", ")))
	}
	if r.Reload {
		n = append(n, "plugins changed — run /reload-plugins or start a new session")
	}
	return append(n, r.Warnings...)
}

// Sync captures local changes, syncs with the remote and applies the result.
func (a *App) Sync(ctx context.Context, opts SyncOptions) (*Result, error) {
	if !a.Initialized() {
		return nil, ErrNotInitialized
	}
	release, err := acquireLock(a.cfg.lockPath(), opts.LockWait)
	if err != nil {
		return nil, err
	}
	defer release()
	return a.syncLocked(ctx, opts)
}

func (a *App) syncLocked(ctx context.Context, opts SyncOptions) (*Result, error) {
	res, err := a.doSync(ctx, opts)
	a.st.LastSync = time.Now()
	if err != nil {
		a.st.LastError = err.Error()
		a.st.LastResult = "error"
	} else {
		a.st.LastError = res.NetError
		a.st.LastResult = res.Summary()
		a.st.Conflict = res.Conflict
	}
	if serr := a.st.save(a.cfg.statePath()); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		appendLog(a.cfg.logPath(), "sync error: "+err.Error())
	} else {
		appendLog(a.cfg.logPath(), "sync: "+res.Summary()+" "+strings.Join(res.Notices(), " | "))
	}
	return res, err
}

func (a *App) doSync(ctx context.Context, opts SyncOptions) (*Result, error) {
	if opts.NetTimeout == 0 {
		opts.NetTimeout = 60 * time.Second
	}
	res := &Result{}
	a.warnings = nil
	if err := a.git.configure(ctx, a.cfg.Host); err != nil {
		return nil, err
	}
	if a.git.rebaseInProgress() {
		a.git.run(ctx, "rebase", "--abort")
	}
	if err := a.loadRepoConfig(); err != nil {
		return nil, err
	}
	repoShared := filepath.Join(a.cfg.RepoDir(), sharedDir)

	// 1. Capture local changes into the repository working tree.
	local, warns, err := a.m.collect(a.cfg.ClaudeDir)
	if err != nil {
		return nil, err
	}
	a.warnings = append(a.warnings, warns...)
	var pushed []string
	for _, rel := range unionKeys(local, a.st.Files) {
		if !a.m.managed(rel) {
			continue
		}
		l, b := local[rel].hash, a.st.Files[rel]
		if l == b {
			continue
		}
		dst := filepath.Join(repoShared, filepath.FromSlash(rel))
		if l == "" {
			if err := removeFile(dst, repoShared); err != nil {
				return nil, err
			}
		} else if h, _ := hashFile(dst); h != l {
			if err := copyFile(local[rel].path, dst); err != nil {
				return nil, err
			}
		}
		pushed = append(pushed, rel)
	}

	settingsRaw, localSettings, serr := a.readLocalSettings()
	settingsOK := serr == nil
	if serr != nil {
		a.warnings = append(a.warnings, serr.Error())
	}
	if settingsOK {
		if ops := a.settingsOps(localSettings); len(ops) > 0 {
			l, err := a.loadLayers()
			if err != nil {
				return nil, err
			}
			a.applyOps(l, ops)
			if err := a.saveLayers(l); err != nil {
				return nil, err
			}
			pushed = append(pushed, "settings.json")
		}
	}

	// 2. Stage, scan for secrets, commit.
	blocked, err := a.commit(ctx, res)
	if err != nil {
		return nil, err
	}
	for _, p := range pushed {
		if !blocked[p] {
			res.Pushed = append(res.Pushed, p)
		}
	}

	// 3. Exchange commits with the remote.
	if !opts.Offline {
		a.exchange(ctx, opts.NetTimeout, res)
	}

	// 4. Apply the repository state locally.
	if err := a.apply(local, settingsRaw, localSettings, settingsOK, blocked, res); err != nil {
		return nil, err
	}
	res.Warnings = append(res.Warnings, a.warnings...)
	return res, nil
}

func unionKeys[V any, W any](a map[string]V, b map[string]W) []string {
	seen := map[string]bool{}
	var keys []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// localRel maps a repository path to the local path it came from.
func localRel(repoPath string) string {
	if strings.HasPrefix(repoPath, sharedDir+"/") {
		rel := strings.TrimPrefix(repoPath, sharedDir+"/")
		if rel == "settings.json" {
			return "settings.json"
		}
		return rel
	}
	if strings.HasPrefix(repoPath, "os/") || strings.HasPrefix(repoPath, "hosts/") {
		return "settings.json"
	}
	return repoPath
}

// commit stages the working tree, removes changes that look like secrets
// from the index (and the working tree), and commits the rest. It returns the
// local paths that were held back.
func (a *App) commit(ctx context.Context, res *Result) (map[string]bool, error) {
	g := a.git
	if _, err := g.run(ctx, "add", "-A"); err != nil {
		return nil, err
	}
	hits, err := scanStaged(ctx, g, a.rc.SecretAllow)
	if err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	held := map[string]bool{}
	for _, h := range hits {
		res.Secrets = append(res.Secrets, fmt.Sprintf("%s: %s: %s", h.file, h.rule, h.line))
		if held[h.file] {
			continue
		}
		held[h.file] = true
		blocked[localRel(h.file)] = true
	}
	hasHead := g.hasRef(ctx, "HEAD")
	for f := range held {
		if _, err := g.run(ctx, "reset", "-q", "--", f); err != nil && hasHead {
			return nil, err
		}
		if hasHead {
			if _, err := g.run(ctx, "checkout", "HEAD", "--", f); err == nil {
				continue
			}
		}
		os.Remove(filepath.Join(a.cfg.RepoDir(), filepath.FromSlash(f)))
	}
	if blocked["settings.json"] {
		// The layers were reverted, so all settings changes are held back.
		for _, f := range []string{"shared/settings.json", "os/" + a.cfg.OS + ".json", "hosts/" + a.cfg.Host + ".json"} {
			if !held[f] {
				g.run(ctx, "reset", "-q", "--", f)
				if _, err := g.run(ctx, "checkout", "HEAD", "--", f); err != nil {
					os.Remove(filepath.Join(a.cfg.RepoDir(), filepath.FromSlash(f)))
				}
			}
		}
	}
	for b := range blocked {
		res.Blocked = append(res.Blocked, b)
	}
	sort.Strings(res.Blocked)
	a.st.Blocked = res.Blocked

	var staged []string
	for _, f := range strings.Split(g.out(ctx, "-c", "core.quotePath=false", "diff", "--cached", "--name-only", "--no-renames"), "\n") {
		if f != "" {
			staged = append(staged, f)
		}
	}
	if len(staged) == 0 {
		return blocked, nil
	}
	seen := map[string]bool{}
	var changed []string
	for _, f := range staged {
		if r := localRel(f); !seen[r] {
			seen[r] = true
			changed = append(changed, r)
		}
	}
	msg := fmt.Sprintf("sync(%s): %s\n\n%s\n", a.cfg.Host, list(changed), strings.Join(staged, "\n"))
	if _, err := g.run(ctx, "commit", "-q", "--no-verify", "-m", msg); err != nil {
		return nil, err
	}
	return blocked, nil
}

// exchange fetches, rebases local commits onto the remote and pushes.
// Network failures are recorded in res and are not fatal.
func (a *App) exchange(parent context.Context, timeout time.Duration, res *Result) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	g := a.git
	branch := g.branch(ctx)
	remote := "refs/remotes/origin/" + branch
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := g.run(ctx, "fetch", "--quiet", "--prune", "origin"); err != nil {
			res.NetError = err.Error()
			return
		}
		if g.hasRef(ctx, remote) {
			if !g.hasRef(ctx, "HEAD") {
				if _, err := g.run(ctx, "checkout", "-q", "-B", branch, remote); err != nil {
					res.NetError = err.Error()
					return
				}
			} else if _, err := g.run(ctx, "rebase", "-q", "-X", "theirs", remote); err != nil {
				files := g.out(ctx, "diff", "--name-only", "--diff-filter=U")
				g.run(context.Background(), "rebase", "--abort")
				if files == "" {
					files = "see `claudesync status`"
				}
				res.Conflict = strings.Join(strings.Fields(files), ", ")
				return
			}
		}
		if !g.hasRef(ctx, "HEAD") {
			return
		}
		if g.hasRef(ctx, remote) && g.out(ctx, "rev-list", "--count", remote+"..HEAD") == "0" {
			return
		}
		err := g.push(ctx, branch)
		if errors.Is(err, errNonFastForward) {
			continue
		}
		if err != nil {
			res.NetError = err.Error()
		}
		return
	}
	res.NetError = "remote kept changing while pushing; will retry next time"
}

// apply makes the managed local files match the repository, except for
// files edited while the sync ran and files held back by the secret scan.
func (a *App) apply(captured fileSet, settingsRaw []byte, localSettings jsonx.Object, settingsOK bool, blocked map[string]bool, res *Result) error {
	repoShared := filepath.Join(a.cfg.RepoDir(), sharedDir)
	repoFiles, _, err := a.m.collect(repoShared)
	if err != nil {
		return err
	}
	now, _, err := a.m.collect(a.cfg.ClaudeDir)
	if err != nil {
		return err
	}
	bk := newBackup(a.cfg.backupDir())
	files := map[string]string{}
	keep := func(rel string) {
		if b, ok := a.st.Files[rel]; ok {
			files[rel] = b
		}
	}
	for _, rel := range unionKeys(repoFiles, a.st.Files) {
		if !a.m.managed(rel) {
			continue
		}
		if blocked[rel] {
			keep(rel)
			continue
		}
		r, ln := repoFiles[rel].hash, now[rel].hash
		if ln != captured[rel].hash {
			a.warnings = append(a.warnings, rel+" changed during sync; it is synced next time")
			keep(rel)
			continue
		}
		dst := filepath.Join(a.cfg.ClaudeDir, filepath.FromSlash(rel))
		if r == "" {
			if ln != "" {
				if err := bk.save(rel, dst); err != nil {
					return err
				}
				if err := removeFile(dst, a.cfg.ClaudeDir); err != nil {
					return err
				}
				res.Removed = append(res.Removed, rel)
			}
			continue
		}
		if ln != r {
			if ln != "" {
				if err := bk.save(rel, dst); err != nil {
					return err
				}
			}
			if err := copyFile(repoFiles[rel].path, dst); err != nil {
				return err
			}
			res.Pulled = append(res.Pulled, rel)
		}
		files[rel] = r
	}
	a.st.Files = files

	if settingsOK && !blocked["settings.json"] {
		if err := a.applySettings(settingsRaw, localSettings, bk, res); err != nil {
			return err
		}
	}
	pruneBackups(a.cfg.backupDir())
	return nil
}

func (a *App) applySettings(raw []byte, local jsonx.Object, bk *backup, res *Result) error {
	cur, err := os.ReadFile(a.cfg.settingsPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(cur, raw) {
		a.warnings = append(a.warnings, "settings.json changed during sync; it is synced next time")
		return nil
	}
	l, err := a.loadLayers()
	if err != nil {
		return err
	}
	final := a.effective(l)
	for _, k := range a.rc.localOnly() {
		delete(final, k)
		if v, ok := local[k]; ok {
			final[k] = v
		}
	}
	if !jsonx.Equal(final, local) && !(raw == nil && len(final) == 0) {
		if raw != nil {
			if err := bk.save("settings.json", a.cfg.settingsPath()); err != nil {
				return err
			}
		}
		if err := writeFileAtomic(a.cfg.settingsPath(), jsonx.Marshal(final)); err != nil {
			return err
		}
		res.Pulled = append(res.Pulled, "settings.json")
		for _, k := range []string{"enabledPlugins", "extraKnownMarketplaces"} {
			if !jsonx.Equal(final[k], local[k]) {
				res.Reload = true
			}
		}
	}
	a.st.Settings = jsonx.Marshal(final)
	return nil
}

// rebaseline resets the sync base so the next sync merges the local
// configuration with the repository. prefer decides which side wins where
// both have a file or settings value: "repo" or "local". Items only on one
// side are kept either way.
func (a *App) rebaseline(prefer string) error {
	if err := a.loadRepoConfig(); err != nil {
		return err
	}
	local, _, err := a.m.collect(a.cfg.ClaudeDir)
	if err != nil {
		return err
	}
	repo, _, err := a.m.collect(filepath.Join(a.cfg.RepoDir(), sharedDir))
	if err != nil {
		return err
	}
	files := map[string]string{}
	for rel, r := range repo {
		if l, ok := local[rel]; ok {
			if prefer == "local" {
				files[rel] = r.hash
			} else {
				files[rel] = l.hash
			}
		}
	}
	a.st.Files = files

	_, ls, err := a.readLocalSettings()
	if err != nil {
		return err
	}
	l, err := a.loadLayers()
	if err != nil {
		return err
	}
	e := a.effective(l)
	var base any
	if prefer == "local" {
		base = jsonx.Restrict(e, ls)
	} else {
		base = jsonx.Restrict(ls, e)
	}
	a.st.Settings = jsonx.Marshal(base)
	a.st.Conflict = ""
	return nil
}

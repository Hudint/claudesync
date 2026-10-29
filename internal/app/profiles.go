package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hudint/claudesync/internal/jsonx"
)

// Profiles group devices that share a configuration context, such as
// "work" or "personal". A device's configuration is layered, lowest priority
// first: shared, its profiles (the first listed wins), its OS, the device.
// New files and settings keys go into the device's first profile; existing
// ones stay in the layer they come from.

const profilesDir = "profiles"

// fileLayer is a directory of synced files in the repository.
type fileLayer struct {
	name string // "shared" or the profile name
	dir  string
}

func (a *App) profileDir(name string) string {
	return filepath.Join(a.cfg.RepoDir(), profilesDir, name)
}

// profiles returns this device's profiles in priority order.
func (a *App) profiles() []string { return a.rc.profilesFor(a.cfg.Host) }

// fileLayers returns this device's file layers, lowest priority first. The
// last one is where new files go.
func (a *App) fileLayers() []fileLayer {
	ls := []fileLayer{{name: sharedDir, dir: filepath.Join(a.cfg.RepoDir(), sharedDir)}}
	ps := a.profiles()
	for i := len(ps) - 1; i >= 0; i-- {
		ls = append(ls, fileLayer{name: ps[i], dir: a.profileDir(ps[i])})
	}
	return ls
}

// collectRepo returns the files this device sees in the repository, each
// from the highest-priority layer that has it, and the index of that layer.
func (a *App) collectRepo(layers []fileLayer) (fileSet, map[string]int, error) {
	files := fileSet{}
	owner := map[string]int{}
	for i, l := range layers {
		fs, _, err := a.m.collect(l.dir)
		if err != nil {
			return nil, nil, err
		}
		for rel, fi := range fs {
			files[rel] = fi
			owner[rel] = i
		}
	}
	return files, owner, nil
}

// ensureDevice records this device in claudesync.json so `profile list` can
// show every device.
func (a *App) ensureDevice() error {
	if d, ok := a.rc.Devices[a.cfg.Host]; ok && d.OS == a.cfg.OS {
		return nil
	}
	return a.editRepoConfig(func(o jsonx.Object) {
		jsonx.Set(o, []string{"devices", a.cfg.Host, "os"}, a.cfg.OS)
	})
}

// editRepoConfig changes claudesync.json in place, keeping keys claudesync
// does not know, and reloads it.
func (a *App) editRepoConfig(edit func(jsonx.Object)) error {
	p := filepath.Join(a.cfg.RepoDir(), repoConfigName)
	o, err := readObject(p)
	if err != nil {
		return err
	}
	if _, ok := o["version"]; !ok {
		o["version"] = 1
	}
	edit(o)
	if err := writeObject(p, o, true); err != nil {
		return err
	}
	return a.loadRepoConfig()
}

func splitProfiles(names []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		for _, p := range strings.Split(n, ",") {
			if strings.TrimSpace(p) == "" {
				continue
			}
			s := SanitizeHost(p)
			if s == sharedDir {
				return nil, errors.New(`"shared" is reserved and cannot be a profile name`)
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func (a *App) setProfiles(names []string) error {
	return a.editRepoConfig(func(o jsonx.Object) {
		path := []string{"devices", a.cfg.Host, "profiles"}
		if len(names) == 0 {
			jsonx.Delete(o, path)
			return
		}
		list := make([]any, len(names))
		for i, n := range names {
			list[i] = n
		}
		jsonx.Set(o, path, list)
		jsonx.Set(o, []string{"devices", a.cfg.Host, "os"}, a.cfg.OS)
	})
}

// UseProfiles sets this device's profiles (first = highest priority, and
// where new files and keys go). Pending local changes are synced first with
// the old profiles; then files that only belong to the old profiles are
// removed locally (after a backup) and the new profiles are applied.
func (a *App) UseProfiles(ctx context.Context, names []string, opts SyncOptions) (*Result, error) {
	if !a.Initialized() {
		return nil, ErrNotInitialized
	}
	names, err := splitProfiles(names)
	if err != nil {
		return nil, err
	}
	release, err := acquireLock(a.cfg.lockPath(), opts.LockWait)
	if err != nil {
		return nil, err
	}
	defer release()
	first, err := a.syncLocked(ctx, opts)
	if err != nil {
		return nil, err
	}
	if first.Conflict != "" {
		return first, errors.New("resolve the conflict first (`claudesync resolve`)")
	}
	if err := a.setProfiles(names); err != nil {
		return nil, err
	}
	res, err := a.syncLocked(ctx, opts)
	if res != nil {
		res.Pushed = append(first.Pushed, res.Pushed...)
	}
	return res, err
}

// ProfileInfo describes one profile in the repository.
type ProfileInfo struct {
	Name     string
	Devices  []string
	Files    int
	Settings int // top-level settings keys
}

// ProfileList returns all profiles and the devices without one.
func (a *App) ProfileList() ([]ProfileInfo, []string, error) {
	if !a.Initialized() {
		return nil, nil, ErrNotInitialized
	}
	if err := a.loadRepoConfig(); err != nil {
		return nil, nil, err
	}
	byName := map[string]*ProfileInfo{}
	get := func(n string) *ProfileInfo {
		if byName[n] == nil {
			byName[n] = &ProfileInfo{Name: n}
		}
		return byName[n]
	}
	entries, _ := os.ReadDir(filepath.Join(a.cfg.RepoDir(), profilesDir))
	for _, e := range entries {
		if e.IsDir() {
			get(e.Name())
		}
	}
	var unassigned []string
	for host := range a.rc.Devices {
		ps := a.rc.profilesFor(host)
		if len(ps) == 0 {
			unassigned = append(unassigned, host)
		}
		for _, p := range ps {
			get(p).Devices = append(get(p).Devices, host)
		}
	}
	var out []ProfileInfo
	for _, p := range byName {
		files, _, err := a.m.collect(a.profileDir(p.Name))
		if err != nil {
			return nil, nil, err
		}
		p.Files = len(files)
		if o, err := readObject(filepath.Join(a.profileDir(p.Name), "settings.json")); err == nil {
			p.Settings = len(o)
		}
		sort.Strings(p.Devices)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(unassigned)
	return out, unassigned, nil
}

// Move puts a file, a directory or a settings key into another layer: "shared"
// (all devices) or a profile. It is removed from this device's other shared
// and profile layers; device and OS overrides are left alone.
func (a *App) Move(ctx context.Context, target, to string, opts SyncOptions) (*Result, error) {
	if !a.Initialized() {
		return nil, ErrNotInitialized
	}
	release, err := acquireLock(a.cfg.lockPath(), opts.LockWait)
	if err != nil {
		return nil, err
	}
	defer release()
	// Capture pending changes first so they move too.
	if first, err := a.syncLocked(ctx, opts); err != nil {
		return nil, err
	} else if first.Conflict != "" {
		return first, errors.New("resolve the conflict first (`claudesync resolve`)")
	}

	var dest fileLayer
	if to == sharedDir {
		dest = fileLayer{name: sharedDir, dir: filepath.Join(a.cfg.RepoDir(), sharedDir)}
	} else {
		ps, err := splitProfiles([]string{to})
		if err != nil {
			return nil, err
		}
		if len(ps) != 1 {
			return nil, errors.New("--to needs one profile name or shared")
		}
		dest = fileLayer{name: ps[0], dir: a.profileDir(ps[0])}
	}

	rel := filepath.ToSlash(target)
	if abs, err := filepath.Abs(target); err == nil {
		if r, err := filepath.Rel(a.cfg.ClaudeDir, abs); err == nil && !strings.HasPrefix(r, "..") && filepath.IsAbs(target) {
			rel = filepath.ToSlash(r)
		}
	}
	rel = strings.Trim(rel, "/")

	layers := a.fileLayers()
	repo, _, err := a.collectRepo(layers)
	if err != nil {
		return nil, err
	}
	var matched []string
	for f := range repo {
		if under(f, rel) {
			matched = append(matched, f)
		}
	}
	if len(matched) > 0 {
		for _, f := range matched {
			dst := filepath.Join(dest.dir, filepath.FromSlash(f))
			if repo[f].path != dst {
				if err := copyFile(repo[f].path, dst); err != nil {
					return nil, err
				}
			}
			for _, l := range layers {
				if l.dir != dest.dir {
					if err := removeFile(filepath.Join(l.dir, filepath.FromSlash(f)), l.dir); err != nil {
						return nil, err
					}
				}
			}
		}
	} else if err := a.moveSetting(rel, dest); err != nil {
		return nil, err
	}
	return a.syncLocked(ctx, opts)
}

func (a *App) moveSetting(key string, dest fileLayer) error {
	path := strings.Split(key, ".")
	for _, k := range a.rc.localOnly() {
		if path[0] == k {
			return fmt.Errorf("%q never leaves this device (localOnlyKeys)", k)
		}
	}
	l, err := a.loadLayers()
	if err != nil {
		return err
	}
	var merged any = l.shared
	for i := len(l.profiles) - 1; i >= 0; i-- {
		merged = jsonx.Merge(merged, l.profiles[i])
	}
	v, ok := jsonx.Get(merged.(jsonx.Object), path)
	if !ok {
		return fmt.Errorf("%s is neither a synced file nor a key in the shared or profile settings", key)
	}
	jsonx.Delete(l.shared, path)
	for _, p := range l.profiles {
		jsonx.Delete(p, path)
	}
	if err := a.saveLayers(l); err != nil {
		return err
	}
	destFile := filepath.Join(dest.dir, "settings.json")
	o, err := readObject(destFile)
	if err != nil {
		return err
	}
	jsonx.Set(o, path, v)
	return writeObject(destFile, o, dest.name == sharedDir)
}

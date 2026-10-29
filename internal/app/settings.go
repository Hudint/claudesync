package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hudint/claudesync/internal/jsonx"
)

// layers are the settings.json fragments in the repository. The effective
// settings of a device are shared, overlaid by its profiles (first listed
// wins), its OS, and finally the device itself.
type layers struct {
	shared   jsonx.Object
	profiles []jsonx.Object // priority order, like App.profiles()
	os, host jsonx.Object
}

// layerPaths returns the repository-relative settings files of this device:
// shared, profiles (priority order), OS, host.
func (a *App) layerPaths() (shared string, profiles []string, osFile, host string) {
	for _, p := range a.profiles() {
		profiles = append(profiles, profilesDir+"/"+p+"/settings.json")
	}
	return sharedDir + "/settings.json", profiles, "os/" + a.cfg.OS + ".json", "hosts/" + a.cfg.Host + ".json"
}

func (a *App) allLayerPaths() []string {
	s, ps, o, h := a.layerPaths()
	return append(append([]string{s}, ps...), o, h)
}

func (a *App) repoPath(rel string) string {
	return filepath.Join(a.cfg.RepoDir(), filepath.FromSlash(rel))
}

func readObject(p string) (jsonx.Object, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return jsonx.Object{}, nil
	}
	if err != nil {
		return nil, err
	}
	o, err := jsonx.ParseObject(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return o, nil
}

func (a *App) loadLayers() (*layers, error) {
	s, ps, o, h := a.layerPaths()
	var l layers
	var err error
	if l.shared, err = readObject(a.repoPath(s)); err != nil {
		return nil, err
	}
	for _, p := range ps {
		obj, err := readObject(a.repoPath(p))
		if err != nil {
			return nil, err
		}
		l.profiles = append(l.profiles, obj)
	}
	if l.os, err = readObject(a.repoPath(o)); err != nil {
		return nil, err
	}
	if l.host, err = readObject(a.repoPath(h)); err != nil {
		return nil, err
	}
	return &l, nil
}

func writeObject(p string, o jsonx.Object, keepEmpty bool) error {
	cur, err := os.ReadFile(p)
	exists := err == nil
	if len(o) == 0 && !keepEmpty {
		if exists {
			return os.Remove(p)
		}
		return nil
	}
	data := jsonx.Marshal(o)
	if exists && string(cur) == string(data) {
		return nil
	}
	return writeFileAtomic(p, data)
}

func (a *App) saveLayers(l *layers) error {
	s, ps, o, h := a.layerPaths()
	if err := writeObject(a.repoPath(s), l.shared, true); err != nil {
		return err
	}
	for i, p := range ps {
		if err := writeObject(a.repoPath(p), l.profiles[i], false); err != nil {
			return err
		}
	}
	if err := writeObject(a.repoPath(o), l.os, false); err != nil {
		return err
	}
	return writeObject(a.repoPath(h), l.host, false)
}

// effective returns the merged settings with templates expanded for this
// device.
func (a *App) effective(l *layers) jsonx.Object {
	var m any = l.shared
	for i := len(l.profiles) - 1; i >= 0; i-- {
		m = jsonx.Merge(m, l.profiles[i])
	}
	m = jsonx.Merge(jsonx.Merge(m, l.os), l.host)
	return jsonx.MapStrings(m, a.cfg.expand).(jsonx.Object)
}

func (a *App) collapseValue(v any) any { return jsonx.MapStrings(v, a.cfg.collapse) }

// applyOps writes local settings changes into the layer that owns each path:
// the most specific layer (device, OS, profiles, shared) that defines it.
// New paths and new array items go into the device's first profile, or into
// shared if it has none. Removals apply to every layer of this device.
func (a *App) applyOps(l *layers, ops []jsonx.Op) {
	specific := append([]jsonx.Object{l.host, l.os}, l.profiles...)
	target := l.shared
	if len(l.profiles) > 0 {
		target = l.profiles[0]
	}
	all := append(specific, l.shared)
	for _, op := range ops {
		v := a.collapseValue(op.Value)
		switch op.Kind {
		case jsonx.OpSet:
			owner := target
			for _, layer := range all {
				if jsonx.Has(layer, op.Path) {
					owner = layer
					break
				}
			}
			jsonx.Set(owner, op.Path, v)
		case jsonx.OpDelete:
			for _, layer := range all {
				jsonx.Delete(layer, op.Path)
			}
		case jsonx.OpAdd:
			owner := target
			for _, layer := range specific {
				if jsonx.IsArrayAt(layer, op.Path) {
					owner = layer
					break
				}
			}
			jsonx.AddItem(owner, op.Path, v)
		case jsonx.OpRemove:
			for _, layer := range all {
				jsonx.RemoveItem(layer, op.Path, v)
			}
		}
	}
}

func withoutKeys(o jsonx.Object, keys []string) jsonx.Object {
	out := jsonx.Clone(o).(jsonx.Object)
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

// readLocalSettings returns the raw bytes (nil if missing) and parsed object.
func (a *App) readLocalSettings() ([]byte, jsonx.Object, error) {
	raw, err := os.ReadFile(a.cfg.settingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, jsonx.Object{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	o, err := jsonx.ParseObject(raw)
	if err != nil {
		return raw, nil, fmt.Errorf("%s is not valid JSON (%v); settings are not synced until it is fixed", a.cfg.settingsPath(), err)
	}
	return raw, o, nil
}

func (a *App) appliedSettings() jsonx.Object {
	if len(a.st.Settings) == 0 {
		return jsonx.Object{}
	}
	o, err := jsonx.ParseObject(a.st.Settings)
	if err != nil {
		return jsonx.Object{}
	}
	return o
}

// settingsOps returns the local settings changes since the last sync.
func (a *App) settingsOps(local jsonx.Object) []jsonx.Op {
	keys := a.rc.localOnly()
	return jsonx.Diff(withoutKeys(a.appliedSettings(), keys), withoutKeys(local, keys))
}

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hudint/claudesync/internal/jsonx"
)

// layers are the settings.json fragments in the repository. The effective
// settings of a device are shared, overlaid by its OS, overlaid by its host.
type layers struct {
	shared, os, host jsonx.Object
}

func (a *App) layerFiles() (shared, osFile, host string) {
	r := a.cfg.RepoDir()
	return filepath.Join(r, sharedDir, "settings.json"),
		filepath.Join(r, "os", a.cfg.OS+".json"),
		filepath.Join(r, "hosts", a.cfg.Host+".json")
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
	s, o, h := a.layerFiles()
	var l layers
	var err error
	if l.shared, err = readObject(s); err != nil {
		return nil, err
	}
	if l.os, err = readObject(o); err != nil {
		return nil, err
	}
	if l.host, err = readObject(h); err != nil {
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
	s, o, h := a.layerFiles()
	if err := writeObject(s, l.shared, true); err != nil {
		return err
	}
	if err := writeObject(o, l.os, false); err != nil {
		return err
	}
	return writeObject(h, l.host, false)
}

// effective returns the merged settings with templates expanded for this
// device.
func (a *App) effective(l *layers) jsonx.Object {
	m := jsonx.Merge(jsonx.Merge(l.shared, l.os), l.host)
	return jsonx.MapStrings(m, a.cfg.expand).(jsonx.Object)
}

func (a *App) collapseValue(v any) any { return jsonx.MapStrings(v, a.cfg.collapse) }

// applyOps writes local settings changes into the layer that owns each path:
// the host layer if it defines the path, else the OS layer if it does, else
// the shared layer. Removals apply to every layer of this device.
func (a *App) applyOps(l *layers, ops []jsonx.Op) {
	owner := func(path []string, array bool) jsonx.Object {
		for _, layer := range []jsonx.Object{l.host, l.os} {
			if array && jsonx.IsArrayAt(layer, path) || !array && jsonx.Has(layer, path) {
				return layer
			}
		}
		return l.shared
	}
	all := []jsonx.Object{l.host, l.os, l.shared}
	for _, op := range ops {
		v := a.collapseValue(op.Value)
		switch op.Kind {
		case jsonx.OpSet:
			jsonx.Set(owner(op.Path, false), op.Path, v)
		case jsonx.OpDelete:
			for _, layer := range all {
				jsonx.Delete(layer, op.Path)
			}
		case jsonx.OpAdd:
			jsonx.AddItem(owner(op.Path, true), op.Path, v)
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

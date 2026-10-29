package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// entry is one synced path relative to the Claude directory.
type entry struct {
	rel     string   // slash-separated
	dir     bool     // sync the whole tree below rel
	exclude []string // sub-paths of a dir entry to skip
}

var defaultEntries = []entry{
	{rel: "CLAUDE.md"},
	{rel: "keybindings.json"},
	{rel: "rules", dir: true},
	{rel: "agents", dir: true},
	{rel: "commands", dir: true},
	{rel: "output-styles", dir: true},
	{rel: "scripts", dir: true},
	{rel: "skills", dir: true, exclude: []string{"synced", ".trash"}},
}

// deniedPaths never sync, even when listed in include: credentials, caches,
// session data, and files claudesync handles itself.
var deniedPaths = []string{
	".credentials.json",
	"settings.json",
	"settings.local.json",
	"projects",
	"history.jsonl",
	"file-history",
	"sessions",
	"session-env",
	"shell-snapshots",
	"plugins",
	"backups",
	"todos",
	"statsig",
	"ide",
	"cache",
	"paste-cache",
	"state",
	"uploads",
	"downloads",
	"bridge-spawn",
	"debug",
	"telemetry",
	"skills/synced",
	"skills/.trash",
}

// ignoredNames are skipped at any depth.
var ignoredNames = []string{".DS_Store", "Thumbs.db", "desktop.ini", ".git", "node_modules", "__pycache__", "*.swp", "*~", ".claudesync-*"}

const maxFileSize = 10 << 20

const sharedDir = "shared"

// fileSet describes the managed files of one side.
type fileSet map[string]fileInfo

type fileInfo struct {
	hash string
	path string // absolute path on disk
}

type matcher struct {
	entries []entry
	exclude []string
}

func newMatcher(rc *RepoConfig) (*matcher, []string) {
	m := &matcher{entries: append([]entry(nil), defaultEntries...), exclude: rc.Exclude}
	var warnings []string
	for _, inc := range rc.Include {
		dir := strings.HasSuffix(inc, "/")
		rel := path.Clean(strings.Trim(filepath.ToSlash(inc), "/"))
		if rel == "." || strings.HasPrefix(rel, "..") {
			warnings = append(warnings, fmt.Sprintf("include %q ignored: must be inside the Claude directory", inc))
			continue
		}
		if denied(rel) {
			warnings = append(warnings, fmt.Sprintf("include %q ignored: path is never synced", inc))
			continue
		}
		m.entries = append(m.entries, entry{rel: rel, dir: dir})
	}
	return m, warnings
}

func under(rel, prefix string) bool {
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

func denied(rel string) bool {
	for _, d := range deniedPaths {
		if under(rel, d) {
			return true
		}
	}
	return false
}

func ignoredName(name string) bool {
	for _, p := range ignoredNames {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// globMatch matches a slash path against a pattern. "dir/**" matches the
// tree below dir; a pattern without a slash also matches any base name.
func globMatch(pattern, rel string) bool {
	pattern = strings.Trim(filepath.ToSlash(pattern), "/")
	if strings.HasSuffix(pattern, "/**") {
		return under(rel, strings.TrimSuffix(pattern, "/**"))
	}
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	if under(rel, pattern) {
		return true
	}
	if !strings.Contains(pattern, "/") {
		for _, part := range strings.Split(rel, "/") {
			if ok, _ := path.Match(pattern, part); ok {
				return true
			}
		}
	}
	return false
}

// managed reports whether rel (relative to the Claude directory) is synced.
func (m *matcher) managed(rel string) bool {
	if denied(rel) {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if ignoredName(part) {
			return false
		}
	}
	for _, ex := range m.exclude {
		if globMatch(ex, rel) {
			return false
		}
	}
	for _, e := range m.entries {
		if !e.dir {
			if rel == e.rel {
				return true
			}
			continue
		}
		if !strings.HasPrefix(rel, e.rel+"/") {
			continue
		}
		skip := false
		for _, ex := range e.exclude {
			if under(rel, e.rel+"/"+ex) {
				skip = true
			}
		}
		if !skip {
			return true
		}
	}
	return false
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return hashBytes(b), nil
}

// collect walks root and returns the managed files below it, keyed by their
// path relative to root. Symlinks are followed (with a cycle guard).
func (m *matcher) collect(root string) (fileSet, []string, error) {
	files := fileSet{}
	var warnings []string
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return files, nil, nil
	}
	seen := map[string]bool{}
	var walk func(dir, relDir string) error
	walk = func(dir, relDir string) error {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil
		}
		if seen[real] {
			return nil
		}
		seen[real] = true
		defer delete(seen, real)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		for _, de := range entries {
			name := de.Name()
			if ignoredName(name) {
				continue
			}
			rel := name
			if relDir != "" {
				rel = relDir + "/" + name
			}
			abs := filepath.Join(dir, name)
			fi, err := os.Stat(abs) // follows symlinks
			if err != nil {
				continue // dangling symlink
			}
			if fi.IsDir() {
				if denied(rel) || !m.mayContain(rel) {
					continue
				}
				if err := walk(abs, rel); err != nil {
					return err
				}
				continue
			}
			if !fi.Mode().IsRegular() || !m.managed(rel) {
				continue
			}
			if fi.Size() > maxFileSize {
				warnings = append(warnings, fmt.Sprintf("skipped %s: larger than %d MB", rel, maxFileSize>>20))
				continue
			}
			h, err := hashFile(abs)
			if err != nil {
				return err
			}
			files[rel] = fileInfo{hash: h, path: abs}
		}
		return nil
	}
	err := walk(root, "")
	return files, warnings, err
}

// mayContain reports whether a directory can contain managed files, so the
// walk can prune large unrelated trees such as projects/.
func (m *matcher) mayContain(relDir string) bool {
	for _, e := range m.entries {
		if under(relDir, e.rel) && e.dir {
			return true
		}
		if strings.HasPrefix(e.rel, relDir+"/") {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileAtomic(dst, data)
}

// removeFile deletes p and any parent directories left empty, up to stop.
func removeFile(p, stop string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stop = filepath.Clean(stop)
	for dir := filepath.Dir(p); dir != stop && strings.HasPrefix(dir, stop); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break // not empty
		}
	}
	return nil
}

// --- path templating for settings values ---

const (
	tmplHome      = "{{home}}"
	tmplClaudeDir = "{{claude_dir}}"
)

func (c *Config) templatePath(p string) string {
	if isWindows() {
		return filepath.ToSlash(p)
	}
	return p
}

// expand replaces templates with this device's paths.
func (c *Config) expand(s string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	s = strings.ReplaceAll(s, tmplClaudeDir, c.templatePath(c.ClaudeDir))
	return strings.ReplaceAll(s, tmplHome, c.templatePath(c.Home))
}

// collapse replaces this device's paths with templates.
func (c *Config) collapse(s string) string {
	type repl struct{ path, token string }
	var rs []repl
	for _, p := range []struct{ path, token string }{{c.ClaudeDir, tmplClaudeDir}, {c.Home, tmplHome}} {
		if len(p.path) < 2 {
			continue
		}
		rs = append(rs, repl{p.path, p.token})
		if slash := filepath.ToSlash(p.path); slash != p.path {
			rs = append(rs, repl{slash, p.token})
		}
	}
	for _, r := range rs {
		s = replaceBounded(s, r.path, r.token)
	}
	return s
}

func isPathChar(b byte) bool {
	return b == '_' || b == '-' || b == '.' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// replaceBounded replaces old with new where old is not part of a longer path
// component (e.g. /home/al is not replaced inside /home/alice).
func replaceBounded(s, old, new string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(old)
		before := i == 0 || !(isPathChar(s[i-1]) || s[i-1] == '/' || s[i-1] == '\\')
		after := end == len(s) || !isPathChar(s[end])
		b.WriteString(s[:i])
		if before && after {
			b.WriteString(new)
		} else {
			b.WriteString(old)
		}
		s = s[end:]
	}
}

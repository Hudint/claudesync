package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Hudint/claudesync/internal/jsonx"
)

const (
	pluginMarketplace = "claudesync"
	pluginID          = "claudesync@" + pluginMarketplace
	pluginRepo        = "Hudint/claudesync"
)

// InitOptions configure claudesync init.
type InitOptions struct {
	Repo     string // owner/name, git URL or path
	Create   bool   // create a private GitHub repository with gh first
	Prefer   string // "repo" or "local": which side wins on the first merge
	NoPlugin bool   // do not enable the Claude Code plugin
}

const gitattributes = `# claudesync: keep files byte-identical on every OS
* -text
# three-way JSON merge for settings layers
/shared/settings.json merge=claudesync-json
/os/*.json merge=claudesync-json
/hosts/*.json merge=claudesync-json
`

const repoReadme = "# Claude Code configuration\n\n" +
	"Synced by [claudesync](https://github.com/" + pluginRepo + "). Do not store secrets here.\n\n" +
	"- `shared/` — CLAUDE.md, skills, agents, commands, rules, output styles, scripts and the shared `settings.json`\n" +
	"- `os/<os>.json` — settings overrides per operating system (`linux`, `darwin`, `windows`)\n" +
	"- `hosts/<host>.json` — settings overrides per device (`claudesync pin <key>`)\n" +
	"- `claudesync.json` — include/exclude rules and keys that never leave a device\n\n" +
	"Paths in settings values are stored as `{{home}}` and `{{claude_dir}}`.\n"

// Init clones (or creates) the config repository and runs the first sync.
func (a *App) Init(ctx context.Context, opts InitOptions) (*Result, error) {
	if opts.Repo == "" {
		return nil, errors.New("--repo is required (for example --repo you/claude-config)")
	}
	if opts.Prefer == "" {
		opts.Prefer = "repo"
	}
	if opts.Prefer != "repo" && opts.Prefer != "local" {
		return nil, errors.New("--prefer must be repo or local")
	}
	if a.Initialized() {
		return nil, fmt.Errorf("already initialized: %s (remove it to start over)", a.cfg.RepoDir())
	}
	if err := os.MkdirAll(a.cfg.StateDir, 0o755); err != nil {
		return nil, err
	}
	release, err := acquireLock(a.cfg.lockPath(), 0)
	if err != nil {
		return nil, err
	}
	defer release()

	if opts.Create {
		if _, err := exec.LookPath("gh"); err != nil {
			return nil, errors.New("--create needs the GitHub CLI (gh)")
		}
		cmd := exec.CommandContext(ctx, "gh", "repo", "create", opts.Repo, "--private", "--description", "Claude Code configuration synced by claudesync")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("gh repo create: %v: %s", err, strings.TrimSpace(string(out)))
		}
		fmt.Fprintf(a.Out, "created private repository %s\n", opts.Repo)
	}

	dir := a.cfg.RepoDir()
	os.RemoveAll(dir) // leftovers of a failed init
	src, useGh := cloneSource(opts.Repo)
	var out []byte
	if useGh {
		cmd := exec.CommandContext(ctx, "gh", "repo", "clone", src, dir, "--", "-q")
		cmd.Env = gitEnv()
		out, err = cmd.CombinedOutput()
	} else {
		_, err = runGit(ctx, "", "clone", "-q", src, dir)
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("clone %s: %v %s", opts.Repo, err, strings.TrimSpace(string(out)))
	}
	g := a.git
	if err := g.configure(ctx, a.cfg.Host); err != nil {
		return nil, err
	}
	if !g.hasRef(ctx, "HEAD") {
		if _, err := g.run(ctx, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
			return nil, err
		}
		cfg, _ := jsonMarshalIndent(&RepoConfig{Version: 1})
		for name, data := range map[string][]byte{
			repoConfigName:   cfg,
			".gitattributes": []byte(gitattributes),
			"README.md":      []byte(repoReadme),
		} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return nil, err
			}
		}
		if _, err := g.run(ctx, "add", "-A"); err != nil {
			return nil, err
		}
		if _, err := g.run(ctx, "commit", "-q", "--no-verify", "-m", "initialize claudesync configuration repository"); err != nil {
			return nil, err
		}
		fmt.Fprintln(a.Out, "initialized empty repository layout")
	}

	if !opts.NoPlugin {
		if err := a.enablePlugin(); err != nil {
			return nil, err
		}
	}
	a.st.Host = a.cfg.Host
	if err := a.rebaseline(opts.Prefer); err != nil {
		return nil, err
	}
	return a.syncLocked(ctx, SyncOptions{})
}

func jsonMarshalIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}

// enablePlugin adds the claudesync marketplace and plugin to the local
// settings, so they sync to every device like any other plugin.
func (a *App) enablePlugin() error {
	raw, ls, err := a.readLocalSettings()
	if err != nil {
		return err
	}
	jsonx.Set(ls, []string{"extraKnownMarketplaces", pluginMarketplace}, jsonx.Object{
		"source": jsonx.Object{"source": "github", "repo": pluginRepo},
	})
	jsonx.Set(ls, []string{"enabledPlugins", pluginID}, true)
	if raw != nil {
		if old, err := jsonx.Parse(raw); err == nil && jsonx.Equal(old, ls) {
			return nil
		}
	}
	return writeFileAtomic(a.cfg.settingsPath(), jsonx.Marshal(ls))
}

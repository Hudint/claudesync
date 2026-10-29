package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hudint/claudesync/internal/jsonx"
)

// The git merge driver runs os.Executable(), which is the test binary here.
func TestMain(m *testing.M) {
	if len(os.Args) == 5 && os.Args[1] == "merge-json" {
		if err := MergeJSON(os.Args[2], os.Args[3], os.Args[4]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type device struct {
	t   *testing.T
	cfg *Config
}

func newDevice(t *testing.T, host, goos string) *device {
	home := t.TempDir()
	cfg := &Config{
		Home: home, ClaudeDir: filepath.Join(home, ".claude"), StateDir: filepath.Join(home, ".claudesync"),
		Host: host, OS: goos,
	}
	return &device{t: t, cfg: cfg}
}

func (d *device) app() *App {
	a, err := New(d.cfg)
	if err != nil {
		d.t.Fatal(err)
	}
	a.Out = io.Discard
	return a
}

func (d *device) write(rel, content string) {
	d.t.Helper()
	p := filepath.Join(d.cfg.ClaudeDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		d.t.Fatal(err)
	}
}

func (d *device) read(rel string) string {
	d.t.Helper()
	b, err := os.ReadFile(filepath.Join(d.cfg.ClaudeDir, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (d *device) settings() jsonx.Object {
	d.t.Helper()
	o, err := jsonx.ParseObject([]byte(d.read("settings.json")))
	if err != nil {
		d.t.Fatalf("settings.json: %v", err)
	}
	return o
}

func (d *device) get(path string) any {
	v, _ := jsonx.Get(d.settings(), strings.Split(path, "."))
	return v
}

func (d *device) sync() *Result {
	d.t.Helper()
	res, err := d.app().Sync(context.Background(), SyncOptions{})
	if err != nil {
		d.t.Fatalf("%s sync: %v", d.cfg.Host, err)
	}
	if res.NetError != "" || res.Conflict != "" {
		d.t.Fatalf("%s sync: net=%q conflict=%q", d.cfg.Host, res.NetError, res.Conflict)
	}
	return res
}

func (d *device) init(remote, prefer string) *Result {
	d.t.Helper()
	res, err := d.app().Init(context.Background(), InitOptions{Repo: remote, Prefer: prefer})
	if err != nil {
		d.t.Fatalf("%s init: %v", d.cfg.Host, err)
	}
	return res
}

func (d *device) repoFile(rel string) string {
	b, err := os.ReadFile(filepath.Join(d.cfg.RepoDir(), filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func bareRemote(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return dir
}

func want(t *testing.T, what string, got, expected any) {
	t.Helper()
	if !jsonx.Equal(got, expected) {
		t.Fatalf("%s = %v, want %v", what, got, expected)
	}
}

func TestTwoDevices(t *testing.T) {
	remote := bareRemote(t)
	a := newDevice(t, "laptop", "linux")
	b := newDevice(t, "server", "linux")

	a.write("CLAUDE.md", "# from laptop\n")
	a.write("skills/foo/SKILL.md", "---\nname: foo\n---\nfoo\n")
	a.write("skills/synced/managed/SKILL.md", "synced by claude.ai\n")
	a.write("projects/x/session.jsonl", "private\n")
	a.write(".credentials.json", "{}")
	a.write("settings.json", fmt.Sprintf(`{
  "model": "opus",
  "env": {"TOKEN": "local-only"},
  "permissions": {"allow": ["Bash(ls)"]},
  "statusLine": {"type": "command", "command": "%s/scripts/status.sh"}
}`, filepath.ToSlash(a.cfg.ClaudeDir)))
	a.write("scripts/status.sh", "echo hi\n")
	a.init(remote, "repo")

	if got := a.repoFile("shared/CLAUDE.md"); got != "# from laptop\n" {
		t.Fatalf("repo CLAUDE.md = %q", got)
	}
	for _, p := range []string{"shared/skills/synced/managed/SKILL.md", "shared/projects/x/session.jsonl", "shared/.credentials.json"} {
		if a.repoFile(p) != "<missing>" {
			t.Fatalf("%s must not be synced", p)
		}
	}
	shared := a.repoFile("shared/settings.json")
	if strings.Contains(shared, "local-only") || strings.Contains(shared, a.cfg.Home) {
		t.Fatalf("shared settings leak local data:\n%s", shared)
	}
	if !strings.Contains(shared, "{{claude_dir}}/scripts/status.sh") {
		t.Fatalf("path not templated:\n%s", shared)
	}

	// Second device with its own settings and a different CLAUDE.md.
	b.write("CLAUDE.md", "# from server\n")
	b.write("settings.json", `{"theme": "light", "env": {"TOKEN": "server"}, "permissions": {"allow": ["Bash(pwd)"]}}`)
	b.init(remote, "repo")

	if got := b.read("CLAUDE.md"); got != "# from laptop\n" {
		t.Fatalf("server CLAUDE.md = %q (prefer repo)", got)
	}
	if len(b.app().Backups()) == 0 {
		t.Fatal("expected a backup of the replaced CLAUDE.md")
	}
	want(t, "server model", b.get("model"), "opus")
	want(t, "server theme", b.get("theme"), "light")
	want(t, "server env", b.get("env.TOKEN"), "server")
	want(t, "server allow", b.get("permissions.allow"), []any{"Bash(ls)", "Bash(pwd)"})
	want(t, "server statusLine", b.get("statusLine.command"), filepath.ToSlash(b.cfg.ClaudeDir)+"/scripts/status.sh")
	want(t, "server plugin", b.get("enabledPlugins."+pluginMarketplace+"@"+pluginMarketplace), true)
	if b.read("skills/foo/SKILL.md") == "<missing>" || b.read("scripts/status.sh") == "<missing>" {
		t.Fatal("skill/script not applied on server")
	}

	// The laptop picks up the server's additions.
	a.sync()
	want(t, "laptop theme", a.get("theme"), "light")
	want(t, "laptop env", a.get("env.TOKEN"), "local-only")
	want(t, "laptop allow", a.get("permissions.allow"), []any{"Bash(ls)", "Bash(pwd)"})

	// Concurrent edits to different things merge.
	a.write("CLAUDE.md", "# from laptop\n\nmore rules\n")
	b.write("agents/reviewer.md", "reviewer\n")
	b.write("commands/my command.md", "spaces\n")
	os.Remove(filepath.Join(b.cfg.ClaudeDir, "skills", "foo", "SKILL.md"))
	setSetting(t, a, "model", "sonnet")
	setSetting(t, b, "permissions.defaultMode", "plan")
	a.sync()
	b.sync()
	a.sync()
	for _, d := range []*device{a, b} {
		want(t, d.cfg.Host+" CLAUDE.md", d.read("CLAUDE.md"), "# from laptop\n\nmore rules\n")
		want(t, d.cfg.Host+" agent", d.read("agents/reviewer.md"), "reviewer\n")
		want(t, d.cfg.Host+" spaced command", d.read("commands/my command.md"), "spaces\n")
		want(t, d.cfg.Host+" deleted skill", d.read("skills/foo/SKILL.md"), "<missing>")
		want(t, d.cfg.Host+" model", d.get("model"), "sonnet")
		want(t, d.cfg.Host+" defaultMode", d.get("permissions.defaultMode"), "plan")
	}

	// Both devices change the same settings value: the later sync wins and
	// both converge.
	setSetting(t, a, "theme", "dark")
	setSetting(t, b, "theme", "solarized")
	a.sync()
	b.sync()
	a.sync()
	want(t, "laptop theme", a.get("theme"), "solarized")
	want(t, "server theme", b.get("theme"), "solarized")

	// Both edit the same line of a file: the later sync wins.
	a.write("CLAUDE.md", "laptop version\n")
	b.write("CLAUDE.md", "server version\n")
	a.sync()
	b.sync()
	a.sync()
	want(t, "laptop CLAUDE.md", a.read("CLAUDE.md"), "server version\n")
	want(t, "server CLAUDE.md", b.read("CLAUDE.md"), "server version\n")

	// Pinning keeps a device-specific value.
	if _, err := b.app().Pin(context.Background(), "model", false, false, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	setSetting(t, b, "model", "haiku")
	b.sync()
	setSetting(t, a, "model", "opus")
	a.sync()
	b.sync()
	want(t, "laptop model", a.get("model"), "opus")
	want(t, "server pinned model", b.get("model"), "haiku")
	if !strings.Contains(b.repoFile("hosts/server.json"), "haiku") {
		t.Fatalf("hosts/server.json = %s", b.repoFile("hosts/server.json"))
	}

	// --only-here removes the key from the other devices.
	setSetting(t, b, "skipDangerousModePermissionPrompt", true)
	b.sync()
	a.sync()
	want(t, "laptop got key", a.get("skipDangerousModePermissionPrompt"), true)
	if _, err := b.app().Pin(context.Background(), "skipDangerousModePermissionPrompt", false, true, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	a.sync()
	want(t, "laptop dropped key", a.get("skipDangerousModePermissionPrompt"), nil)
	want(t, "server kept key", b.get("skipDangerousModePermissionPrompt"), true)

	// Secrets are held back, and the rest still syncs.
	a.write("skills/leaky/SKILL.md", "key: sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ012345\n")
	a.write("rules/style.md", "be brief\n")
	res := a.sync()
	if len(res.Blocked) != 1 || res.Blocked[0] != "skills/leaky/SKILL.md" {
		t.Fatalf("blocked = %v", res.Blocked)
	}
	b.sync()
	want(t, "server leaky skill", b.read("skills/leaky/SKILL.md"), "<missing>")
	want(t, "server rules", b.read("rules/style.md"), "be brief\n")
	want(t, "laptop keeps leaky skill", strings.Contains(a.read("skills/leaky/SKILL.md"), "sk-ant"), true)

	// Nothing to do means no new commits.
	before := a.app().git.out(context.Background(), "rev-parse", "HEAD")
	if r := a.sync(); len(r.Pushed)+len(r.Pulled) != 0 {
		t.Fatalf("idle sync did something: %+v", r)
	}
	if after := a.app().git.out(context.Background(), "rev-parse", "HEAD"); after != before {
		t.Fatal("idle sync created a commit")
	}
}

func TestOSLayer(t *testing.T) {
	remote := bareRemote(t)
	lin := newDevice(t, "linux-box", "linux")
	win := newDevice(t, "win-box", "windows")
	lin.write("settings.json", `{"model": "opus"}`)
	lin.init(remote, "repo")
	win.init(remote, "repo")

	// A Windows-only hook command lives in os/windows.json.
	osFile := filepath.Join(win.cfg.RepoDir(), "os", "windows.json")
	if err := os.MkdirAll(filepath.Dir(osFile), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(osFile, []byte(`{"statusLine": {"type": "command", "command": "powershell -File status.ps1"}}`), 0o644)
	win.sync()
	lin.sync()
	want(t, "windows statusLine", win.get("statusLine.command"), "powershell -File status.ps1")
	want(t, "linux statusLine", lin.get("statusLine"), nil)

	// Editing the value on Windows updates the OS layer, not shared.
	setSetting(t, win, "statusLine.command", "pwsh -File status.ps1")
	win.sync()
	if !strings.Contains(win.repoFile("os/windows.json"), "pwsh") || strings.Contains(win.repoFile("shared/settings.json"), "pwsh") {
		t.Fatal("OS-layer value was not updated in place")
	}
}

func TestOfflineThenPush(t *testing.T) {
	remote := bareRemote(t)
	a := newDevice(t, "a", "linux")
	a.write("CLAUDE.md", "v1\n")
	a.init(remote, "repo")
	a.write("CLAUDE.md", "v2\n")
	res, err := a.app().Sync(context.Background(), SyncOptions{Offline: true})
	if err != nil || len(res.Pushed) != 1 {
		t.Fatalf("offline sync: %v %+v", err, res)
	}
	a.sync()
	b := newDevice(t, "b", "linux")
	b.init(remote, "repo")
	want(t, "b CLAUDE.md", b.read("CLAUDE.md"), "v2\n")
}

func setSetting(t *testing.T, d *device, path string, v any) {
	t.Helper()
	s := d.settings()
	jsonx.Set(s, strings.Split(path, "."), v)
	if err := os.WriteFile(d.cfg.settingsPath(), jsonx.Marshal(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

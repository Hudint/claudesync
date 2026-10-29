package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type gitRepo struct{ dir string }

func gitEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_MERGE_AUTOEDIT=no", "GIT_EDITOR=true")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		// Never block a hook on an interactive SSH prompt.
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
	}
	return env
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if ctx.Err() != nil {
			return out.String(), fmt.Errorf("git %s: %w", args[len(args)-1], ctx.Err())
		}
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(stripC(args), " "), err, msg)
	}
	return out.String(), nil
}

func stripC(args []string) []string {
	if len(args) > 2 && args[0] == "-C" {
		return args[2:]
	}
	return args
}

func (g gitRepo) run(ctx context.Context, args ...string) (string, error) {
	return runGit(ctx, g.dir, args...)
}

func (g gitRepo) out(ctx context.Context, args ...string) string {
	s, _ := g.run(ctx, args...)
	return strings.TrimSpace(s)
}

func (g gitRepo) exists() bool {
	_, err := os.Stat(filepath.Join(g.dir, ".git"))
	return err == nil
}

func (g gitRepo) branch(ctx context.Context) string {
	b := g.out(ctx, "symbolic-ref", "--short", "HEAD")
	if b == "" {
		b = "main"
	}
	return b
}

func (g gitRepo) hasRef(ctx context.Context, ref string) bool {
	_, err := g.run(ctx, "rev-parse", "--verify", "-q", ref+"^{commit}")
	return err == nil
}

func (g gitRepo) rebaseInProgress() bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(g.dir, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

// configure sets the repository-local options claudesync relies on.
func (g gitRepo) configure(ctx context.Context, host string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe = filepath.ToSlash(exe)
	settings := [][2]string{
		{"core.autocrlf", "false"},
		{"core.safecrlf", "false"},
		{"pull.rebase", "true"},
		{"merge.claudesync-json.name", "claudesync JSON three-way merge"},
		{"merge.claudesync-json.driver", fmt.Sprintf("%q merge-json %%O %%A %%B", exe)},
	}
	for _, kv := range settings {
		if _, err := g.run(ctx, "config", "--local", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if g.out(ctx, "config", "user.email") == "" {
		if _, err := g.run(ctx, "config", "--local", "user.email", "claudesync@"+host); err != nil {
			return err
		}
	}
	if g.out(ctx, "config", "user.name") == "" {
		if _, err := g.run(ctx, "config", "--local", "user.name", "claudesync ("+host+")"); err != nil {
			return err
		}
	}
	return nil
}

var errNonFastForward = errors.New("remote has new commits")

func (g gitRepo) push(ctx context.Context, branch string) error {
	_, err := g.run(ctx, "push", "-u", "origin", "HEAD:refs/heads/"+branch)
	if err != nil && (strings.Contains(err.Error(), "non-fast-forward") || strings.Contains(err.Error(), "fetch first") || strings.Contains(err.Error(), "rejected")) {
		return errNonFastForward
	}
	return err
}

// cloneSource turns "owner/name" into something git or gh can clone.
func cloneSource(repo string) (url string, useGh bool) {
	if strings.Contains(repo, "://") || strings.Contains(repo, "@") || strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, ".") || filepath.IsAbs(repo) {
		return repo, false
	}
	if strings.Count(repo, "/") == 1 {
		if _, err := exec.LookPath("gh"); err == nil {
			return repo, true
		}
		return "https://github.com/" + repo + ".git", false
	}
	return repo, false
}

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Hook handles a Claude Code hook event. It always exits successfully so a
// sync problem never blocks a session; problems are reported as a
// systemMessage instead.
func Hook(event string, stdin io.Reader, out io.Writer) {
	io.Copy(io.Discard, stdin)
	cfg, err := LoadConfig()
	if err != nil {
		return
	}
	a, err := New(cfg)
	if err != nil || !a.Initialized() {
		return
	}
	switch event {
	case "session-start":
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		res, err := a.Sync(ctx, SyncOptions{NetTimeout: 15 * time.Second, LockWait: 5 * time.Second})
		var msg string
		switch {
		case ErrIsLocked(err):
			return
		case err != nil:
			msg = "claudesync: sync failed: " + err.Error()
		default:
			var lines []string
			if res.Changed() {
				lines = append(lines, res.Summary())
			}
			lines = append(lines, res.Notices()...)
			if len(lines) > 0 {
				msg = "claudesync: " + strings.Join(lines, "; ")
			}
		}
		if msg != "" {
			json.NewEncoder(out).Encode(map[string]string{"systemMessage": msg})
		}
	default: // config-change, session-end, …: push in the background
		if err := SpawnBackground(cfg); err != nil {
			appendLog(cfg.logPath(), "hook "+event+": "+err.Error())
		}
	}
}

// SpawnBackground starts a detached `claudesync sync` that waits briefly (so
// bursts of changes become one commit) and logs to the claudesync log.
func SpawnBackground(cfg *Config) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(cfg.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "sync", "--quiet", "--delay", "3s", "--lock-wait", "90s")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start background sync: %w", err)
	}
	return cmd.Process.Release()
}

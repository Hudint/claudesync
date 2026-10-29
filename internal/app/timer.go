package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const timerName = "claudesync"

// envForTimer passes through the variables that change where claudesync
// looks, so the timer sees the same paths as the shell that installed it.
func envForTimer() map[string]string {
	env := map[string]string{}
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CLAUDESYNC_HOME", "CLAUDESYNC_HOST"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// InstallTimer schedules `claudesync sync --quiet` every interval using the
// platform scheduler: systemd user timer (or cron) on Linux, launchd on
// macOS, Task Scheduler on Windows.
func InstallTimer(cfg *Config, interval time.Duration) (string, error) {
	if interval < time.Minute {
		interval = time.Minute
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	switch cfg.OS {
	case "linux":
		if detail, err := installSystemd(cfg, exe, interval); err == nil {
			return detail, nil
		} else if _, cerr := exec.LookPath("crontab"); cerr != nil {
			return "", fmt.Errorf("systemd user timer failed (%v) and crontab is not available", err)
		}
		return installCron(exe, interval)
	case "darwin":
		return installLaunchd(cfg, exe, interval)
	case "windows":
		return installSchtasks(cfg, exe, interval)
	}
	return installCron(exe, interval)
}

// UninstallTimer removes whatever InstallTimer set up.
func UninstallTimer(cfg *Config) error {
	switch cfg.OS {
	case "linux":
		dir := systemdDir(cfg)
		exec.Command("systemctl", "--user", "disable", "--now", timerName+".timer").Run()
		os.Remove(filepath.Join(dir, timerName+".timer"))
		os.Remove(filepath.Join(dir, timerName+".service"))
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		return removeCron()
	case "darwin":
		p := launchdPlist(cfg)
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), p).Run()
		return removeIfExists(p)
	case "windows":
		exec.Command("schtasks", "/Delete", "/F", "/TN", timerName).Run()
		return removeIfExists(filepath.Join(cfg.StateDir, "claudesync-hidden.vbs"))
	}
	return removeCron()
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func timerStatus(cfg *Config) (bool, string) {
	switch cfg.OS {
	case "linux":
		if exec.Command("systemctl", "--user", "is-active", "--quiet", timerName+".timer").Run() == nil {
			return true, "systemd user timer active"
		}
		if out, err := exec.Command("crontab", "-l").Output(); err == nil && strings.Contains(string(out), cronMarker) {
			return true, "cron job installed"
		}
	case "darwin":
		if _, err := os.Stat(launchdPlist(cfg)); err == nil {
			return true, "launchd agent installed"
		}
	case "windows":
		if exec.Command("schtasks", "/Query", "/TN", timerName).Run() == nil {
			return true, "scheduled task installed"
		}
	}
	return false, "no timer installed (`claudesync install-timer`) — sync runs only from Claude Code hooks or by hand"
}

// --- systemd ---

func systemdDir(cfg *Config) string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user")
	}
	return filepath.Join(cfg.Home, ".config", "systemd", "user")
}

func installSystemd(cfg *Config, exe string, interval time.Duration) (string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", err
	}
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		return "", fmt.Errorf("no systemd user session: %w", err)
	}
	dir := systemdDir(cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var env strings.Builder
	for k, v := range envForTimer() {
		fmt.Fprintf(&env, "Environment=%s=%s\n", k, strconv.Quote(v))
	}
	service := fmt.Sprintf(`[Unit]
Description=claudesync: sync Claude Code configuration
After=network-online.target

[Service]
Type=oneshot
%sExecStart=%s sync --quiet
`, env.String(), strconv.Quote(exe))
	timer := fmt.Sprintf(`[Unit]
Description=Run claudesync every %s

[Timer]
OnBootSec=2min
OnUnitActiveSec=%ds
Persistent=true

[Install]
WantedBy=timers.target
`, interval, int(interval.Seconds()))
	if err := os.WriteFile(filepath.Join(dir, timerName+".service"), []byte(service), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, timerName+".timer"), []byte(timer), 0o644); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", timerName + ".timer"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	detail := fmt.Sprintf("systemd user timer every %s (%s)", interval, dir)
	if out, _ := exec.Command("loginctl", "show-user", os.Getenv("USER"), "-p", "Linger").Output(); strings.Contains(string(out), "Linger=no") {
		detail += "\nnote: run `loginctl enable-linger` so the timer also runs while you are logged out"
	}
	return detail, nil
}

// --- cron ---

const cronMarker = "# claudesync"

func installCron(exe string, interval time.Duration) (string, error) {
	if err := removeCron(); err != nil {
		return "", err
	}
	mins := int(interval.Minutes())
	if mins < 1 {
		mins = 1
	}
	var env strings.Builder
	for k, v := range envForTimer() {
		fmt.Fprintf(&env, "%s=%s ", k, strconv.Quote(v))
	}
	line := fmt.Sprintf("*/%d * * * * %s%s sync --quiet >/dev/null 2>&1 %s\n", mins, env.String(), strconv.Quote(exe), cronMarker)
	cur, _ := exec.Command("crontab", "-l").Output()
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(string(cur) + line)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("crontab: %v: %s", err, out)
	}
	return fmt.Sprintf("cron job every %d minutes", mins), nil
}

func removeCron() error {
	cur, err := exec.Command("crontab", "-l").Output()
	if err != nil || !strings.Contains(string(cur), cronMarker) {
		return nil
	}
	var keep []string
	for _, l := range strings.Split(string(cur), "\n") {
		if !strings.Contains(l, cronMarker) && l != "" {
			keep = append(keep, l)
		}
	}
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(strings.Join(keep, "\n") + "\n")
	return cmd.Run()
}

// --- launchd ---

func launchdPlist(cfg *Config) string {
	return filepath.Join(cfg.Home, "Library", "LaunchAgents", "com.github.hudint.claudesync.plist")
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func installLaunchd(cfg *Config, exe string, interval time.Duration) (string, error) {
	p := launchdPlist(cfg)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	env := map[string]string{"PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
	for k, v := range envForTimer() {
		env[k] = v
	}
	var envXML strings.Builder
	for k, v := range env {
		fmt.Fprintf(&envXML, "\t\t<key>%s</key><string>%s</string>\n", xmlEscape(k), xmlEscape(v))
	}
	logPath := filepath.Join(cfg.StateDir, "timer.log")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.github.hudint.claudesync</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string><string>sync</string><string>--quiet</string>
	</array>
	<key>StartInterval</key><integer>%d</integer>
	<key>RunAtLoad</key><true/>
	<key>EnvironmentVariables</key>
	<dict>
%s	</dict>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, xmlEscape(exe), int(interval.Seconds()), envXML.String(), xmlEscape(logPath), xmlEscape(logPath))
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", domain, p).Run()
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain, p).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("launchctl", "load", "-w", p).CombinedOutput(); err2 != nil {
			return "", fmt.Errorf("launchctl: %v: %s %s", err, out, out2)
		}
	}
	return fmt.Sprintf("launchd agent every %s (%s)", interval, p), nil
}

// --- Windows Task Scheduler ---

func installSchtasks(cfg *Config, exe string, interval time.Duration) (string, error) {
	// A VBScript wrapper runs claudesync without flashing a console window.
	vbs := filepath.Join(cfg.StateDir, "claudesync-hidden.vbs")
	var env strings.Builder
	for k, v := range envForTimer() {
		fmt.Fprintf(&env, "sh.Environment(\"Process\")(%q) = %q\r\n", k, v)
	}
	script := "Set sh = CreateObject(\"WScript.Shell\")\r\n" + env.String() +
		fmt.Sprintf("sh.Run \"\"\"%s\"\" sync --quiet\", 0, False\r\n", exe)
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(vbs, []byte(script), 0o644); err != nil {
		return "", err
	}
	mins := int(interval.Minutes())
	tr := fmt.Sprintf(`wscript.exe "%s"`, vbs)
	out, err := exec.Command("schtasks", "/Create", "/F", "/SC", "MINUTE", "/MO", strconv.Itoa(mins), "/TN", timerName, "/TR", tr).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("schtasks: %v: %s", err, out)
	}
	return fmt.Sprintf("scheduled task %q every %d minutes", timerName, mins), nil
}

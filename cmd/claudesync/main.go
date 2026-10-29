// Command claudesync keeps Claude Code configuration in sync across devices
// through a private git repository.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Hudint/claudesync/internal/app"
)

var version = "dev"

const usage = `claudesync keeps your Claude Code configuration in sync across devices.

Usage:
  claudesync init --repo <owner/name|url> [--create] [--prefer repo|local] [--profile a,b] [--host <name>] [--no-plugin]
  claudesync sync [--quiet] [--offline] [--timeout 60s]
  claudesync status [--fetch]
  claudesync pin [--only-here] <key>   keep a settings value specific to this device
  claudesync unpin <settings.key>      use the shared value again
  claudesync profile [list]            profiles and their devices
  claudesync profile use <a[,b]>       put this device into profiles (first wins, new things go there)
  claudesync profile use --none        leave all profiles
  claudesync move <path|key> --to <profile|shared>
  claudesync promote <path|key>        move to shared, so every device gets it
  claudesync resolve --prefer local|repo
  claudesync backups                   list backups taken before overwriting files
  claudesync restore <backup-id>
  claudesync install-timer [--interval 5m]
  claudesync uninstall-timer
  claudesync doctor
  claudesync version

Synced: CLAUDE.md, skills, agents, commands, rules, output-styles, scripts,
keybindings.json and settings.json (including plugins). Never synced:
credentials, ~/.claude.json, sessions, history, and the settings keys listed in
localOnlyKeys (env, apiKeyHelper, ...).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	// Internal commands that must not print anything unexpected.
	switch cmd {
	case "hook":
		event := ""
		if len(args) > 0 {
			event = args[0]
		}
		app.Hook(event, os.Stdin, os.Stdout)
		return
	case "merge-json":
		if len(args) != 3 {
			fail(errors.New("usage: claudesync merge-json <base> <ours> <theirs>"))
		}
		if err := app.MergeJSON(args[0], args[1], args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "claudesync merge-json:", err)
			os.Exit(1)
		}
		return
	case "version", "--version", "-v":
		fmt.Println("claudesync", version)
		return
	case "help", "--help", "-h":
		fmt.Print(usage)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg, err := app.LoadConfig()
	if err != nil {
		fail(err)
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	switch cmd {
	case "init":
		repo := fs.String("repo", "", "config repository (owner/name, git URL or path)")
		create := fs.Bool("create", false, "create the repository as a private GitHub repo (needs gh)")
		prefer := fs.String("prefer", "repo", "which side wins where both have a value: repo or local")
		host := fs.String("host", "", "name of this device (default: hostname)")
		noPlugin := fs.Bool("no-plugin", false, "do not enable the claudesync Claude Code plugin")
		profile := fs.String("profile", "", "comma-separated profiles for this device (first wins)")
		fs.Parse(args)
		if *host != "" {
			cfg.Host = app.SanitizeHost(*host)
		}
		a := newApp(cfg)
		res, err := a.Init(ctx, app.InitOptions{Repo: *repo, Create: *create, Prefer: *prefer, NoPlugin: *noPlugin, Profiles: []string{*profile}})
		if err != nil {
			fail(err)
		}
		fmt.Printf("claudesync is set up for %q.\n", a.Config().Host)
		report(res, false)
		fmt.Println("\nNext: `claudesync install-timer` to sync in the background, `claudesync doctor` to check the setup.")

	case "sync":
		quiet := fs.Bool("quiet", false, "print only problems")
		offline := fs.Bool("offline", false, "do not fetch or push")
		timeout := fs.Duration("timeout", 60*time.Second, "network timeout")
		delay := fs.Duration("delay", 0, "wait before syncing (debounce)")
		lockWait := fs.Duration("lock-wait", 30*time.Second, "how long to wait for another running sync")
		background := fs.Bool("background", false, "run detached and return immediately")
		fs.Parse(args)
		if *background {
			if err := app.SpawnBackground(cfg); err != nil {
				fail(err)
			}
			return
		}
		if *delay > 0 {
			time.Sleep(*delay)
		}
		a := newApp(cfg)
		res, err := a.Sync(ctx, app.SyncOptions{Offline: *offline, NetTimeout: *timeout, LockWait: *lockWait})
		if err != nil {
			if *quiet && app.ErrIsLocked(err) {
				return
			}
			fail(err)
		}
		report(res, *quiet)

	case "status", "diff":
		fetch := fs.Bool("fetch", false, "contact the remote to count incoming changes")
		fs.Parse(args)
		s, err := newApp(cfg).Status(ctx, *fetch)
		if err != nil {
			fail(err)
		}
		printStatus(s)

	case "pin", "unpin":
		onlyHere := fs.Bool("only-here", false, "pin: also remove the key from the shared settings, so other devices drop it")
		fs.Parse(args)
		if fs.NArg() != 1 {
			fail(fmt.Errorf("usage: claudesync %s <settings.key> (for example: model or permissions.defaultMode)", cmd))
		}
		res, err := newApp(cfg).Pin(ctx, fs.Arg(0), cmd == "unpin", *onlyHere, app.SyncOptions{LockWait: 30 * time.Second})
		if err != nil {
			fail(err)
		}
		if cmd == "pin" {
			fmt.Printf("%s is now specific to %s.\n", fs.Arg(0), cfg.Host)
		} else {
			fmt.Printf("%s uses the shared value again.\n", fs.Arg(0))
		}
		report(res, false)

	case "profile", "profiles":
		sub := "list"
		if len(args) > 0 {
			sub, args = args[0], args[1:]
		}
		switch sub {
		case "list":
			a := newApp(cfg)
			ps, unassigned, err := a.ProfileList()
			if err != nil {
				fail(err)
			}
			fmt.Printf("this device: %s\n", a.Config().Host)
			if len(ps) == 0 {
				fmt.Println("no profiles yet — create one with `claudesync profile use <name>`")
			}
			for _, p := range ps {
				devs := strings.Join(p.Devices, ", ")
				if devs == "" {
					devs = "no devices"
				}
				fmt.Printf("  %-16s %3d files, %2d settings keys  — %s\n", p.Name, p.Files, p.Settings, devs)
			}
			if len(unassigned) > 0 {
				fmt.Println("devices without a profile:", strings.Join(unassigned, ", "))
			}
		case "use":
			pfs := flag.NewFlagSet("profile use", flag.ExitOnError)
			none := pfs.Bool("none", false, "leave all profiles")
			pfs.Parse(args)
			if !*none && pfs.NArg() == 0 {
				fail(errors.New("usage: claudesync profile use <name[,name2]> | --none"))
			}
			res, err := newApp(cfg).UseProfiles(ctx, pfs.Args(), app.SyncOptions{LockWait: 30 * time.Second})
			if err != nil {
				fail(err)
			}
			if *none {
				fmt.Println("this device is in no profile now")
			} else {
				fmt.Printf("profiles of %s: %s\n", cfg.Host, strings.Join(pfs.Args(), ","))
			}
			report(res, false)
		default:
			fail(fmt.Errorf("unknown profile command %q (list, use)", sub))
		}

	case "move", "promote":
		to := fs.String("to", "", "target: a profile name or shared")
		var target string
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			target, args = args[0], args[1:]
		}
		fs.Parse(args)
		if target == "" && fs.NArg() > 0 {
			target = fs.Arg(0)
		}
		if cmd == "promote" {
			*to = "shared"
		}
		if target == "" || *to == "" {
			fail(errors.New("usage: claudesync move <path|settings.key> --to <profile|shared>"))
		}
		res, err := newApp(cfg).Move(ctx, target, *to, app.SyncOptions{LockWait: 30 * time.Second})
		if err != nil {
			fail(err)
		}
		fmt.Printf("moved %s to %s\n", target, *to)
		report(res, false)

	case "resolve":
		prefer := fs.String("prefer", "", "local or repo")
		fs.Parse(args)
		res, err := newApp(cfg).Resolve(ctx, *prefer, app.SyncOptions{LockWait: 30 * time.Second})
		if err != nil {
			fail(err)
		}
		report(res, false)

	case "backups":
		for _, id := range newApp(cfg).Backups() {
			fmt.Println(id)
		}

	case "restore":
		fs.Parse(args)
		if fs.NArg() != 1 {
			fail(errors.New("usage: claudesync restore <backup-id> (see `claudesync backups`)"))
		}
		files, err := newApp(cfg).Restore(fs.Arg(0))
		if err != nil {
			fail(err)
		}
		fmt.Printf("restored %d file(s): %s\nThe next sync pushes them to your other devices.\n", len(files), strings.Join(files, ", "))

	case "install-timer":
		interval := fs.Duration("interval", 5*time.Minute, "how often to sync")
		fs.Parse(args)
		detail, err := app.InstallTimer(cfg, *interval)
		if err != nil {
			fail(err)
		}
		fmt.Println("installed:", detail)

	case "uninstall-timer":
		if err := app.UninstallTimer(cfg); err != nil {
			fail(err)
		}
		fmt.Println("timer removed")

	case "doctor":
		ok := true
		for _, c := range newApp(cfg).Doctor(ctx) {
			mark := "✓"
			if !c.OK {
				mark, ok = "✗", false
			}
			fmt.Printf("%s %-16s %s\n", mark, c.Name, c.Detail)
		}
		if !ok {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func newApp(cfg *app.Config) *app.App {
	a, err := app.New(cfg)
	if err != nil {
		fail(err)
	}
	return a
}

func report(res *app.Result, quiet bool) {
	if !quiet || res.Changed() {
		fmt.Println(res.Summary())
	}
	for _, n := range res.Notices() {
		fmt.Println("!", n)
	}
	for _, s := range res.Secrets {
		fmt.Println("! possible secret:", s)
	}
	if res.NetError != "" && !quiet {
		fmt.Println("! network:", res.NetError)
	}
}

func printStatus(s *app.Status) {
	fmt.Printf("host:      %s\nrepo:      %s\nremote:    %s\n", s.Host, s.Repo, s.Remote)
	if len(s.Profiles) > 0 {
		fmt.Printf("profiles:  %s (new files and keys go into %s)\n", strings.Join(s.Profiles, ", "), s.Profiles[0])
	}
	if !s.LastSync.IsZero() {
		fmt.Printf("last sync: %s (%s ago): %s\n", s.LastSync.Format(time.RFC1123), time.Since(s.LastSync).Round(time.Second), s.LastResult)
	}
	if s.LastError != "" {
		fmt.Println("error:    ", s.LastError)
	}
	if s.Conflict != "" {
		fmt.Println("CONFLICT: ", s.Conflict, "— run `claudesync resolve --prefer local|repo`")
	}
	if len(s.Blocked) > 0 {
		fmt.Println("blocked (possible secrets):", strings.Join(s.Blocked, ", "))
	}
	if s.FetchError != "" {
		fmt.Println("fetch:    ", s.FetchError)
	}
	fmt.Printf("commits:   %d to push, %d to pull\n", s.Ahead, s.Behind)
	if len(s.LocalChanges)+len(s.SettingsOps) == 0 {
		fmt.Println("local:     no changes since last sync")
		return
	}
	fmt.Println("local changes:")
	for _, c := range s.LocalChanges {
		fmt.Println("  " + c)
	}
	for _, op := range s.SettingsOps {
		fmt.Println("  settings " + op)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "claudesync:", err)
	os.Exit(1)
}

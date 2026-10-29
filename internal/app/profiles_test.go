package app

import (
	"context"
	"strings"
	"testing"
)

func TestProfiles(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	pc := newDevice(t, "pc", "windows")
	server := newDevice(t, "server", "linux")
	mac := newDevice(t, "macbook", "darwin")

	// The PC starts without a profile: everything is shared.
	pc.write("CLAUDE.md", "# everywhere\n")
	pc.write("settings.json", `{"model": "opus"}`)
	pc.init(remote, "repo")

	// Joining "work": new things go into the profile, existing ones stay shared.
	if _, err := pc.app().UseProfiles(ctx, []string{"work"}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	pc.write("skills/deploy/SKILL.md", "work skill\n")
	pc.write("CLAUDE.md", "# everywhere, edited\n")
	setSetting(t, pc, "theme", "work-theme")
	setSetting(t, pc, "model", "sonnet")
	pc.sync()
	want(t, "work skill in profile", pc.repoFile("profiles/work/skills/deploy/SKILL.md"), "work skill\n")
	want(t, "work skill not shared", pc.repoFile("shared/skills/deploy/SKILL.md"), "<missing>")
	want(t, "CLAUDE.md stays shared", pc.repoFile("shared/CLAUDE.md"), "# everywhere, edited\n")
	if !strings.Contains(pc.repoFile("profiles/work/settings.json"), "work-theme") {
		t.Fatalf("theme not in profile: %s", pc.repoFile("profiles/work/settings.json"))
	}
	if !strings.Contains(pc.repoFile("shared/settings.json"), "sonnet") {
		t.Fatal("existing shared key moved out of shared")
	}

	server.init(remote, "repo", "work")
	mac.init(remote, "repo", "personal")
	want(t, "server work skill", server.read("skills/deploy/SKILL.md"), "work skill\n")
	want(t, "server theme", server.get("theme"), "work-theme")
	want(t, "mac no work skill", mac.read("skills/deploy/SKILL.md"), "<missing>")
	want(t, "mac no work theme", mac.get("theme"), nil)
	want(t, "mac shared CLAUDE.md", mac.read("CLAUDE.md"), "# everywhere, edited\n")
	want(t, "mac shared model", mac.get("model"), "sonnet")

	// The MacBook's additions stay personal.
	mac.write("skills/fun/SKILL.md", "fun\n")
	setSetting(t, mac, "theme", "light")
	mac.sync()
	pc.sync()
	server.sync()
	want(t, "pc no personal skill", pc.read("skills/fun/SKILL.md"), "<missing>")
	want(t, "pc keeps work theme", pc.get("theme"), "work-theme")
	want(t, "mac personal theme", mac.get("theme"), "light")

	// Promoting makes a work skill and a key available everywhere.
	if _, err := server.app().Move(ctx, "skills/deploy", "shared", SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.app().Move(ctx, "permissions", "shared", SyncOptions{}); err == nil {
		t.Fatal("moving a missing key should fail")
	}
	mac.sync()
	want(t, "mac promoted skill", mac.read("skills/deploy/SKILL.md"), "work skill\n")
	want(t, "promoted skill left profile", server.repoFile("profiles/work/skills/deploy/SKILL.md"), "<missing>")

	// Switching profiles swaps the profile content.
	pc.sync()
	pc.write("skills/work-only/SKILL.md", "only at work\n")
	pc.sync()
	if _, err := pc.app().UseProfiles(ctx, []string{"personal"}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	want(t, "pc lost work-only skill", pc.read("skills/work-only/SKILL.md"), "<missing>")
	want(t, "pc got personal skill", pc.read("skills/fun/SKILL.md"), "fun\n")
	want(t, "pc personal theme", pc.get("theme"), "light")
	want(t, "pc kept promoted skill", pc.read("skills/deploy/SKILL.md"), "work skill\n")
	server.sync()
	want(t, "server keeps work-only skill", server.read("skills/work-only/SKILL.md"), "only at work\n")

	// Two profiles: the first wins and receives new things.
	if _, err := server.app().UseProfiles(ctx, []string{"work,personal"}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	want(t, "server first profile wins", server.get("theme"), "work-theme")
	want(t, "server gets both", server.read("skills/fun/SKILL.md"), "fun\n")

	ps, unassigned, err := server.app().ProfileList()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range ps {
		got[p.Name] = strings.Join(p.Devices, ",")
	}
	want(t, "work devices", got["work"], "server")
	want(t, "personal devices", got["personal"], "macbook,pc,server")
	want(t, "unassigned", len(unassigned), 0)
}

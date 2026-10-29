# claudesync

Keep your personal Claude Code configuration in sync across all your devices —
Windows, macOS, Linux/WSL and remote servers — through a private git repository.

Claude Code already syncs skills and plugins you enable **on claude.ai**. claudesync
covers everything else: your global `CLAUDE.md`, local skills, agents, slash
commands, rules, output styles, scripts, keybindings and `settings.json`, including
the plugins and marketplaces you installed.

- **Automatic and bidirectional.** A Claude Code plugin pulls when a session starts
  and pushes when settings change; a timer syncs in the background.
- **Settings are merged, not copied.** `settings.json` is split into a shared layer,
  profile layers, a per-OS layer and a per-device layer, merged key by key.
- **Profiles** group devices by context — say `work` for your PC and server and
  `personal` for your MacBook — each with its own skills, instructions and settings
  on top of what all devices share.
- **Safe by default.** Credentials, sessions, history and `~/.claude.json` never sync;
  `env`, `apiKeyHelper` and similar keys stay on each device; a secret scan holds back
  anything that looks like a token; every overwritten file is backed up first.
- **One binary**, no runtime. Needs only `git`.

## Install

```sh
# macOS / Linux / WSL
curl -fsSL https://raw.githubusercontent.com/Hudint/claudesync/main/scripts/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/Hudint/claudesync/main/scripts/install.ps1 | iex
```

Or with Go: `go install github.com/Hudint/claudesync/cmd/claudesync@latest`.

## Set up

On the **first** device, create the private config repository (needs the GitHub CLI `gh`)
and import the current configuration:

```sh
claudesync init --repo you/claude-config --create
claudesync install-timer
```

On **every other** device:

```sh
claudesync init --repo you/claude-config            # the repository wins where both differ
claudesync init --repo you/claude-config --prefer local   # or: this device wins
claudesync install-timer
```

Either way, things that exist on only one side are kept, and files that get replaced
are backed up to `~/.claudesync/backups/`.

`init` also enables the `claudesync` Claude Code plugin (skip with `--no-plugin`), which
then syncs to your other devices like any other plugin.

## Profiles

A profile is a group of devices that share a context. Every device sees, from lowest
to highest priority:

```
shared  ←  its profiles (first listed wins)  ←  os/<os>.json  ←  hosts/<host>.json
```

This applies to files (`CLAUDE.md`, skills, agents, …: the highest layer that has a
file provides it) and to `settings.json` (merged key by key).

```sh
claudesync init --repo you/claude-config --profile work   # or later:
claudesync profile use work            # this device joins "work"
claudesync profile use work,linux      # several profiles; the first wins
claudesync profile use --none          # back to shared only
claudesync profile list                # profiles and their devices
```

**Where changes go:** a file or settings key that already exists stays in the layer it
comes from (edit a shared `CLAUDE.md` and every device gets the edit). Anything **new** —
a new skill, a new settings key, a new permission rule — goes into the device's first
profile, so it stays in that context. Deleting removes it from all of this device's layers.

```sh
claudesync promote skills/deploy                  # make it available to every device
claudesync move CLAUDE.md --to personal           # only personal devices get it
claudesync move permissions.defaultMode --to work # settings keys work too
```

When a device switches profiles, files that only belonged to the old profile are removed
locally (backed up first) and the new profile is applied. With no profile, everything goes
into `shared`, exactly as before.

## What syncs

| Local (`~/.claude/`) | Repository |
|---|---|
| `CLAUDE.md`, `keybindings.json` | `shared/` |
| `skills/` (not `skills/synced/`), `agents/`, `commands/`, `rules/`, `output-styles/`, `scripts/` | `shared/` |
| `settings.json` | `shared/settings.json` + `profiles/<name>/settings.json` + `os/<os>.json` + `hosts/<host>.json` |

Profile files live in `profiles/<name>/`, laid out like `shared/`.

**Never synced:** `.credentials.json`, `~/.claude.json` (MCP servers, account state),
`settings.local.json`, `projects/`, `history.jsonl`, `sessions/`, plugin caches, and the
settings keys listed in `localOnlyKeys`.

**Plugins** sync through `enabledPlugins` and `extraKnownMarketplaces` in `settings.json`;
Claude Code downloads them on each device. After a sync changes plugins, run
`/reload-plugins` or start a new session.

**MCP servers** are not synced yet (they often contain secrets). Use claude.ai connectors
for remote MCP servers in the meantime — those are available on every device already.

## How settings merge

A device's `settings.json` is `shared` ← profiles ← `os/<os>.json` ← `hosts/<host>.json`: objects
merge by key, arrays such as `permissions.allow` are combined, and other values are
replaced by the more specific layer.

When you change `settings.json` (by hand, with `/config`, `/model`, a permission
approval …), only the changed keys go back to the repository — into the layer that already
defines them, otherwise into the device's first profile (or `shared`). So a value you
pinned stays device-specific:

```sh
claudesync pin model                        # this device keeps its own model
claudesync pin --only-here skipDangerousModePermissionPrompt   # and other devices drop the key
claudesync unpin model                      # use the shared value again
```

OS-specific values (for example a hook command that is different on Windows) go into
`os/windows.json`, `os/darwin.json` or `os/linux.json` in the repository.

Absolute paths under your home directory are stored as `{{home}}` and `{{claude_dir}}`, so
`~/.claude/scripts/statusline.sh` works on every device.

## Conflicts

Changes to different files or different settings keys merge automatically. If two devices
change the same value or the same lines, the device that syncs later wins. If git still
can't merge (for example, one device edits a file another deleted), syncing stops for that
device and tells you:

```sh
claudesync status
claudesync resolve --prefer local   # keep this device's version
claudesync resolve --prefer repo    # take the other devices' version
```

## Commands

```
claudesync sync [--quiet] [--offline]   sync now
claudesync status [--fetch]             pending changes, last sync, conflicts
claudesync pin|unpin <key>              device-specific settings values
claudesync profile list|use             profiles (device groups)
claudesync move <path|key> --to <p>     move into a profile or shared; promote = --to shared
claudesync resolve --prefer local|repo  recover from a conflict
claudesync backups / restore <id>       backups taken before overwriting files
claudesync install-timer [--interval 5m] / uninstall-timer
claudesync doctor                       check the setup
```

In Claude Code: `/claudesync:sync` and `/claudesync:sync-status`.

## Configuration

`claudesync.json` at the root of the config repository:

```json
{
  "version": 1,
  "include": ["hooks/", "statusline.sh"],
  "exclude": ["skills/huge-skill/**"],
  "localOnlyKeys": ["env", "apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper"],
  "secretAllow": ["example-token-[0-9]+"]
}
```

- `include` adds paths relative to `~/.claude` (a trailing `/` marks a directory).
- `localOnlyKeys` replaces the default list of top-level settings keys that never leave a device.
- A line containing `claudesync:allow` is never reported by the secret scan.

Environment variables: `CLAUDE_CONFIG_DIR` (Claude directory), `CLAUDESYNC_HOME`
(default `~/.claudesync`), `CLAUDESYNC_HOST` (device name, default: hostname).

## License

MIT

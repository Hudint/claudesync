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
  a per-OS layer and a per-device layer, merged key by key.
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

## What syncs

| Local (`~/.claude/`) | Repository |
|---|---|
| `CLAUDE.md`, `keybindings.json` | `shared/` |
| `skills/` (not `skills/synced/`), `agents/`, `commands/`, `rules/`, `output-styles/`, `scripts/` | `shared/` |
| `settings.json` | `shared/settings.json` + `os/<os>.json` + `hosts/<host>.json` |

**Never synced:** `.credentials.json`, `~/.claude.json` (MCP servers, account state),
`settings.local.json`, `projects/`, `history.jsonl`, `sessions/`, plugin caches, and the
settings keys listed in `localOnlyKeys`.

**Plugins** sync through `enabledPlugins` and `extraKnownMarketplaces` in `settings.json`;
Claude Code downloads them on each device. After a sync changes plugins, run
`/reload-plugins` or start a new session.

**MCP servers** are not synced yet (they often contain secrets). Use claude.ai connectors
for remote MCP servers in the meantime — those are available on every device already.

## How settings merge

A device's `settings.json` is `shared` ← `os/<os>.json` ← `hosts/<host>.json`: objects
merge by key, arrays such as `permissions.allow` are combined, and other values are
replaced by the more specific layer.

When you change `settings.json` (by hand, with `/config`, `/model`, a permission
approval …), only the changed keys go back to the repository — into the layer that already
defines them, otherwise into `shared`. So a value you pinned stays device-specific:

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

# claudesync – Plan

Ziel: Persönliche Claude-Code-Konfiguration (Instructions, Skills, Settings, Plugins …)
automatisch und bidirektional zwischen allen Geräten synchron halten –
Windows, macOS, Linux/WSL und dieser Server.

## 1. Ausgangslage

**Was Claude Code schon selbst synct** (bei Login mit claude.ai-Account, nur claude.ai → Gerät):

| Was | Wohin | Bemerkung |
|---|---|---|
| Auf claude.ai aktivierte Skills | `~/.claude/skills/synced/` | nicht hochladbar aus Claude Code |
| Auf claude.ai aktivierte Plugins | `~/.claude/plugins/synced/` | `<name>@synced` |
| claude.ai-Connectors (MCP) | – | remote, kein lokaler Config-Eintrag |

→ claudesync **fasst diese Ordner nicht an** und ergänzt alles andere.

**Bestehende Tools** (claude-nomad, andrewle8/claude-code-sync, dotai, clsync): keins
kombiniert automatischen bidirektionalen Sync, `settings.json`-Merge mit Overrides pro
Gerät/OS, Plugin-Sync und Windows-Support. claude-nomad ist konzeptionell am nächsten
(Git, Allow-List, Basis+Host-Overrides, Secret-Scan) und dient als Inspiration.

## 2. Entscheidungen

- **Transport:** privates GitHub-Repo (z. B. `Hudint/claude-config`) als Quelle der Wahrheit
- **Sprache:** Go → eine Binary pro Plattform (`claudesync`), Git wird per `git`-CLI genutzt
- **Richtung:** bidirektional, automatisch
- **Integration:** CLI + Claude-Code-Plugin (Hooks + `/sync`), auf dem Server zusätzlich Timer
- **MCP-Server:** vorerst **nicht** (Phase 2, siehe unten)

## 3. Was wird gesynct (Allow-List)

| Lokal (`~/.claude/…`) | Im Repo | Art |
|---|---|---|
| `CLAUDE.md` | `shared/CLAUDE.md` | Datei |
| `rules/`, `agents/`, `commands/`, `output-styles/` | `shared/<dir>/` | Verzeichnis |
| `skills/` (ohne `synced/`, `.trash/`) | `shared/skills/` | Verzeichnis |
| `settings.json` | `shared/settings.json` + `os/<os>.json` + `hosts/<host>.json` | **JSON-Merge** |
| `keybindings.json` | `shared/keybindings.json` | Datei |
| `scripts/` (eigene Hook-/Statusline-Skripte) | `shared/scripts/` | Verzeichnis |

**Plugins** laufen über `settings.json`: `enabledPlugins` + `extraKnownMarketplaces`.
Claude Code holt deklarierte Marketplaces und aktivierte Plugins beim Session-Start selbst
nach. Cache, `installed_plugins.json` usw. werden also nicht gesynct.

**Niemals gesynct:** `.credentials.json`, `~/.claude.json`, `settings.local.json`,
`projects/`, `history.jsonl`, `file-history/`, `sessions/`, `plugins/{cache,marketplaces,synced,data}`,
`skills/synced/`, `backups/`, `todos/`, `shell-snapshots/` …

Erweiterbar über `claudesync.json` im Repo (`include`/`exclude`-Globs).

## 4. Repo-Layout

```
claude-config/                 (privat)
├── claudesync.json            # Version, include/exclude, localOnlyKeys, secretAllow
├── shared/
│   ├── CLAUDE.md
│   ├── settings.json          # gemeinsame Basis
│   ├── skills/ agents/ commands/ rules/ output-styles/ scripts/
│   └── keybindings.json
├── os/
│   ├── windows.json           # OS-spezifische settings-Overrides (z. B. Hook-Befehle)
│   ├── darwin.json
│   └── linux.json
└── hosts/
    └── <hostname>.json        # gerätespezifische Overrides
```

Lokaler Zustand pro Gerät in `~/.claudesync/`:
`repo/` (Clone), `state.json` (Host-Name, Hashes vom letzten Sync), `backups/<timestamp>/`, `lock`, `log`.

## 5. settings.json-Merge

**Apply (Repo → Gerät):**
`effektiv = deepMerge(shared, os/<os>, hosts/<host>)`, danach werden Templates aufgelöst
(`{{home}}`, `{{claude_dir}}`, `{{os}}`). Listen wie `permissions.allow` werden
vereinigt (dedupliziert), Objekte rekursiv gemergt, Skalare überschrieben.

**Capture (Gerät → Repo):** 3-Wege-Vergleich `lokal` vs. `zuletzt angewendet`:
- Nur **geänderte Keys** wandern zurück, nicht die ganze Datei.
- Ein geänderter Key landet in `hosts/<host>.json`, wenn er dort schon existiert oder
  als host-lokal markiert ist, sonst in `shared/settings.json`.
- **Nie gesynct** (bleiben auf dem Gerät, `localOnlyKeys`): `env`, `apiKeyHelper`,
  `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`, `forceLogin*`.
- Gerätespezifisch machen: `claudesync pin <key>` (Wert nach `hosts/<host>.json`),
  `pin --only-here <key>` entfernt ihn zusätzlich aus `shared`, sodass andere Geräte ihn verlieren.
- Absolute Home-Pfade werden zurück zu `{{home}}` templatisiert.

## 6. Sync-Ablauf (`claudesync sync`)

1. Lock holen (verhindert parallele Läufe von Hook + Timer).
2. **Capture:** Allow-List-Dateien lokal hashen und mit `state.json` vergleichen;
   Änderungen und Löschungen ins Repo-Working-Tree übernehmen, Settings wie in §5.
3. **Secret-Scan** über den Diff: eingebaute Regeln (`sk-ant-`, `ghp_`, `AKIA…`,
   private Keys, generische Token mit hoher Entropie), optional `gitleaks`, wenn installiert.
   Bei einem Treffer: Abbruch und Meldung, kein Commit.
4. Commit (`sync(<host>): …`), dann `git pull --rebase`, dann `git push`.
   - Bei Git-Konflikt: Rebase abbrechen, lokale Dateien bleiben unangetastet, und
     `claudesync status` bzw. die Hook-Meldung zeigt den Konflikt an. Aufgelöst wird per
     `claudesync resolve --ours|--theirs` oder von Hand.
5. **Apply:** Repo-Stand nach `~/.claude` schreiben. Vorher kommt jede Datei, die
   überschrieben oder gelöscht wird, nach `~/.claudesync/backups/`. Hashes landen in `state.json`.
6. Kurze Zusammenfassung ausgeben (für Hooks als `systemMessage`).

Offline oder ohne Netz: Capture und Commit laufen lokal, gepusht wird beim nächsten Lauf.

## 7. Trigger / Integration

**Claude-Code-Plugin `claudesync`** (der Tool-Repo ist gleichzeitig ein Marketplace):
- `SessionStart` → `claudesync sync --quiet --timeout 15s` (zieht neue Config vor der Arbeit)
- `ConfigChange` und `SessionEnd` → startet `claudesync sync` losgelöst im Hintergrund
  (3 s Verzögerung zum Bündeln, blockiert die Session nie)
- `/sync`, `/sync-status` als Slash-Commands
- Fehlt die Binary, meldet der Hook das freundlich mit Installationsanleitung.

Weil das Plugin in `shared/settings.json` steht, wird es auf jedem neuen Gerät
automatisch mit aktiviert.

**Zusätzlich** (`claudesync install-timer`): systemd-User-Timer (Linux, alle 5 min),
launchd (macOS), Aufgabenplanung (Windows). Damit kommen Änderungen auch an, wenn
gerade keine Session läuft. Auf dem Server ist das der Hauptmechanismus.

Hinweis: Änderungen an Skills, CLAUDE.md und settings.json lädt Claude Code im
Hintergrund neu. Für neue Plugins braucht es `/reload-plugins` oder eine neue Session.

## 8. CLI

```
claudesync init [--repo owner/name] [--create]   # Repo anlegen/klonen, ersten Stand importieren
claudesync sync | pull | push
claudesync status | diff
claudesync pin <settings-key> / unpin <key>      # Key gerätespezifisch machen
claudesync resolve --ours|--theirs
claudesync restore [<backup-id>]
claudesync install-timer | uninstall-timer
claudesync doctor                                 # git, Auth, Pfade, Plugin, Timer prüfen
```

`init` auf einem Gerät mit bestehender Config: Ist das Repo leer, wird importiert.
Sonst zeigt `init` einen Diff und fragt, ob gemergt oder der Repo-Stand übernommen wird.

## 9. Cross-Platform

- Nur Kopieren, keine Symlinks (Windows).
- `.gitattributes` mit `* text=auto eol=lf`, Skripte (`*.ps1`, `*.cmd`) mit eigenen Regeln.
- Pfade werden templatisiert (§5). OS-spezifische Hook-Befehle kommen in `os/<os>.json`.
- Host-Name: Default ist der Hostname, überschreibbar per `CLAUDESYNC_HOST`.
- Respektiert `CLAUDE_CONFIG_DIR`.
- CI-Matrix mit Tests auf ubuntu, macos und windows.

## 10. Meilensteine

Stand 2026-10-29: M1–M3 umgesetzt und getestet (`go test ./...`), M4 vorbereitet
(goreleaser, CI-Matrix, Install-Skripte) – fehlt nur das erste Release-Tag.

1. **M1 – Kern:** Go-Projekt, `init`, Capture/Apply für Dateien und Verzeichnisse,
   Git-Sync, `state.json`, Lock, Backups, `status`/`diff`. Tests mit temporären Repos.
2. **M2 – Settings:** JSON-Merge (shared/os/host), 3-Wege-Capture, Templates, `pin`, Secret-Scan.
3. **M3 – Integration:** Claude-Code-Plugin (Hooks, `/sync`), `install-timer` für alle 3 OS, `doctor`.
4. **M4 – Release:** goreleaser + GitHub Releases, Install-Skript (sh/ps1), README.
5. **Phase 2 (später):** MCP-Server aus `~/.claude.json` (`mcpServers`) mit
   `${VAR}`-Platzhaltern und verschlüsselten Secrets (age), optional Auto-Memory.

## 11. Entschieden

- Config-Repo: `Hudint/claude-config` (privat), Tool-Repo: `Hudint/claudesync` (öffentlich)

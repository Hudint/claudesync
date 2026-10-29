---
description: Sync the Claude Code configuration with your other devices now
allowed-tools: Bash(claudesync sync:*), Bash(claudesync status:*)
---
Run `claudesync sync` with the Bash tool and report the result in one or two sentences.

- If it reports a conflict, explain `claudesync resolve --prefer local` (keep this device's version) and `claudesync resolve --prefer repo` (take the other devices' version), and ask which one to run.
- If it reports possible secrets, show the affected files and suggest moving the value into an environment variable (`env` in settings.json stays on this device) or marking the line with `claudesync:allow` if it is a false positive.
- If plugins changed, remind the user to run `/reload-plugins`.
- If `claudesync` is not found, point to https://github.com/Hudint/claudesync#install.

---
description: Show what claudesync would sync and when it last ran
allowed-tools: Bash(claudesync status:*), Bash(claudesync doctor:*)
---
Run `claudesync status --fetch` with the Bash tool and summarize: last sync, pending local changes, incoming commits, conflicts or blocked files. If anything looks broken, also run `claudesync doctor` and explain the failing checks.

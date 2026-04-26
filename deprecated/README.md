# Deprecated code — reference only

This directory holds code that has been removed from active use but retained for reference. **Nothing in here is built, tested, deployed, or imported by production code.** It exists only so future implementers can read the old patterns when investigating edge cases.

## What's here

| Path | Replaced by | When | Why |
|---|---|---|---|
| `cmd-discord-bot/` | `cmd/edin-bot/` | 2026-04-26 | Single-guild design; replaced by multi-server, multi-channel, factory-style framework |
| `internal-discord/` | `internal/edinbot/` | 2026-04-26 | Same reason; package-level rewrite to fit the new framework |
| `ansible-discord_bot/` | `atlas/ansible/roles/edin_bot/` | 2026-04-26 | Ansible role moved to atlas (the EDIN control plane) |

## Rules

1. **Do not import from this directory.** Any production code that does so is a bug — the build should not allow it.
2. **Do not extend or "fix" code in here.** If you need a behaviour from the old bot, reimplement it inside the new framework as a `Feature`.
3. **Do not run anything from here.** The old binary is not built, the old ansible role is not included in `site.yml`.

If a file in here turns out to be of zero reference value, deleting it from this directory is fine — but discuss with the orchestrator before doing so in bulk.

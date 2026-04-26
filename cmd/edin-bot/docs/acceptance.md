# EDIN Bot — Acceptance Verification

Walk-through of every spec §11 acceptance criterion. Owner of this doc: the implementing team at deploy time. Update if the implementation changes.

## Criteria

| # | Criterion | Verification |
|---|---|---|
| 1 | `cmd/discord-bot` and `internal/discord/` removed; replaced by `deprecated/` references; lost slash commands explicitly out-of-scope | Phase 0.2 commit. Verify: `ls edin-backend/cmd/discord-bot 2>/dev/null` returns nothing; `ls edin-backend/deprecated/cmd-discord-bot/main.go` exists. Spec §10 'Functional regressions accepted' lists the lost commands. |
| 2 | Bot posts a platinum + LTD message in the Kaine alerts channel within one poll cycle of deploy against real production data | Operator runs `make deploy-edin-bot` and observes channel `1487248197582852321` within 15 minutes. Operator sign-off only — cannot be auto-verified. |
| 3 | `ops-health-alerts` posts an outage alert into `#edin-ops` when control-API is stopped, and edits the same message to RESOLVED on recovery | Operator: `ssh -p 2222 debian@51.178.89.95 'sudo docker stop control-api'`, observe alert in channel `1497743648488554607`, restart with `sudo docker start control-api`, observe RESOLVED edit. |
| 4 | Strike-through verified end-to-end | `internal/edinbot/integration_test.go::TestEdinBot_FullLifecycle_PostEditNoopStrikeUnstrike` covers strike + unstrike with persisted state assertion. E2E covered by `TestE2E_PostEditStrikeUnstrikeAgainstRealDiscord` (gated EDIN_E2E=1). |
| 5 | `make test-edin-bot-all` and `make lint-edin-bot` pass on every implementation plan task | Operator: `cd edin-backend && make test-edin-bot-all && make lint-edin-bot`. |
| 6 | `make deploy-edin-bot` succeeds from clean checkout | Operator runs from clean checkout. |
| 7 | `cmd/edin-bot/docs/discord-app-setup.md` exists and is complete | `cat edin-backend/cmd/edin-bot/docs/discord-app-setup.md` shows Developer Portal config, Application ID, OAuth URLs, guild/channel IDs, vault key names. |
| 8 | Restart produces zero spurious posts/edits | `internal/edinbot/integration_test.go::TestEdinBot_RestartSafety_NoDoublePosts`. |
| 9 | New feature added by implementing one Go interface + one binding YAML row | Lock test: `internal/edinbot/features/registry_test.go::TestRegistry_*`. The PollFeature/EventDrivenFeature interfaces are stable; bindings.Load validates schema. Stress test by adding a fake feature to Registry in a test (see Phase 4 integration test pattern). |
| 10 | Bot identity is `bot:edin` group; control-API audit log shows m2m calls correctly attributed | Phase 4 commits. Verify: `journalctl -u control-api.service \| grep 'm2m call:'` shows lines with `sub=svc-edin-bot`. |
| 11 | `/admin/diagnose` rejects non-allowlist values with 400; no shell-out paths; control-API has no docker socket mount | `internal/httpapi/admin_diagnose_test.go::TestDiagnoseHandler_RejectsNonAllowlistedChecks` and `TestDiagnoseHandler_NoShellOut` and `cmd/docker-inspect-sidecar/main_test.go::TestSidecar_NoShellOut`. Production: `docker inspect control-api --format '{{.Mounts}}'` shows NO `/var/run/docker.sock`. |
| 12 | Sidecar exposes only `GET /inspect/{container}`, returns 404 for non-allowlist, no host port | `cmd/docker-inspect-sidecar/main_test.go` covers all four guards. Production: `docker port docker-inspect` returns nothing. |
| 13 | Channel-deleted simulation marks `disabled_at`, stops Discord calls, emits OpsEvent | `internal/edinbot/integration_test.go::TestEdinBot_ChannelDeleted_DisablesBinding` + `discord.disabled_bindings` tombstone table covers the cold-start case. |
| 14 | Discord rate-limit handling: flood of 50 simulated posts to one channel processed without 429 | `internal/edinbot/discordclient/ratelimit_test.go::TestPerChannelLimiter_*`. |
| 15 | `Validate(Config)` rejects unknown keys per feature | `internal/edinbot/features/{platinum,ltd,ops_health}_test.go` — each feature's `Validate()` is covered by a negative test. |

## Sign-off

After every checklist row above is verified by the operator (and integration tests pass + e2e test runs cleanly), this section gets signed.

- [ ] All criteria verified above
- [ ] Operator (David) sign-off: ___________  date: __________
- [ ] First production deploy observed in #edin-ops: ___________

## Open issues (tracked in commits, not blocking deploy)

None at the time of writing. The previous open items (`/admin/diagnose` route wire-up, `Store.LatestSuccessAt`, `disabled_bindings` cold-start fix, Phase 15 tests + this doc) have all been resolved.

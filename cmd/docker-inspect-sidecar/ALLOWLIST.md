# Sidecar / diagnose allowlist (lock-step)

The list of containers that `docker-inspect-sidecar` will inspect is duplicated
in TWO places that **MUST stay in lock-step**:

1. `cmd/docker-inspect-sidecar/main.go` — `allowedContainers()`
2. `internal/httpapi/admin_diagnose.go` — `allowedDiagnoseChecks` (Phase 6)

Adding a container to one without the other will silently break the
`/admin/diagnose` endpoint (the control-API will reject the check name with
HTTP 400, OR the sidecar will return 404 even though diagnose tried).

## Adding a new container

1. Add it to BOTH lists.
2. Update the integration test in `cmd/docker-inspect-sidecar/main_test.go`
   (`TestSidecar_AllowlistContents`) AND the admin_diagnose test asserting the
   allowlist contents.
3. Update spec §5 (the "Mandatory implementation requirements" subsection's
   allowedChecks map example).
4. Add the container to the docker-compose group of the production deploy if
   the sidecar's docker socket cannot already see it (network-namespace caveat).

Failure mode if you only update one side: tests in the changed package pass,
the other package's tests fail loudly (the integration test in part 6 cross-
checks the two lists agree). Do not silence the failing test — fix the missing
list.

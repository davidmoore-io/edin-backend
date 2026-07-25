# Sidecar / diagnose allowlist

The sidecar owns the set of containers it will inspect:

1. `cmd/docker-inspect-sidecar/main.go` — `allowedContainers()`.
2. `internal/httpapi/admin_diagnose.go` maps public check names to containers.

The required invariant is one-way: every `container` value in
`allowedDiagnoseChecks` must be present in `allowedContainers()`. Check names
can differ from container names; `galaxy-reader`, for example, inspects
`eddn-timescaledb`.

## Adding a new container

1. Add the container to `allowedContainers()`.
2. Add or update the diagnose check-to-container mapping when applicable.
3. Update both packages' allowlist tests, including the subset assertion.
4. Add the container to the production deploy if
   the sidecar's docker socket cannot already see it (network-namespace caveat).

Never expose arbitrary container names from request data.

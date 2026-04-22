# access_list role

Holds the commander allowlist consumed by `control_api` when rendering the
backend environment file.

## Adding a commander

Edit `defaults/main.yml` and append to `commander_fid_allowlist`:

```yaml
commander_fid_allowlist:
  - F2504   # Pattern State (David)
  - F9999   # CMDR Someone Else
```

Then `make deploy-backend-api` to push the new list. The control-api
container restarts and the new FID can log in on the next attempt.

## Removing a commander

Remove the line and redeploy. Existing EDIN JWTs for that FID remain valid
until they expire (default 24h) — nothing in this role revokes them. If
immediate revocation matters, clear the commander's Redis `jti` entries
too.

## Why a separate role and not group_vars?

Two reasons:

1. **Discoverability.** A new maintainer looking for "who can log in?"
   finds it via `roles/access_list/` rather than trawling a grab-bag
   `group_vars/all.yml`.
2. **Future task surface.** The role currently has no operational tasks —
   it's a variable bundle — but leaves room for future work: rotating the
   login-attempt log, validating FID format, pushing allowlist changes
   without a full control-api restart.

## Why not in ansible-vault?

FIDs aren't secret. They appear in every commander's journal output, in any
EDIN JWT this backend issues, and in frontend presence responses. Encrypting
them would add friction without adding security.

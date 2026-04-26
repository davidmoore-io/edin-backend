# EDIN Discord Bot — Developer Portal Setup

This document captures the Discord Developer Portal configuration for the EDIN bot Application. **It contains no secret values** — secrets live only in `atlas/ansible/group_vars/<group>/vault.yml`. If the bot's identity ever needs rebuilding, this document is sufficient to recreate the same configuration.

## Application identifiers

| Field | Value | Where it lives |
|---|---|---|
| Application name | EDIN | Developer Portal |
| Application ID | `1487253410658123808` | vault: `vault_edin_bot_application_id` (not strictly secret; lives next to the secret for convenience) |
| Public Key | (from General Information tab) | vault: `vault_edin_bot_public_key` |
| Bot Token | (from Bot tab → Reset Token) | vault: `vault_edin_bot_discord_token` |

## Application settings (Developer Portal)

### Authorization Flow
- Public Bot: **OFF**
- Requires OAuth2 Code Grant: **OFF**

### Privileged Gateway Intents
- Presence Intent: **OFF**
- Server Members Intent: **OFF**
- Message Content Intent: **ON** (required for future DM/chat features)

### Installation
- Installation Contexts: Guild Install ✓ + User Install ✓
- Install Link: **None** (we use OAuth2 → URL Generator manually)

### Bot Permissions (integer: `517543939136`)

Decoded:
- View Channels (1 << 10)
- Add Reactions (1 << 6)
- Send Messages (1 << 11)
- Embed Links (1 << 14)
- Attach Files (1 << 15)
- Read Message History (1 << 16)
- Use External Emojis (1 << 18)
- Use Slash Commands / Use Application Commands (1 << 31)
- Create Public Threads (1 << 35)
- Create Private Threads (1 << 36)
- Use External Stickers (1 << 37)
- Send Messages in Threads (1 << 38)

Explicitly **OFF** (not granted):
- Administrator
- Manage Channels / Manage Roles / Manage Webhooks / Manage Server / Manage Events / Manage Messages / Manage Threads / Manage Nicknames / Manage Expressions
- Mention Everyone / Bypass Slowmode
- Kick / Ban / Moderate Members
- All voice permissions
- Use External Apps / Use Embedded Activities / Send Voice Messages / Send TTS / Create Polls

### OAuth Scopes
- `bot`
- `applications.commands`

## OAuth invite URLs

These are the canonical URLs used to install the bot. Both have already been used to install into both guilds. **Do not regenerate** unless the permission set changes.

```
Guild install:
  https://discord.com/oauth2/authorize?client_id=1487253410658123808&permissions=517543939136&integration_type=0&scope=bot+applications.commands

User install (for future DM chat — not exercised in MVP):
  https://discord.com/oauth2/authorize?client_id=1487253410658123808&permissions=517543939136&integration_type=1&scope=bot+applications.commands
```

## Discord destinations

| Guild | Guild ID | Channel | Channel ID | Purpose |
|---|---|---|---|---|
| Kaine | `1334858214533103646` | (alerts) | `1487248197582852321` | platinum + LTD alerts (MVP) |
| edin.space | `1497743490744975534` | `#edin-ops` | `1497743648488554607` | ops health alerts |

## Vault entries the deployment requires

```yaml
# atlas/ansible/group_vars/<group>/vault.yml
vault_edin_bot_discord_token: "<from Bot tab>"
vault_edin_bot_application_id: "1487253410658123808"
vault_edin_bot_public_key: "<from General Information>"
vault_edin_bot_oauth_client_id: "<minted by Authentik provisioning task — Phase 2>"
vault_edin_bot_oauth_client_secret: "<minted by Authentik provisioning task — Phase 2>"
vault_edin_bot_db_password: "<minted by Phase 1.1 db-role provisioning>"
```

## Recovery procedure

If the bot's Discord identity must be rebuilt (token leak, account compromise):

1. Visit https://discord.com/developers/applications/1487253410658123808
2. Bot tab → **Reset Token** — invalidates the previous token
3. Update `vault_edin_bot_discord_token` in vault: `cd atlas/ansible && ansible-vault edit group_vars/<group>/vault.yml`
4. Redeploy: `make deploy-edin-bot` from repo root
5. Bot reconnects with new token; existing posted messages remain accessible (Discord identifies the bot by Application ID, not token)

If the Application itself must be rebuilt (compromise of the entire bot account):

1. Discord Developer Portal → New Application → name "EDIN"
2. Reapply every setting in this document exactly
3. Update `vault_edin_bot_application_id` and `vault_edin_bot_public_key`
4. Generate new OAuth invite URLs (the Application ID changes — URLs above will not work)
5. Re-authorize the bot into both guilds (Discord requires interactive authorization per guild)
6. Existing `discord.posted_messages` rows reference message_ids posted by the OLD bot account. Those messages cannot be edited by the NEW bot. Acceptable: bot will create fresh posts on next cycle for each identity, leaving the old (now-orphaned) messages untouched. To clean up, manually delete the orphans in Discord.

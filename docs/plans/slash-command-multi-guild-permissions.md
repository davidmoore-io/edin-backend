# Plan: Multi-Guild Slash Command Permissions

**Objective**: Register `/watch` and `/unwatch` in two Discord guilds with
channel-level visibility restrictions and per-guild role access controls.

| Guild | ID | Watch channel | Access |
|---|---|---|---|
| Kaine | `1334858214533103646` | `1498813935057637597` | Administrators only |
| New server | `1289051766456848546` | `1503701700320432239` | Director, Lead Operative, Council Member |

---

## How Discord's permission model works here

### `DefaultMemberPermissions`

Set at command registration time. A bitmask of the permissions a guild member
must hold to see and use the command. Discord enforces this server-side and
never sends the interaction to our bot if the user lacks the required
permissions.

- `"8"` (Administrator): command is hidden from everyone except admins.
- `"0"`: command is hidden from everyone by default; the Permissions API then
  grants access to specific roles.

### Application Command Permissions API

A per-guild, per-command endpoint that overrides visibility with role, user, or
channel-scoped rules. Called after `ApplicationCommandCreate`.

Each override: `{ id, type (1=ROLE / 2=USER / 3=CHANNEL), permission (bool) }`.

A special channel ID of `guild_id - 1` represents "all channels" in Discord's
documented permissions model. Setting this to `permission: false` hides the
command across all channels; an explicit `permission: true` for a specific
channel then carves out the single allowed channel.

### The Administrator bypass

Administrators always bypass Application Command Permissions overrides.
Discord documents this explicitly. For Kaine (admin-only), channel restrictions
via the API have no effect; the runtime channel gate in the slash router
(ephemeral error reply) is the only per-channel enforcement mechanism for
admins. This is acceptable.

### Why the runtime `RequirePermissions` check must be removed

The router currently re-checks `ic.Member.Permissions & PermissionAdministrator`
at dispatch time. For the new server's Director/Lead Operative/Council Member
roles (none of whom hold Administrator), this gate blocks them even after
Discord has validated their access via the Permissions API.

The correct security model: Discord owns access control; by the time an
interaction reaches our bot, Discord has already verified the user is permitted.
The router's responsibility is the channel gate only.

---

## Files to change

### 1. `internal/edinbot/bindings/binding.go`

**Why**: Add `SlashGuild` to carry per-guild slash config, and wrap the
existing `[]Binding` in a `Config` struct so one `Load` call returns both.

**Changes**:

```go
// SlashGuild holds slash-command registration config for one Discord guild.
type SlashGuild struct {
    GuildID        string   // Discord snowflake
    WatchChannelID string   // Channel where /watch and /unwatch are honoured
    // AllowedRoleIDs: empty = admin-only (DefaultMemberPermissions="8")
    // Non-empty = DefaultMemberPermissions="0" + Permissions API for roles
    //             + channel restriction via Permissions API
    AllowedRoleIDs []string
}

// Config is the parsed, validated result of loading bindings.yml.
type Config struct {
    Bindings    []Binding
    SlashGuilds []SlashGuild
}
```

---

### 2. `internal/edinbot/bindings/loader.go`

**Why**: `Load` returns `[]Binding` today. It must return `Config` to expose
the new `SlashGuilds` field. The YAML file shape must also accept
`slash_guilds`.

**Changes**:

Add private parse struct:
```go
type rawSlashGuild struct {
    GuildID        string   `yaml:"guild_id"`
    WatchChannelID string   `yaml:"watch_channel_id"`
    AllowedRoleIDs []string `yaml:"allowed_role_ids,omitempty"`
}
```

Update `fileShape`:
```go
type fileShape struct {
    Bindings    []rawBinding    `yaml:"bindings"`
    SlashGuilds []rawSlashGuild `yaml:"slash_guilds,omitempty"`
}
```

Change `Load` signature:
```go
func Load(r io.Reader) (Config, error)
```

Add `validateSlashGuild(raw rawSlashGuild, seenGuilds map[string]bool) (SlashGuild, error)`:
- `guild_id` must be a non-empty numeric snowflake (reuse `snowflakePattern`)
- `watch_channel_id` must be a non-empty numeric snowflake
- Each entry in `allowed_role_ids` must be a numeric snowflake
- Duplicate `guild_id` across `slash_guilds` entries is rejected
- `slash_guilds` section is entirely optional; empty slice is valid

Note: `KnownFields(true)` on the decoder means the YAML parser will reject
any field not present in `fileShape`. Adding `slash_guilds` to the struct is
all that's needed to accept it.

---

### 3. `internal/edinbot/bindings/loader_test.go`

**Why**: `Load` returns `Config` — every existing call site breaks. New coverage
is also needed for `slash_guilds` validation.

**Changes**:

- All `bs, err := bindings.Load(...)` → `cfg, err := bindings.Load(...)`;
  slice assertions use `cfg.Bindings`.
- Add `TestLoader_SlashGuilds_HappyPath`: one guild with roles, one without;
  verify `cfg.SlashGuilds` length, field values, and that `Bindings` is
  unaffected.
- Add `TestLoader_SlashGuilds_DuplicateGuildFails`: same `guild_id` twice →
  error containing `"duplicate"`.
- Add `TestLoader_SlashGuilds_BadGuildSnowflakeFails`: non-numeric `guild_id`
  → error containing `"guild_id"`.
- Add `TestLoader_SlashGuilds_BadChannelSnowflakeFails`: non-numeric
  `watch_channel_id` → error containing `"watch_channel_id"`.
- Add `TestLoader_SlashGuilds_BadRoleSnowflakeFails`: non-numeric entry in
  `allowed_role_ids` → error containing `"allowed_role_ids"`.
- Add `TestLoader_NoSlashGuildsIsValid`: a `bindings.yml` with no
  `slash_guilds` key → no error, `cfg.SlashGuilds` is nil/empty.

---

### 4. `internal/edinbot/features/watcher/watcher.go`

**Why**: `Config.AllowedChannelID` is dead code. It is defined and set from
`main.go` but is never read anywhere in the handler or loop. Its presence is
misleading — it implies a channel restriction exists at the handler level when
the restriction actually lives in the slash router. Remove it to prevent future
confusion.

**Changes**:
- Remove the `AllowedChannelID string` field and its comment from `Config`.

---

### 5. `internal/edinbot/features/watcher/handler.go`

**Why**: `HandlerDeps.GuildID` is set at bot startup from the `SLASH_GUILD_ID`
env var — a single fixed value. With multiple guilds, the correct value is
`ic.GuildID`, which is present on every incoming interaction and is already the
right guild for the row being persisted.

`deps.GuildID` is used in exactly two places:
- Line 190: `GuildID: deps.GuildID` when persisting the `WatchedSystem` row
- Line 219: `messageLink(deps.GuildID, ic.ChannelID, msgID)` in the success reply

**Changes**:
- Remove `GuildID string` field (and its comment) from `HandlerDeps`
- Line 190: `GuildID: deps.GuildID` → `GuildID: ic.GuildID`
- Line 219: `messageLink(deps.GuildID, ...)` → `messageLink(ic.GuildID, ...)`

---

### 6. `internal/edinbot/features/watcher/handler_test.go`

**Why**: Tests construct `HandlerDeps` with `GuildID: "kaine-guild"`. Since
the field is removed, the guild ID must come from the interaction object. Tests
also assert `require.Equal(t, "kaine-guild", got.GuildID)` on persisted rows —
this still passes because the value now flows from `ic.GuildID`.

**Changes**:
- Remove `GuildID: "kaine-guild"` from the `HandlerDeps` literal in
  `makeTestDeps()` (or wherever the shared deps factory lives).
- Add `GuildID: "kaine-guild"` to the `Interaction` field of every
  `discordgo.InteractionCreate` object used in tests that verify row
  persistence. The assertion on the persisted row is unchanged.

---

### 7. `internal/edinbot/slash/slash.go`

**Why**: `Config.RequirePermissions` and its enforcement in `dispatch` must be
removed. With multi-guild support, Discord's platform-level permission controls
(`DefaultMemberPermissions` + the Permissions API) are the authoritative access
gate. The runtime check duplicates this gating and, critically, blocks non-admin
role holders (Director et al.) from using the command in the new server.

The `ic.Member == nil` check is retained as a safety belt against unexpected
DM-context interactions (defence in depth; should never be reached).

**Changes**:

In `Config`:
- Remove `RequirePermissions int64` field and its comment.

In `NewRouter`:
- Remove `if cfg.RequirePermissions == 0 { cfg.RequirePermissions = discordgo.PermissionAdministrator }`.

In `dispatch`:
- Remove the `ic.Member.Permissions & r.cfg.RequirePermissions == 0` check and
  its error reply.
- Retain the `ic.Member == nil` guard with updated comment:
  ```go
  // Guard against unexpected DM-context interactions; ic.Member is nil
  // outside a guild. DMPermission:false on the command and the channel gate
  // above make this unreachable in normal operation.
  if ic.Member == nil {
      r.replyEphemeral(resp, ic, "This command requires guild membership.")
      return
  }
  ```

Update package doc comment at top of file: remove the line
`RequirePermissions: discordgo.PermissionAdministrator,` from the example.

---

### 8. `internal/edinbot/slash/slash_test.go`

**Why**: `RequirePermissions: discordgo.PermissionAdministrator` appears in
five test `Config` literals. Removing the field from `Config` breaks compilation.
Any test that specifically exercises the permission gate (wrong permissions →
ephemeral error) must also be removed or repurposed.

**Changes**:
- Remove `RequirePermissions: discordgo.PermissionAdministrator` from all five
  `Config` literals.
- Identify and remove any test case whose sole purpose is asserting the
  "requires Administrator permission" error branch (the branch no longer
  exists). The channel-gate tests, happy-path tests, and unknown-command tests
  are all unaffected.
- If `mkInteraction` takes a permissions bitmask argument only to populate
  `ic.Member.Permissions`, simplify the helper — the permissions value is no
  longer checked.

---

### 9. `cmd/edin-bot/main.go`

**Why**: The slash setup is driven by two env vars (`WATCH_CHANNEL_ID`,
`SLASH_GUILD_ID`) and a single `startSlashAndWatcher` call. This is replaced
by iterating `bindings.Config.SlashGuilds`, registering commands per guild, and
applying the Permissions API for role-restricted guilds.

**Changes**:

Remove from `envConfig`:
```go
WatchChannelID string
SlashGuildID   string
```
Remove from `loadEnv()` population and from the "missing required" validation
list.

Update `loadBindings`:
```go
func loadBindings(path string) (bindings.Config, error)
```
The scheduler receives `bindingsCfg.Bindings`; the slash setup receives
`bindingsCfg.SlashGuilds`.

Replace the existing `if cfg.WatchChannelID != "" && cfg.SlashGuildID != ""`
block with:
```go
if len(bindingsCfg.SlashGuilds) > 0 {
    if err := setupSlash(ctx, bindingsCfg.SlashGuilds, st, control, dc); err != nil {
        log.Printf("[WARN] slash setup failed: %v (continuing without /watch feature)", err)
    }
}
```

Replace `startSlashAndWatcher` with two new functions:

**`setupSlash(ctx, guilds, st, control, dc) error`**

```
1. Collect all watch channel IDs from every guild entry.
2. Wire one router for all guilds:
     router := slash.NewRouter(slash.Config{AllowedChannelIDs: allWatchChannelIDs})
     deps  := watcher.HandlerDeps{Store: st, Snap: control, Discord: dc, Cfg: watcher.Config{}}
     router.Handle("watch",   watcher.Watch(deps))
     router.Handle("unwatch", watcher.Unwatch(deps))
     dc.Session().AddHandler(router.Dispatch)
3. For each guild, call registerSlashGuild (registration + permissions).
   Any error is returned immediately — a misconfigured guild is a hard failure.
4. Start the watcher loop once (it polls all persisted rows, any guild):
     w := watcher.NewWatcher(watcher.LoopDeps{Store: st, Snap: control, Discord: dc})
     w.Start(ctx)
```

**`registerSlashGuild(sess *discordgo.Session, appID string, guild bindings.SlashGuild) error`**

```
adminOnly := len(guild.AllowedRoleIDs) == 0

Build command specs:
  dmsBlocked := false
  var defaultPerms int64
  if adminOnly {
      defaultPerms = discordgo.PermissionAdministrator  // = 8
  } else {
      defaultPerms = 0
  }
  commands := []*discordgo.ApplicationCommand{
      {Name: "watch",   ..., DMPermission: &dmsBlocked, DefaultMemberPermissions: &defaultPerms},
      {Name: "unwatch", ..., DMPermission: &dmsBlocked, DefaultMemberPermissions: &defaultPerms},
  }

For each command:
  created, err := sess.ApplicationCommandCreate(appID, guild.GuildID, cmd)
  if err != nil { return fmt.Errorf("register %s in guild %s: %w", cmd.Name, guild.GuildID, err) }

  if !adminOnly {
      if err := applyChannelPermissions(sess, appID, guild, created.ID); err != nil {
          return fmt.Errorf("permissions %s in guild %s: %w", cmd.Name, guild.GuildID, err)
      }
  }
```

**`applyChannelPermissions(sess *discordgo.Session, appID string, guild bindings.SlashGuild, cmdID string) error`**

```go
guildIDUint, err := strconv.ParseUint(guild.GuildID, 10, 64)
if err != nil {
    return fmt.Errorf("invalid guild_id %q: %w", guild.GuildID, err)
}
allChannelsID := strconv.FormatUint(guildIDUint-1, 10) // Discord "all channels" constant

perms := make([]*discordgo.ApplicationCommandPermissions, 0, len(guild.AllowedRoleIDs)+2)
for _, roleID := range guild.AllowedRoleIDs {
    perms = append(perms, &discordgo.ApplicationCommandPermissions{
        ID:         roleID,
        Type:       discordgo.ApplicationCommandPermissionTypeRole,
        Permission: true,
    })
}
perms = append(perms,
    &discordgo.ApplicationCommandPermissions{
        ID:         allChannelsID,
        Type:       discordgo.ApplicationCommandPermissionTypeChannel,
        Permission: false,
    },
    &discordgo.ApplicationCommandPermissions{
        ID:         guild.WatchChannelID,
        Type:       discordgo.ApplicationCommandPermissionTypeChannel,
        Permission: true,
    },
)
return sess.ApplicationCommandPermissionsEdit(appID, guild.GuildID, cmdID,
    &discordgo.ApplicationCommandPermissionsList{Permissions: perms})
```

---

### 10. `atlas/ansible/roles/edin_bot/templates/bindings.yml.j2`

**Why**: The bot reads slash guild config from `bindings.yml` rather than env
vars. The template must carry the new `slash_guilds` section.

**Changes**: Add below the existing `bindings:` list:
```yaml
slash_guilds:
  - guild_id: "1334858214533103646"          # Kaine
    watch_channel_id: "1498813935057637597"   # #system-watch
    # No allowed_role_ids → admin-only (DefaultMemberPermissions="8")

  - guild_id: "1289051766456848546"           # new server
    watch_channel_id: "1503701700320432239"   # #edin-watch
    allowed_role_ids:
      - "1289051766582677507"  # Director
      - "1353722595043971112"  # Lead Operative
      - "1329039235063480360"  # Council Member
```

---

### 11. `atlas/ansible/roles/edin_bot/defaults/main.yml`

**Why**: `edin_bot_watch_channel_id` and `edin_bot_slash_guild_id` are no
longer referenced by any template. Dead defaults invite future confusion.

**Changes**: Remove both lines.

---

### 12. `atlas/ansible/roles/edin_bot/templates/edin-bot.env.j2`

**Why**: `WATCH_CHANNEL_ID` and `SLASH_GUILD_ID` env vars are no longer read
by the bot binary.

**Changes**: Remove the comment block and the two variable lines:
```
# Slash-command surface for the /watch /unwatch system-watch feature.
# When both are set, the bot registers two guild-local slash commands and
# starts the 120s polling loop. Unset → feature dormant; alerts side keeps
# running normally.
WATCH_CHANNEL_ID={{ edin_bot_watch_channel_id }}
SLASH_GUILD_ID={{ edin_bot_slash_guild_id }}
```

---

## Implementation order

Execute in this sequence to keep the build green at each step:

1. `binding.go` — add `SlashGuild` and `Config` types (no callers yet)
2. `loader.go` — update `Load` return type, add slash guild parsing + validation
3. `loader_test.go` — fix broken call sites, add new tests; verify with `go test ./internal/edinbot/bindings/...`
4. `watcher.go` — remove dead `AllowedChannelID` field from `Config`
5. `handler.go` — remove `GuildID` from `HandlerDeps`, use `ic.GuildID`
6. `handler_test.go` — fix deps construction, set `GuildID` on interactions; verify with `go test ./internal/edinbot/features/watcher/...`
7. `slash.go` — remove `RequirePermissions` from `Config` and `dispatch`
8. `slash_test.go` — remove `RequirePermissions` from configs, remove permission-gate tests; verify with `go test ./internal/edinbot/slash/...`
9. `main.go` — full slash setup refactor; `go build ./cmd/edin-bot`, `go test ./internal/edinbot/...`
10. Ansible: `bindings.yml.j2`, `defaults/main.yml`, `edin-bot.env.j2`
11. `make deploy-edin-bot`

---

## Risk notes

### `guild_id - 1` as the "all channels" constant

Discord documents this constant for the Application Command Permissions API —
the channel ID `(guild_id as uint64) - 1` means "all channels in this guild".
It is used to set a blanket channel deny before carving out the single allowed
channel. If Discord's API rejects this value or the restriction does not take
effect on first deploy, the runtime channel gate (ephemeral error from the
router) is the fallback enforcement layer while we investigate.

### Re-registration is idempotent

`ApplicationCommandCreate` called with the same name and signature on an
existing guild command is a no-op on Discord's side (it returns the existing
command with its current ID). `ApplicationCommandPermissionsEdit` replaces all
permission overrides on each call — it is not additive. Both are safe to
call on every bot startup.

### Admins see `/watch` in all Kaine channels

As documented: Discord does not honour channel permission overrides for members
with Administrator. In Kaine, admins see `/watch` in every channel's
autocomplete. The runtime channel gate (ephemeral "not enabled in this channel"
reply) is the only enforcement. This is explicitly accepted.

### `strconv.ParseUint` on `guild_id`

`guild_id` is validated as a numeric snowflake in the bindings loader, so
`ParseUint` in `applyChannelPermissions` will not fail in practice. The error
path is kept as belt-and-suspenders only.

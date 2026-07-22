# ReminderRelay

Event-driven, bidirectional sync daemon that projects **iCloud Reminders** into **Home Assistant** todo lists while keeping iCloud authoritative.

```
iCloud Reminders  ←──── commands ────  Home Assistant
        │                                  ▲
        └──── canonical projection ────────┘
             EventKit + native push
```

## Features

- **iCloud is always authoritative** — HA edits are committed to iCloud first, then HA is refreshed from the accepted canonical representation. Concurrent conflicts always resolve to iCloud.
- **Bidirectional sync** — creates, edits, completions, and deletions made in either app propagate to the other.
- **Push on both sides** — `EKEventStoreChangedNotification` receives iCloud/Reminders changes and HA's `todo/item/subscribe` stream receives every todo-item mutation, including edits that do not change entity state.
- **Recurring reminders** — native EventKit recurrence rules round-trip; completing an occurrence lets Reminders materialize the next occurrence, which is pushed into HA with its next due date/time.
- **Full metadata projection** — tags, recurrence rules, assignments, exact due date/time, notes, completion, and priority round-trip. Fields HA lacks natively live in a readable JSON metadata block at the end of the description.
- **Native assignments** — shared-list assignees are read and written through guarded ReminderKit runtime APIs on macOS. Assignees are resolved against participants already in the iCloud shared list.
- **Recovery, not polling** — a six-hour full reconciliation is only a safety net for events missed while the daemon or network was unavailable.
- **Priority mapping** — Apple Reminders priorities are encoded as `[High]`, `[Medium]`, `[Low]` prefixes in HA descriptions.
- **First-run bootstrap** — interactive wizard that matches existing items between both sides by title and prompts before writing anything.
- **Persistent state database** — SQLite tracks sync metadata so resuming after a restart is safe.

## Prerequisites

| Requirement | Version |
|---|---|
| macOS | 26 Tahoe for the assignment bridge; EventKit fields work on earlier supported releases |
| Apple ID / iCloud | Signed in with Reminders enabled |
| Home Assistant | A todo entity supporting CRUD, descriptions, due dates, and due datetimes |
| HA long-lived access token | Profile → Security → Long-Lived Access Tokens |

## Quick Start

### 1. Install devbox (once)

```bash
curl -fsSL https://get.jetify.com/devbox | bash
```

### 2. Clone and enter the dev shell

```bash
git clone https://github.com/nworb-cire/reminderrelay.git
cd reminderrelay
devbox shell
```

### 3. Run the setup wizard

```bash
just build
reminderrelay setup
```

For unattended credential entry, the wizard accepts `REMINDERRELAY_HA_URL`
(or `HASS_URL`) and `REMINDERRELAY_HA_TOKEN` (or
`HOME_ASSISTANT_TOKEN`/`HASS_TOKEN`) from the environment. The generated
configuration persists the resolved values for launchd.

The wizard will walk you through:
1. Connecting to your Home Assistant instance
2. Discovering Reminders lists and HA todo entities
3. Mapping lists to entities interactively
4. Writing the config file
5. Reviewing and running the initial iCloud-authoritative sync
6. Optionally installing as a background daemon

The wizard prompts you to review and confirm bootstrap matches before it installs the daemon — nothing is written until you type **y**.

<details>
<summary>Manual config (alternative to wizard)</summary>

```bash
mkdir -p ~/.config/reminderrelay
cp config.example.yaml ~/.config/reminderrelay/config.yaml
$EDITOR ~/.config/reminderrelay/config.yaml
```

Key fields:

```yaml
ha_url: "http://homeassistant.local:8123"
ha_token: "your-long-lived-access-token-here"
recovery_interval: 6h
list_mappings:
  "Shopping": "todo.shopping"
  "Work":     "todo.work_tasks"
```

Then test with `just sync-once` and install with `just install`.

</details>

## CLI Reference

```bash
reminderrelay setup                     # interactive first-run wizard
reminderrelay daemon [--config <path>]  # start native push listeners
reminderrelay sync-once [--config ...]  # single reconcile pass then exit
reminderrelay status                    # show daemon & config state
reminderrelay uninstall [--purge]       # stop daemon and remove files
reminderrelay version                   # print version
```

Legacy flag-based invocation (`--daemon`, `--sync-once`) is still supported for backward compatibility.

## Configuration Reference

| Key | Type | Default | Description |
|---|---|---|---|
| `ha_url` | string | — | Home Assistant base URL (`http://…` or `https://…`) |
| `ha_token` | string | — | Long-lived access token |
| `recovery_interval` | duration | `6h` | Safety reconciliation interval (15 m – 24 h); normal sync is push-driven |
| `list_mappings` | map | — | `"Reminders list name": "todo.entity_id"` |
| `telemetry` | object | *(disabled)* | Optional OpenTelemetry export (see below) |

### Telemetry (optional)

Export traces, metrics, and logs to any OTLP-compatible collector (e.g. Grafana Alloy, Jaeger, Dash0).

```yaml
telemetry:
  otlp_endpoint: "localhost:4317"
  insecure: true
  service_name: "reminderrelay"   # optional, defaults to "reminderrelay"
  headers:                          # optional gRPC metadata
    Authorization: "Bearer <token>"
```

## Discovering Your HA Entity IDs

1. Open Home Assistant → **Settings → Devices & services → Entities**.
2. Filter by domain **todo**.
3. Copy the entity IDs (e.g. `todo.shopping`) into `list_mappings`.

Or run:

```bash
just sync-once -- --verbose 2>&1 | grep "entity"
```

## Home Assistant projection format

Apple Reminders supports four priority levels.  
Home Assistant todo has no native priority field, so ReminderRelay encodes priority as a prefix in the task description:

| Reminders priority | Description prefix |
|---|---|
| High | `[High] ` |
| Medium | `[Medium] ` |
| Low | `[Low] ` |
| None | *(no prefix)* |

Tags, assignments, recurrence, and the stable iCloud reminder ID are encoded after the notes:

```text
Bring the blue bin

--- ReminderRelay metadata ---
{"version":1,"icloud_uid":"…","tags":["outside"],"assignment":{"name":"Madi","address":"…"},"recurrence":[…]}
--- End ReminderRelay metadata ---
```

The block is deliberately editable. An HA edit is treated as a requested iCloud change. iCloud normalizes or rejects it, and the next HA state is always rebuilt from iCloud. Assignment names, addresses, or stable IDs must identify a participant in that reminder's shared iCloud list.

## Justfile Recipes

```bash
just build        # compile binary
just test         # run all tests
just lint         # run golangci-lint
just run          # run daemon in foreground (Ctrl-C to stop)
just sync-once    # run one sync cycle and exit
just install      # build + install + load launchd agent
just uninstall    # unload + remove binary and plist
```

## Logs

| Location | Contents |
|---|---|
| `~/Library/Logs/reminderrelay/output.log` | Info and debug output |
| `~/Library/Logs/reminderrelay/errors.log` | Errors and warnings |

Tail logs live:

```bash
tail -f ~/Library/Logs/reminderrelay/errors.log
```

## Uninstall

```bash
reminderrelay uninstall          # stop daemon + remove binary and plist
reminderrelay uninstall --purge  # also remove config, state DB, and logs
```

## Troubleshooting

### Reminders access denied (TCC)

macOS requires explicit permission for apps to access Reminders.  
On first run a system dialog appears — click **OK**.  
If you previously denied access:

1. Open **System Settings → Privacy & Security → Reminders**.
2. Enable access for Terminal (or your shell app).

### HA connection refused

- Confirm `ha_url` is reachable: `curl -s <ha_url>/api/ -H "Authorization: Bearer <token>"`
- Ensure the token has not expired or been revoked.

### Items duplicated after restart

This usually means the state database was deleted while items still existed in both systems. Remove the DB and re-run the bootstrap:

```bash
rm ~/.local/share/reminderrelay/state.db
just sync-once
```

### A mapped HA todo entity is rejected

ReminderRelay refuses todo providers that cannot preserve CRUD, descriptions, dates, and date-times. Create a **Local to-do** list in Home Assistant and map the iCloud list to that entity.

### Push connection was interrupted

Both listeners reconnect automatically. `recovery_interval` controls the low-frequency safety reconciliation; reducing it should not be necessary during normal operation.

## Architecture

```
cmd/reminderrelay/        Entry point, subcommand dispatch, wiring
internal/config/          YAML config loader + validation
internal/state/           SQLite repository (WAL mode)
internal/model/           Shared Item type, metadata codec, content hash
internal/reminders/       EventKit adapter + guarded assignment bridge
internal/homeassistant/   HA REST + native todo WebSocket subscription
internal/sync/            Reconciler, bootstrap wizard, daemon engine
internal/setup/           Interactive setup wizard, daemon install/uninstall
internal/telemetry/       Optional OpenTelemetry OTLP gRPC export
deployment/               launchd plist, install/uninstall scripts
```

## License

MIT — see [LICENSE](LICENSE).

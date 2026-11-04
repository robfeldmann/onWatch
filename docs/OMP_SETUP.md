# OMP Broker Setup Guide

Track every account held by an **Oh My Pi (OMP) auth broker** in onWatch: each Anthropic, Cursor, Codex, OpenCode Go or other account the broker reports gets its own history and reset cycles.

onWatch polls one read-only endpoint, `GET {OMP_BROKER_URL}/v1/usage`. The broker returns one usage report per account. Each report lists that account's limits (5-hour and weekly windows, monthly spend, and so on) with the used amount, the cap, the unit, and the reset time.

---

## Prerequisites

- A running OMP auth broker reachable from onWatch
- A bearer token for the broker, or a command that prints one
- onWatch installed ([Quick Start](../README.md#quick-start))

---

## Configure onWatch

Add to `~/.onwatch/.env`:

```dotenv
OMP_BROKER_URL=http://127.0.0.1:8765
OMP_BROKER_TOKEN_COMMAND=onwatch-token omp-broker
```

| Variable | Purpose |
|---|---|
| `OMP_BROKER_URL` | Broker base URL. Setting it enables the `omp` provider. There is no default. |
| `OMP_BROKER_TOKEN_COMMAND` | Command that prints the broker's bearer token. See [Token Command Setup](TOKEN_COMMAND_SETUP.md). |
| `OMP_BROKER_TOKEN` | Static bearer token. Use it when you have no token command. |

The token command runs at startup, before every poll, and after a 401 response. A 401 is retried once with the new token. Unlike the other token commands, a failed command does not disable the provider: the broker may start after onWatch, so the next poll tries again. onWatch never writes the token to disk.

Restart onWatch, or use **Settings -> Providers -> Reload**, to apply changes.

---

## How Accounts Are Recorded

- Each report becomes one account, keyed `<provider>:<identity>`. The identity is the report's `metadata.email`, or `metadata.accountId` when there is no email. When one response has two accounts with the same email for the same provider (for example personal and team Anthropic organisations), `#<orgId>` or `#<accountId>` is appended.
- An account the broker starts reporting is recorded automatically on the next poll.
- The broker caches each report for about 5 minutes. onWatch stores a new snapshot for an account only when its `fetchedAt` changes, so polling faster than the cache adds no duplicate rows.
- A response that cannot be parsed (missing fields, wrong types, duplicate accounts) fails the whole poll. Nothing is stored, so bad data is never recorded as zero usage.
- Utilization is the used share of each limit in percent. onWatch uses `usedFraction`, then `used / limit`, then `used` for percent units, then `1 - remainingFraction`. A limit with none of these keeps a null utilization and is not tracked for resets or alerts.
- A report with no limits (for example a Cursor team account whose spend is withheld) is still recorded, with the broker's notes.
- Warning, critical and reset alerts fire once per crossing per account and limit, using the normal notification thresholds. They name the quota as `Cursor (me@example.com) Personal Usage at 81.2%`; webhooks also carry `source_provider`, `account_label` and `quota_label` (see [Webhook Notifications](WEBHOOK_SETUP.md#payload)). Per-quota threshold overrides use the key `omp:<limit id>`.

History starts when the provider is first enabled. Past broker data is not imported.

---

## Dashboard

The **OMP** tab shows every broker account:

- **Account picker** - *All accounts* groups the limit cards under one heading per account (provider, email, organisation, plan, and when the broker last fetched it). Picking an account narrows the cards, the usage chart and the insights to it. The choice is remembered in the browser.
- **Limit cards** - spend limits show dollars (`$34.45 / $250.00`) under the percentage; percent windows show the window (`5 Hour window`). The bar takes the status colour, the badge reads *Exhausted* when the broker says so, and the countdown runs to the next reset. An account without limits shows the broker's notes instead (for example a withheld Cursor team limit).
- **Usage Graphs** - one line per limit (per account and limit in *All accounts*).
- **Usage Samples** - every stored report of the selected account. Hidden in *All accounts*.
- **All tab** - one OMP card with a compact summary per account; a summary opens the OMP tab on that account.

The tab has no session history and no cross-limit cycle overview.

## Menubar

The menubar shows one card per broker account, `OMP - Cursor · me@example.com` (just `OMP` when the broker holds a single account, or the tab name you set in Settings). A card lists the account's limits with the percent, `$used/$limit` for spend limits, and the reset. Accounts without reported limits have no card. **Settings -> Menubar -> Provider Order** lists every account on its own row (`OMP · Cursor · me@example.com`, or `<renamed tab> · Cursor · me@example.com` after a dashboard tab rename), so accounts can be dragged and shown or hidden one by one. The dashboard OMP tab (account picker and All accounts) follows the same order; accounts missing from it come after the ordered ones in their usual order, a new account is appended and shown when any OMP account is shown, and a removed one is ignored. In saved settings `omp` covers every account and `omp:<id>` one account. The tray can show any OMP limit. A card turns stale after 15 minutes without a new broker report, since the broker itself serves 5-minute-old reports.

## Prometheus

`/metrics` exports `onwatch_quota_utilization_percent` and `onwatch_quota_reset_timestamp_seconds` with `provider="omp"`, `account_id` = the account id, and `quota_type` = the broker limit id, plus `onwatch_account_info{provider="omp",account_name="Cursor · me@example.com"}` for joins and the usual `onwatch_agent_healthy` / `onwatch_agent_last_cycle_age_seconds` per account. See [Prometheus Metrics](PROMETHEUS_METRICS.md).

## API

The same data is available from the JSON API with `provider=omp`:

| Endpoint | Returns |
|---|---|
| `/api/current?provider=omp` | The newest snapshot of every account (the `account` parameter is ignored) |
| `/api/history?provider=omp&range=6h[&account=ID]` | Per-account utilization series, keyed by limit id, with `labels` mapping limit ids to labels |
| `/api/cycles?provider=omp&account=ID&type=LIMIT_ID` | Reset cycles of one limit, active first |
| `/api/summary?provider=omp[&account=ID]` | Per-account, per-limit cycle statistics |
| `/api/insights?provider=omp[&account=ID]` | Highest usage, tracked limits and next reset; insights for limits at warning or above and for accounts whose limits are withheld |
| `/api/logging-history?provider=omp&account=ID` | The account's stored samples (`quotaNames` = limit ids, `quotaLabels` = their labels); empty without `account` |
| `/api/cycle-overview?provider=omp` | Always empty |

`/api/current?provider=both` includes the same `omp` payload as `/api/current?provider=omp`.

`/api/current?provider=omp` returns:

```json
{
  "provider": "omp",
  "capturedAt": "2026-11-03T17:05:00Z",
  "accounts": [
    {
      "accountId": 3,
      "key": "anthropic:me@example.com",
      "sourceProvider": "anthropic",
      "providerLabel": "Anthropic",
      "label": "Anthropic · me@example.com",
      "identity": "me@example.com",
      "email": "me@example.com",
      "upstreamAccountId": "acct-0000",
      "orgId": "org-0000",
      "orgName": "Example Org",
      "planType": "",
      "fetchedAt": "2026-11-03T17:01:30Z",
      "capturedAt": "2026-11-03T17:02:00Z",
      "ageSeconds": 210,
      "isStale": false,
      "notes": [],
      "limits": [
        {
          "id": "anthropic:5h",
          "label": "Claude 5 Hour",
          "windowId": "5h",
          "windowLabel": "5 Hour",
          "windowDurationMs": 18000000,
          "used": 13,
          "limit": 100,
          "remaining": 87,
          "unit": "percent",
          "utilization": 13,
          "status": "healthy",
          "upstreamStatus": "ok",
          "resetsAt": "2026-11-03T18:30:00Z",
          "timeUntilReset": "1h 25m",
          "timeUntilResetSeconds": 5100,
          "notes": [],
          "currentRate": 0,
          "projectedUtil": 0
        }
      ]
    }
  ]
}
```

- `used`, `limit`, `remaining`, `utilization` and `windowDurationMs` are `null` when the broker did not report them. `resetsAt` is `null` for a limit without a reset time, and `timeUntilReset` / `timeUntilResetSeconds` are then omitted.
- `status` uses the dashboard vocabulary (`healthy`, `warning`, `critical`) derived from utilization, raised by the broker's own status: `exhausted` is always `critical`. `upstreamStatus` is the broker's raw value.
- `isStale` is true when the newest report is older than 30 minutes, for example an account the broker no longer reports.
- `currentRate` (percentage points per hour) and `projectedUtil` appear for limits with a utilization once a cycle is tracked.

`/api/history` returns `{"accounts": [{...account fields, "labels": {"anthropic:5h": "Claude 5 Hour", ...}, "points": [{"fetchedAt": "...", "values": {"anthropic:5h": 13, ...}}]}]}` with at most 500 points per account. `/api/summary` returns `{"accounts": [{...account fields, "limits": {"anthropic:5h": {"currentUtil", "currentRate", "projectedUtil", "completedCycles", "peakCycle", "avgPerCycle", "totalTracked", "trackingSince", "resetsAt", "timeUntilReset"}}}]}`.

---

## Storage

| Table | Contents |
|---|---|
| `provider_accounts` | One row per broker account: `provider = 'omp'`, `name` and `external_id` = the account key, `metadata` JSON with the source provider, identity, email, upstream account id, org id and name, and plan |
| `omp_snapshots` | `account_id`, `captured_at` (onWatch poll time), `fetched_at` (broker fetch time, unique per account), `source_provider`, `notes` (JSON array), `limit_count` |
| `omp_quota_values` | Per snapshot and limit: `account_id`, `limit_id`, `label`, `window_id`, `window_label`, `window_duration_ms`, `used`, `limit_value`, `remaining`, `unit`, `utilization`, `status`, `resets_at`, `notes` |
| `omp_reset_cycles` | Per account and limit: `cycle_start`, `cycle_end`, `resets_at`, `peak_utilization`, `total_delta` |

---

## Troubleshooting

| Log message | Cause |
|---|---|
| `Failed to fetch OMP broker usage ... omp: unauthorized` | The token is missing or rejected even after the token command ran. Check `OMP_BROKER_TOKEN_COMMAND` by running it in a shell. |
| `... omp: network error` | The broker is not reachable at `OMP_BROKER_URL`. |
| `... omp: invalid response` | The broker returned a body onWatch could not parse. The poll stored nothing. |
| `token command failed` | The token command exited non-zero, printed nothing, or timed out. The previous token is kept. |

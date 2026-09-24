# DNS Record Management (TXT-first) — Plan

> STATUS: all phases built + unit-tested + Go/UI building green. Remaining: ONE
> end-to-end validation against a real provider + a two-peer HA drift test (can't
> be done in this env — no live creds/fleet). Not yet committed.


> How this plan works: see the planning rules in CLAUDE.md. plan.md-style living doc.
> Status marks: ◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

## Goal
Manage arbitrary DNS records (TXT first, plus A/AAAA/CNAME) from the web UI, so
common tasks like adding a `google-site-verification` TXT don't require editing
the config file. Trigger: SEO / site-verification TXT for
`homelab-horizon.iodesystems.com`.

## Decisions locked (from user)
- **Full record manager** UI (list + add/edit/delete + type selector), not TXT-only.
- **Viewer reads LIVE from the provider**, diffed against config → each row tagged
  `HZ-managed` vs `unmanaged` (records added outside HZ are visible, not clobbered).
- **Set-based correctness** (not a user choice — required): records are keyed by
  `(name, type)`. TXT at one name is a *set* (google-verify + SPF + DKIM coexist).
  Never single-shot a TXT add/delete — always publish the whole set via
  `SetRecords`/`SyncRecordSet`. Rationale: libdns `SetRecords` replaces the entire
  (name,type) RRset (`libdns.go:161,185`) and `DeleteRecord` sends empty Data →
  matches ANY value (`libdns.go:205-210`), so naive per-record ops wipe siblings.
- **Drift-safe sync** (user requirement): during publish/sync, pull live provider
  state first. If the live set doesn't match the expected "from" set, DO NOT
  clobber — instead **sync-back** (write live values into config), **alert** the
  user, and **halt**. Three-way reconcile / optimistic lock.

## ✅ DECIDED — drift-halt scope: **ALL DNS sync** (option B)
Drift-detect + sync-back + halt covers *every* record published, including
service A/AAAA. Consequence to design for: a manual/out-of-band edit to ANY
managed record halts the whole sync — including automated failover / round-robin
updates. So the alert must be loud (ntfy + UI banner) and clearing drift must be
a one-click "accept live → adopt into config" action, or sync stays blocked.

## Key implementation facts (verified)
- **`SyncRecordSet` is the single publish chokepoint** — service A/AAAA sync
  (`handlers_api_mutations.go:400,484`) AND static-record sync (`syncZoneRecords`)
  both call it. Implement drift-detect here (or a wrapper) → covers ALL sync (opt B)
  from one place.
- **Provider fan-out**: 7/8 providers wrap `LibdnsAdapter` (route53, cloudflare,
  digitalocean, hetzner, gandi, googlecloud, duckdns). Only **Name.com**
  (`namecom.go:80` `NamecomProvider`) implements `Provider` directly. So
  `ListRecords` = 2 impls (LibdnsAdapter + NamecomProvider).

## Drift state model (Phase 3 design)
Drift needs a "last-published" snapshot to tell an intentional change from external
tampering. Per (name,type):
- `desired` = new set (config for static; computed public IPs for service A).
- `expected` = last set HZ successfully published (**new persisted state**).
- `live` = fetched from provider inside SyncRecordSet.
- live==desired → no-op. live==expected → normal change, publish, update expected.
- live≠expected AND live≠desired → **DRIFT**: sync-back (write live into
  config/expected), alert (ntfy+UI), return a sentinel error that aborts the sync.
- **sub-decision (surface at Phase 3)**: where `expected` lives — new
  `config` field per record vs a separate state store. Leaning: a
  `last_published` map in the persisted state alongside PublicIP tracking.

## Current state (verified)
- TXT already works at model/provider/sync layers: `dns.Record.Type` is free-form
  string (`provider.go:24`), `toLibdnsRecord` handles TXT (`libdns.go:384`),
  `config.DNSRecord` + `Validate/NormalizedType/EffectiveTTL` exist
  (`config.go:373-405`), `syncZoneRecords`/`buildZoneRecordSets` publish sets
  (`handlers_dns_records.go`).
- **Missing**: (1) interface has no list-all — only `GetRecord(name,type)`
  (`provider.go:39`); libdns `GetRecords` exists underneath (`libdns.go:124`).
  (2) No per-record CRUD HTTP endpoint — `Zone.Records` is config-file-only; zone
  add/edit structs don't carry `Records` (`handlers_api_settings.go:117,221`).
  (3) No records UI — `domains.tsx` is status-only, no record list/form.
  (4) No `apitypes` DNS-record struct → tygo generates no TS type.

## Phases

### Phase 1 — Read path: view live records ✅ (backend)
- ✅ `ListRecords(zoneID)` on `Provider` (`provider.go:41`); impls in
  `LibdnsAdapter` (`libdns.go`, covers 7 providers) + `NamecomProvider`
  (`namecom.go`). Added `toFQDN` helper.
- ✅ apitypes `DNSRecordResp` + `ZoneRecordsResponse`; tygo regenerated
  (`generated-types.ts:433`).
- ✅ `GET /api/v1/zones/records?zone=` → `handleAPIZoneRecords`
  (`handlers_dns_records.go`), route at `server.go`. `Managed` = declared in
  `Zone.Records`. Module builds clean.
- UI consumer is Phase 4.

### Phase 2 — Write path: set-based CRUD + drift-safe publish ✅ (backend)
- ✅ `POST /api/v1/zones/records/{add,edit,delete}` → `applyRecordMutation`
  (`handlers_dns_records.go`). Shared `recordMutation` req {zone,name,type,value,
  oldValue,ttl,expectedFrom}.
- ✅ Drift guard: live values for (name,type) must equal `expectedFrom` (multiset)
  else 409 `{drift:true, live:[...]}`. This is the "from→to must match" rule.
- ✅ Publish anchored to LIVE set + delta → unmanaged siblings preserved (TTLs
  too). Empty set → `DeleteRecord`. Config `Zone.Records` updated in step.
- Helpers: `mutateRecordSet`, `applyRecordToConfig`, `valueSetsEqual`. Builds+vets.

### Phase 3 — Sync reconciliation ✅ (backend; UI banner delegated)
- ✅ State: `Config.LastPublishedRecords map[string][]string`, `DNSDriftBlocked`,
  `DNSDriftDetail *DNSDriftInfo` — all local-only, pinned in `mergeRemoteIntoLocal`.
- ✅ Drift-checked publisher `dnsSyncRun.publish` (per-run live cache) + pure
  `classifyDrift(live,expected,desired)` → noop/publish/drift. On drift: sync-back
  baseline to live, set block+detail, ntfy alert, abort run (`errDNSDriftBlocked`).
- ✅ Block gate on ALL sync entrypoints: `handleAPISyncDNS`, `handleAPISyncAllDNS`,
  `applyRecordMutation` (CRUD), and the legacy `syncServices` Route53 block.
- ✅ `GET /api/v1/dns/drift` (status) + `POST /api/v1/dns/drift/clear`. apitypes
  `DNSDriftStatusResponse`/`DNSDriftInfoResp`; tygo regenerated.
- ✅ Tests: `classifyDrift` (10 cases incl. multi-value drift), `driftKey`,
  `replaceLiveSet`. All pass. Build green.
- ◐ UI banner (drift detail + "Accept live & resume" clear button) — delegated.
- ⚠ Still cannot verify end-to-end here (no live provider/fleet). Needs a real
  drift round-trip + a two-peer test before trusting on HA. Watch: service-A
  "desired" changes with public IP — first publish seeds LastPublished so an IP
  change reads as `driftPublish`, not `driftDrift` (see classifyDrift first-run
  branch). Confirm on a real IP rotation.

### Phase 4 — Frontend records manager ✅
- ✅ "DNS Records" section on `domains.tsx`, collapsible per zone (fetch on expand
  only — hits live provider). Records grouped by (name,type), per-value rows,
  managed/unmanaged chip, TTL, edit/delete. Add dialog: name/type(TXT default,
  A/AAAA/CNAME)/value(multiline)/TTL(300).
- ✅ Hooks in `hooks.ts`: `useZoneRecords`, `useAddRecord/useEditRecord/
  useDeleteRecord`; `expectedFrom` computed from rendered group. zod schemas +
  type re-exports.
- Deviation: mutations invalidate on `onSettled` (not `onSuccess`) so a 409 drift
  refreshes the live set → next retry has correct `expectedFrom`. Error text shows
  via existing Alert pattern.
- ⚠ Unverified end-to-end: no live DNS-provider creds in this env; logic is
  unit-tested, UI + Go compile, but a real add-TXT round-trip hasn't been driven.

### Phase 5 — Tests ◐
- ✅ Go unit tests for pure mutation logic (`handlers_dns_records_test.go`):
  mutateRecordSet add/edit/delete (multi-TXT sibling preservation, empty-set,
  errors), applyRecordToConfig (+unmanaged-adopt, trailing-dot), valueSetsEqual,
  recordMatches. All pass.
- ◻ still: drift-on-sync tests (Phase 3), ListRecords per-provider (needs fakes),
  handler-level integration. Confirm `make` green after UI lands.

## Optional extensions (out of scope now)
- Record TTL editing niceties, MX/SRV/CAA typed forms, bulk import.
- Real type enum (currently free-form string).

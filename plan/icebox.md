# Icebox — deferred, opt-in next steps

How this plan works: see `/home/nthalk/CLAUDE.md` "Planning". These are queued, not active.

## Port exclusions in config + portmap "Exclusions" tab (queued 2026-07-21)
**Goal**: move the port denylist from a hardcoded CLI list into server config so it's authoritative
and editable — clients running `hz ports next` (a.k.a. "port free") can't land on 8000/3000/etc.

**Current state**: exclusions are hardcoded in `cmd/hz/ports.go` — `commonPorts` (map incl. 3000, 8000,
8300, 9000, …) + `commonRanges` (`{3000,3010},{8000,8099},{9000,9099},…`), applied by `isCommonPort`,
`findFreeRange`, `suggestFree`. Safe band is 20000–32767. This lives only in the CLI binary, so it's not
centrally editable and a different/older client could ignore it.

**Design**:
- Config: `Config.PortExclusions` — e.g. `{ Ports []int, Ranges []PortRange{From,To int}, Note string }`,
  or a compact `[]string` of `"3000"` / `"8000-8099"`. Seed with today's hardcoded defaults on first run
  (migrate `commonPorts`/`commonRanges`) so behavior is preserved and now editable.
- API: surface exclusions on `GET /api/v1/ports` (new field) so the CLI reads them; add
  `PUT /api/v1/ports/exclusions` (admin) to edit. Server stays authoritative.
- CLI: `findFreeRange`/`suggestFree` consume the server-provided exclusions. Keep a minimal hardcoded
  fallback only if the field is absent (older server), else drop the hardcoded list.
- UI: portmap page gains an **Exclusions** tab — view/add/remove excluded ports + ranges, with the note
  field. Show them alongside the derived reservations.

**Blocking decisions (user owns)**:
- Config authoritative + seeded defaults (drop CLI hardcode), OR hardcoded baseline + config as additions?
  Recommend authoritative-with-seed.
- Exclusion shape: explicit ports+ranges, or string patterns like `8xxx`/`XX00`? The user mentioned
  "8xxx, any common XX00" — decide whether to support wildcard patterns or just enumerated ranges.

**Risks**: changing allocation could strand existing services already on now-excluded ports (exclusions
apply to *new* allocation only — never evict). `ports list` should flag any reserved port that sits in an
exclusion as "grandfathered".

## Hosts UX clarity on the Observability page (queued 2026-07-21, user feedback)
**Problem**: the Hosts section only lets you add *declared* hosts, and doesn't make clear that *derived*
(port-map) hosts are ALWAYS known and already covered by a `hosts:['*']` port rule. User read it as "why
can't I add the known hosts" → confusing.
**Fix**: unify the Hosts view — list every known host (`knownHosts` = derived ∪ declared), each badged
**derived** or **declared**, showing labels. On a derived host, a one-click **Declare / add labels**
action prefills its IP into the host form (no retyping) so you can attach a name/labels. Copy: "Derived
hosts come from the port map and are already scannable; declare one only to label it, or add a host hz
doesn't route to." Fold into the Ports/portmap UI pass.

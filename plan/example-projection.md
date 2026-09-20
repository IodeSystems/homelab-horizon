# Example projection — the model, populated

> A worked instance of the model in [architecture.md](architecture.md), for
> designing against. **Shape and cardinality, not an inventory.** Names are
> placeholders: homelab-horizon is a public repo, so no real hostnames,
> domains, IPs or addresses appear here. The structure is what matters and the
> structure is real.
>
> Every awkward state the UI has to render is present on purpose. A design that
> handles all of §4 is done; one that handles only the happy path is not.

## 1. The tree

```
acme-co                                    root project — carries the package feed
│   feed: <registry-host>/debian  noble/main  key <fingerprint>
│
├── intern                                  posture prod · 1 environment
│   └── prod
│         git         → gw-1:3000       2 domains
│         idp         → gw-1:8080       1 domain
│         registry    → gw-1:3000       (same backend as git, different domain)
│
├── storefront                              3 environments — the mature one
│   ├── dev       posture dev      version —          (local only, no machine)
│   ├── staging   posture staging  version 1.4.2      from: dev
│   └── prod      posture prod     version 1.4.0      from: staging   ⚠ no machine
│
├── analytics                               2 environments
│   ├── beta      posture staging  version 0.9.1
│   └── prod      posture prod     version 0.9.0      from: beta
│
├── client-a     posture prod · 1 environment (prod)   version 2.1.0
├── client-b     posture prod · 1 environment (prod)   version 1.0.7
└── client-c     posture prod · 1 environment (prod)   version 3.2.2
```

**Most projects have exactly one environment.** That is the common case, not a
degenerate one — the UI must not make a single-environment project look
unfinished. Only `storefront` has a full ladder.

**`client-*` call their one environment "prod" while sitting at staging's
isolation level.** Name and posture are separate fields for exactly this
reason. Do not render them as one thing.

## 2. Segments

One segment per project that owns machines. The hub is `gw-1`, which is hz
itself.

```
seg:intern        10.10.1.0/24    gw-1
seg:storefront    10.10.2.0/24    gw-1 · app-1 · app-2
seg:analytics     10.10.3.0/24    gw-1 · an-1
seg:people        10.10.9.0/24    gw-1 · laptops, phones  ← the human-access VPN
                                        one segment among many, not "the" VPN
```

## 3. Machines

```
gw-1        segments: ALL (it is the hub)        agent 0.5.1   reported 40s ago
            hosts:  intern/prod/git/app          on :3000
                    intern/prod/idp/app          on :8080
                    storefront/staging/web/app   on :6400  ← two projects,
                    storefront/staging/web/next  on :6402     one machine
                    storefront/staging/web/ops   on :6404  ← no service, loopback
                    hz itself                    User=hz, unprivileged

app-1       segments: seg:storefront             agent 0.5.1   reported 12s ago
            hosts:  storefront/prod/web/app      ⚠ desired 1.4.0 · observed 1.3.8

app-2       segments: seg:storefront             agent 0.5.1   reported 6d ago  ⚠ stale
            hosts:  storefront/prod/web/app      observed 1.4.0

an-1        segments: seg:analytics              agent 0.5.0   reported 2m ago
            hosts:  analytics/prod/api/app

ci-1        segments: seg:intern · seg:storefront   ⚠ MULTI-HOMED
            forwarding: DENIED between own interfaces
            declared:   "publishes packages, deploys storefront"
            joined seg:storefront 2026-09-18, in person, by <operator>

new-box     ⏳ pending approval
            fingerprint  abcd-efgh-ijkl-mnop     requested 8m ago
            wants:  storefront/staging/web/app
```

## 4. Every state the UI must render

This is the design brief. Each row exists somewhere in §1–3 above.

| state | where | why it is hard |
|---|---|---|
| project with one environment | `client-a` | must not read as incomplete |
| project with a full ladder | `storefront` | the only one; don't design only for this |
| environment declared, no machine | `storefront/prod` | real today; it has a posture and no placement |
| name ≠ posture | `client-*` prod@staging | two fields, two meanings, one row |
| one machine, two projects | `gw-1` | machine has no project; instances do |
| instance with no service | `…/web/ops` :6404 | loopback-only; needs no domain |
| two backends, one service | `web/app` + `web/next` | slots; one is active |
| two services, one backend | `git` and `registry` | different domains, same process |
| version drift | `app-1` 1.4.0 vs 1.3.8 | the normal state mid-rollout, not an error |
| stale report | `app-2`, 6d | silence ≠ healthy ≠ broken; three states |
| multi-homed machine | `ci-1` | the bridge case; must be visible and explained |
| pending approval | `new-box` | fingerprint must be readable aloud |
| service with no project | (any legacy row) | explicitly legal; sorts last |
| agent version skew | `an-1` 0.5.0 | machines lag; not a failure |

**Three states that look alike and are not:** *healthy*, *reporting nothing*,
and *reporting a problem*. `app-2` is the trap — it last spoke six days ago, so
its "observed 1.4.0" is a memory, not a fact. Anything showing an observed
value must show its age beside it or it lies.

## 5. What the projection produces for one machine

`project(global, "app-1")` — pure, diffable, the thing the agent applies:

```json
{
  "machine": "app-1",
  "serial": 47,
  "segments": [
    { "name": "seg:storefront", "interface": "wg-storefront",
      "address": "10.10.2.11/24", "peers": ["gw-1"] }
  ],
  "forwards": [],
  "hosts":    [ { "name": "<gateway-host>", "address": "10.10.2.1" } ],
  "packages": [ { "name": "storefront", "version": "1.4.0", "hold": true },
                { "name": "hz-agent",   "version": "0.5.1", "hold": true } ],
  "units":    [ { "name": "storefront@app.service", "enabled": true } ]
}
```

The UI's most valuable single screen is the **diff** of this against what the
machine last reported — desired minus observed, per machine, before anything
is applied.

## 6. Cardinality summary

```
project      1 ─── n  subprojects          tree, no inheritance except the feed
project      1 ─── n  environments
project      1 ─── 1  segment              (projects that own machines)
machine      n ─── n  segments             usually 1; >1 is flagged
machine      1 ─── n  instances
instance     n ─── 1  environment
instance     n ─── n  services             often 1; sometimes 0
service      1 ─── n  backends             one active
```

Nothing here is 1:1. Any screen that assumes it will be wrong at `gw-1`.

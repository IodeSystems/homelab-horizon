# The project coordinate — making the config address `<project>/<environment>/<app>/<role>`

> **Status: IMPLEMENTED 2026-09-22** on `feat/project-coordinate`, off `dev` at
> `a26688c`. The `hz cm` → `hz config` rename landed first, as this plan
> required. Written 2026-09-22 against `dev` at `96fef0c`; every claim below was
> checked against the code, and the three verification questions were answered by
> a throwaway test in `package configmgr` that was run and then deleted (its
> output is transcribed in §1). That throwaway is now PERMANENT, as
> `configmgr/project_binding_test.go`.
>
> **The freeze check (§6, stage 1) passed.** Measured on the office gateway
> before stage 3: `schema version 10`, and `cm_machines`, `cm_registrations`,
> `cm_configs`, `cm_config_values` and `cm_current_keys` **all zero rows**. The
> config manager has never run there. Combined with §1 Q3's five other items,
> the "there is nothing to migrate" claim is closed, not merely likely — so
> migration `0013` destroys no capability grant that ever existed.
>
> **The positive control, measured against the OLD binding before any code
> changed**, so that the refusal below is a change and not a tautology:
>
> ```
> CONTROL: cross-project read SUCCEEDED, plaintext "acme's production password"
> CONTROL: cross-project unwrap SUCCEEDED, key id 2d65328724b33f88
> ```
>
> One environment key installed at two projects' identical `prod/redline/app`
> opened both a kind 0x01 value and a kind 0x02 grant. Under the four-field
> binding the same two operations fail with `envelope failed authentication`;
> `configmgr.TestCrossProjectReadIsRefused` and
> `TestCrossProjectGrantIsRefused` are that, permanently.
>
> **Where the design met the code and was wrong, or incomplete** — three places,
> all recorded in the implementing commit:
>
> 1. **`config.RecoveryWrap` needed more than `Project` + `Addr()`.**
>    `Config.FindRecoveryWrap` and `Config.PutRecoveryWrap` key on
>    `(environment, app, role, key id, recipient)`, so without the project two
>    projects' rungs of the same name share one wrap entry and a backfill for
>    one reports coverage for the other. Both now key on the project. §2 named
>    the struct and the method but not the two accessors.
> 2. **`apitypes.CMRecoveryWrap` / `CMRecoveryWrapReq` were not in §2's list**
>    and needed the field, along with `handleAPICMRecoveryWraps`' required-field
>    check.
> 3. **The promotion gate's `--project` is now a cross-check, not a selector.**
>    §6 said to keep it as a narrowing hint. With the project on the source
>    config's address that would be a second source of truth able to disagree
>    with the first, which is the defect one layer down, so the gate reads
>    `src.Project` and **refuses** a `project=` that disagrees rather than
>    obeying it. A promotion cannot change project — `cmPromotionEdge` already
>    looked the source rung up inside the target's project — so there is nothing
>    the parameter could legitimately select.
> 4. **`cmApprove` was not in §2's CLI list, and it is the one that mattered
>    most.** `cmd/hz/cm.go`'s approve path builds the `EnvKeyAddr` it wraps the
>    environment key to, and it built a three-part one. The compiler cannot see
>    that — a zero-value `Project` is a legal string — so the wrap would have
>    been produced, hz would have relayed it, and the box would have refused to
>    unwrap at boot with an authentication failure naming nothing: the whole
>    typed-fingerprint ceremony spent on a blob that opens for nobody. Found by
>    a test, not by review. `cmPending`'s address column had the same omission,
>    with a cosmetic rather than a cryptographic consequence.
> 5. **Two UI defects surfaced while threading the field through.** `CMPromote`'s
>    copy-box printed `hz config push <target>/<app>/<role>` — already wrong
>    before this change, and now a command `parseCMAddr` refuses. And its
>    "seen environments" suggestion list pooled environment NAMES across every
>    project, which becomes actively misleading once names are not globally
>    unique: it would offer another project's rung as a promotion target. Both
>    fixed.
>
> Everything else in §2's inventory was accurate, including the two it warned
> would be easy to get wrong: `RegistrationsHoldingStaleKey`'s self-join needed
> the project on BOTH sides, and `unitName` kept its THREE-part instance part.
>
> **The generalisable lesson.** Items 1, 4 and 5 are all the same shape: a place
> where an address was assembled field by field rather than passed as a value,
> so adding a field to the struct left the assembly site silently short. Nothing
> in the type system catches it — every field is a `string` and the zero value
> is legal — and three of the four were found by tests rather than by reading
> §2's inventory. An audit that greps for the TYPE name finds them; one that
> greps for the field names does not.

## 0. The problem, stated exactly

The config manager's address is `(environment, app, role)`. An environment
belongs to exactly one project, and environment names are unique **per project**,
not globally — `internal/config/config.go:696` says so in the type itself:

```go
// envKey is the identity of an environment: names are unique per project, not globally,
// because every project gets to have a "prod".
type envKey struct{ project, name string }
```

`plan/example-projection.md` §1 has six projects declaring an environment called
`prod` (`intern`, `storefront`, `analytics`, `client-a`, `client-b`, `client-c`).

What carries the project today is the **app** coordinate, implicitly and by luck:
`prod/redline/app` is an identity only while exactly one project declares a
service called `redline`. `internal/projection/projection.go:668` is where that
luck is cashed in — `resolveEnvironment` maps `inst.App` → `Service.Project`,
falls back to a globally-unique environment name, and when both roads close emits
a gap naming `hz service assign`. `internal/server/handlers_version_drift.go:167`
calls the same function through an export that exists only so the two callers
cannot disagree about which project `prod/web/app` is on.

There are already two ad-hoc patches, on two independent surfaces:

- **The promotion gate.** `configmgr.QueryProject` (`configmgr/types.go:278`,
  "Optional, and required only when it is ambiguous"), read at
  `internal/server/handlers_configmgr.go:1394`, threaded through
  `cmPromotionEdge(sourceEnv, targetEnv, project)` (`:1408`), returned on
  `CMPromotionEdgeResp.Project` (`internal/apitypes/types.go:2089`), sent by
  `hz config promote --project` (`cmd/hz/cm_config.go:305,339`).
- **The drift row.** `apitypes.InstanceVersion.Project`
  (`internal/apitypes/version_drift.go:188`) already exists as a field — it is
  populated by the backwards resolution, not carried on the address.

**And the model already says the address is four-part; only the code disagrees.**

- `plan/architecture.md:83` — `Instance (project, environment, app, role) — the config address`
- `plan/ui-redesign.md:81` — "An **instance** is `(project, environment, app, role)`"
- `internal/db/migrations/0011_observed_version.up.sql`, header comment — "An
  instance address is (project, environment, app, role)", written over a schema
  whose columns are three.

So this is not a new idea being introduced. It is the schema, the wire and the
keystore catching up to what three documents and one migration header already
assert.

---

## 1. Verdicts on the three verification questions

A throwaway `configmgr/zz_projcoord_verify_test.go` was written in-package (so it
could call the unexported `context()`, `canonicalContext`, `aad`, `mustGCM` and
`openFrom`), run, and deleted. Its output is quoted verbatim.

### Q1 — Is the environment key per-address, not per-environment? **CONFIRMED.**

Key material is minted fresh per address and nothing derives one address's key
from another's:

- `cmd/hz/cm_key.go:90` — `cmKeyNew` calls `configmgr.NewEnvKey()` once, per the
  `<environment>/<app>/<role>` positional it was given.
- `configmgr/keystore.go:21` — the tree is
  `<root>/secrets/keys/<environment>/<app>/<role>/<label>.<keyid>.key`, and
  `addrSegments` (`keystore.go:643`) builds exactly those three segments.
- No code path copies a key between addresses. `Put` refuses to overwrite
  (`keystore.go:420`, `O_EXCL`), and `import` is the only other writer.

```
CLAIM-1 HOLDS: NewEnvKey mints fresh material per call (b872cdda7eb2c2f4 vs 3cc75d305d8a5b47)
```

**So two projects' `prod` do not share a key, and you were right: there is no
standing cross-project decryption risk today.** That lowers the urgency exactly as
you suspected it might.

**But the corollary is real and worth naming.** `hz config key import` exists
precisely so one key can be installed at several addresses (`cm_key.go:369` —
stdin, any address). The moment an operator does that — or two projects
legitimately name an app the same thing and one key is pasted twice — the AAD is
the only thing left, and the AAD does not name the project:

```
CLAIM-1 COROLLARY: one imported key + one identical triple = cross-project read,
plaintext "acme's production password"
```

That is a hazard the design is supposed to refuse structurally, not a hazard it
currently has. Verdict: **not urgent, but the binding is missing.**

### Q2 — Does the AAD bind the address? What would re-addressing invalidate? **CONFIRMED, and it invalidates every kind 0x01 and 0x02 envelope, not only the wraps.**

`Addr.context()` (`crypto.go:355`) = `canonicalContext(labelAddr, Environment, App, Role, Key)`;
`EnvKeyAddr.context()` (`crypto.go:392`) = `canonicalContext(labelEnvKeyAddr, Environment, App, Role)`;
`MachineAddr.context()` (`crypto.go:407`) = `canonicalContext(labelMachineAddr, Machine, Key)`.

The context is the tail of the AAD for every seal and open
(`crypto.go:514, 532, 554, 565, 582, 588`). Two identical triples produce
byte-identical context bytes:

```
CLAIM-2a context(prod/redline/app#DB_PASSWORD) =
  00 00 00 11 "hz-config/v1 addr" 00 00 00 04 "prod" 00 00 00 07 "redline"
  00 00 00 03 "app" 00 00 00 0b "DB_PASSWORD"
CLAIM-2a HOLDS: a value sealed at project A's prod/redline/app opened as project B's
```

Each existing field is genuinely bound — change any one and the open refuses:

```
CLAIM-2b HOLDS: each of environment, app, role and key name is bound;
a change to any one refuses at open
```

Adding a field is therefore a **flag day**, exactly as `configmgr/doc.go:161`
already warns ("Changing an envelope's additional data is a flag day: every blob
minted under the old rule stops opening under the new one"). Measured, with the
correct key in hand and only the context changed:

```
CLAIM-2c HOLDS (kind 0x01): old ciphertext + correct key + new 4-field context
  => cipher: message authentication failed
CLAIM-2c HOLDS (kind 0x02): old wrapped env key + correct private key + new context
  => envelope failed authentication
CLAIM-2d HOLDS: kind 0x03 (machine-scoped) has no address field the project change
  touches; it survives untouched
```

**Precise answer to "what would re-addressing invalidate":**

| artifact | kind | invalidated? |
|---|---|---|
| every sealed config value (`cm_config_values.ciphertext`) | 0x01 | **yes** |
| every wrapped environment key on a registration (`cm_registrations.wrapped_env_key`) | 0x02 | **yes** |
| every recovery wrap (`config.json` `RecoveryWrap.Wrapped`, `internal/config/recovery.go:71`) | 0x02 | **yes** |
| every machine-scoped secret (`cm_machine_secrets.ciphertext`) | 0x03 | **no** — addressed by `(machine id, key name)` |
| the key MATERIAL itself (`~/.hz/secrets/keys/…/*.key`) | n/a | **no** — a key file holds key text, `created_at` and a label; no address |
| `cm_current_keys` rows | n/a | not ciphertext; the row's ADDRESS is stale, the key id it names is still valid |

So: **not "only the wraps", and not "nothing" — every sealed value and every
wrap.** Machine-scoped secrets and the key material survive.

### Q3 — How much sealed data actually exists? **You are right. On this estate: none that matters, and probably none at all.**

Evidence, in descending strength:

1. **There is no keystore on this machine.** `ls ~/.hz` → `No such file or
   directory`; `find "$HOME" -name '*.key' -path '*secrets*'` → empty. hz
   never holds an environment key (`configmgr/doc.go:20`, `keystore.go:24` — "this
   tree is the only place one lives"), so **no environment key exists on the
   development machine at all.** Nothing here has ever sealed anything.
2. **There is no local hz state.** `/var/lib/homelab-horizon/` (`internal/db/db.go:29`,
   `DefaultPath`) does not exist. No `hz.db`, so no `cm_*` rows.
3. **redline does not import `configmgr`.** `go.mod` pins
   `github.com/iodesystems/homelab-horizon v0.3.0`, and
   `grep -rl 'homelab-horizon/configmgr' --include=*.go` over the redline tree
   returns nothing. The first intended client has not started using it. This
   independently confirms `plan/architecture.md`'s own "Not true yet" row
   ("redline still ships plaintext secrets … `configmgr` not imported").
4. **The one recorded run was on a throwaway VM.** `plan/architecture.md:377` —
   "Ran end-to-end once on `redline-virgin` 2026-09-19". `plan/config-manager.md:3`
   says the same. A multipass VM; its disk is not a system of record.
5. **Key custody has never been run for real.** `plan/architecture.md`'s path,
   Phase 1 step 2: "**The tooling landed 2026-09-20**; what remains is running it
   on the real box — `keygen` → `add` → `backfill` → **`verify`**. Not done until
   `verify` says yes." So there are no recovery wraps to invalidate either.
6. **Rotation has never run** (`plan/config-manager.md:22` — "rotation has no
   re-wrap path at all"), so no address holds a second key whose re-wrap would be
   lost.

**What I could not verify from here, and will not pretend to:** the office
gateway's own `hz.db`. It is a live machine on a network this session cannot
reach, and reading it is the operator's action, not mine. Items 1–5 make it very
unlikely to hold cm rows — the gateway runs `main`, which does contain
`configmgr`, so the *code* is there; what is absent is any client that would have
registered against it and any key material anywhere to seal with. **Before
executing stage 3, run the freeze check in §6 on the gateway and record the
output.** That is the one thing standing between "almost certainly nothing" and
"nothing".

**Verdict: this is genuinely the last cheap moment, and the claim is not
overstated.** See §7 for what it costs after `redline` imports `configmgr`.

---

## 2. The address as it would be

```
<project>/<environment>/<app>/<role>#<key>
acme/prod/redline/app#DB_PASSWORD
```

`project` is the fourth coordinate, first in the path, first in the AAD.

**Where the client gets it: compiled in, beside the app name.**
`configmgr.Options` (`client.go:152`) today says "Environment is a launch flag
(`--env`), App is compiled in, Role is the process's own launch flag". Project
joins App as a compiled-in constant, and that is not a convenience — it is what
makes binding it worth anything. `projection.unitName`'s doc comment
(`projection.go:554`) already establishes that **the project IS the package name**,
and a package ships one binary, so the project is exactly as compile-time as the
app name. The agent therefore knows its project independently of hz, which is the
test `configmgr/doc.go:157` applies to every bound field ("the agent knows its own
environment, app and role from its own argv, so it is not feeding back a field hz
chose for it").

### What has to change

**Crypto / AAD** (`configmgr/crypto.go`)
- `Addr` gains `Project`; `context()` → `canonicalContext(labelAddr, Project, Environment, App, Role, Key)`.
- `EnvKeyAddr` gains `Project`; `context()` → `canonicalContext(labelEnvKeyAddr, Project, Environment, App, Role)`.
- `Addr.String()` / `EnvKeyAddr.String()` become four-part.
- `Addr.EnvKeyAddr()` carries `Project` through.
- `MachineAddr` **unchanged**. A machine id is globally unique; a project coordinate there would be a field with no referent.
- The labels stay as they are. They are already disjoint from each other, and the field COUNT changes with the field list, so no new label is needed to keep the three encodings distinct.

**`configmgr/doc.go`** — the "Binding a ciphertext to its address" section, the
per-kind context rows, and the WebCrypto interop paragraph all spell the field
lists out. All three must be restated, because the browser reproduces them byte
for byte and a stale doc there is a wrong implementation.

**Keystore** (`configmgr/keystore.go`)
- Tree becomes `<root>/secrets/keys/<project>/<environment>/<app>/<role>/<label>.<keyid>.key`.
- `addrSegments` (`:643`) validates and appends four segments.
- The header comment at `:21` and `cmd/hz/cm_key.go:97`'s printed path.
- `cmd/hz/cm_key.go:232` `cmKeyAddresses` gains one nesting level.

**Client state** (`configmgr/state.go`)
- `addrDir` (`:373`) gains a `checkName("project", …)` and a fourth path element.
- `Addresses()` (`:342`) gains a loop level.

**Machine protocol** (`configmgr/types.go`)
- `RegisterRequest.Project`, `ConfigRequest.Project`, `BlessRequest.Project`,
  `BlessResponse.Project`, `CurrentKeyPointer.Project`.
- `RegisterRequest.EnvKeyAddr()` and `ConfigRequest.Addr(key)` carry it.
- `QueryProject` (`:278`) stops being "optional, required only when ambiguous" and
  becomes a structural parameter of every cm call. Its comment is rewritten.
- `configmgr/push.go`: `PushOptions.Project` (`:136`), the two `Addr{…}`
  constructions (`:248`, `:260`), the required-field check (`:301`), and the
  `QueryEnv/App/Role` query set (`:329`).
- `configmgr/client.go`: `Options.Project` (`:158`), and the three places that
  build an address from it (`:218`, `:242`, `:286`, `:492`).

**Database** (new migration `0013_project_coordinate.up.sql`)
- `cm_registrations`: `project TEXT NOT NULL` with the same
  `GLOB '[a-z0-9]*' AND NOT GLOB '*[^a-z0-9_-]*'` CHECK; `UNIQUE (machine_id,
  project, environment, app, role)`; `idx_cm_registrations_address` becomes
  `(project, environment, app, role)`.
- `cm_configs`: same column and CHECK; `idx_cm_configs_address` becomes
  `(project, environment, app, role, seq)`.
- `cm_current_keys`: same column and CHECK; `PRIMARY KEY (project, environment,
  app, role)`.
- `cm_machines`, `cm_machine_secrets`, `cm_config_values`, `cm_secret_reads`:
  **no schema change.** (Optional, recommended: `cm_machines.enrolled_project`
  nullable, informational, for the same "what did the box CLAIM" reason
  `enrolled_environment` exists — `0009…up.sql` lines 137–152. Not required.)
- SQLite forces a table rebuild anyway: a column inside a UNIQUE/PRIMARY KEY
  cannot be bolted on, and every one of these tables names a column in a CHECK.
  0009 already had to restate everything for that reason and explains it at the
  top of the file; 0013 follows the same shape and inherits the same "after this
  migration, THIS file is the current schema" consequence.

**Data layer** (`internal/db/configmgr.go`)
- `canonAddress(environment, app, role)` (`:104`) — the fold-and-validate choke
  point. Gains `project`; every caller below goes through it.
- Nine functions gain a leading `project string`: `UpsertRegistration` (:377, and
  its `ON CONFLICT (machine_id, environment, app, role)`), `RegistrationAt` (:454),
  `CreateConfig` (:839, including the open-ended-range uniqueness check at :898),
  `ListConfigsForAddress` (:954), `TombstoneValueAtAddress` (:1027),
  `ResolveConfig` (:1116), `CurrentKeyFor` (:1529), `SetCurrentKey` (:1557, and its
  `ON CONFLICT`), `RegistrationsHoldingStaleKey` (:1592 — note its self-join at
  :1607 compares `c.environment`/`c.role` and must gain the project on both sides).
- The `Registration` (:312), `Config` (:787) and `CurrentKey` (:1516) row structs.

**Admin wire** (`internal/apitypes/types.go`) — `CMRegistrationResp` (:1851),
`CMCreateConfigReq` (:2009), `CMConfigResp` (:2033), `CMCurrentKeyReq`/`Resp`
(:2116) gain `Project`. Two already have it and only change provenance:
`CMPromotionEdgeResp.Project` (:2089) goes from "the project we worked out" to
"the project you named"; `InstanceVersion.Project`
(`internal/apitypes/version_drift.go:188`) goes from derived to carried.

**The shape guard** — `internal/server/configmgr_shapes_test.go`
(`TestBlessShapesMatchAPITypes`) pins `configmgr`'s bless types against
`apitypes`' duplicates (`configmgr/types.go:163–179` explains the deliberate
duplication). It must be updated in the same commit as the shapes, or it fails —
which is the point of it.

**Routes** (`internal/server/server.go:1221–1248`) — **no route changes.** The
address travels in query parameters and bodies, not in paths. Handlers that gain
a `project` read: `handleAPICMRegister` (:182 trims, :188 requires, :252
`UpsertRegistration`), `handleAPICMRegisterPoll` (:346, :367),
`handleAPICMConfigs` (:1050), config create (:1083, :1194), `handleAPICMResolve`
(:1299), `handleAPICMCurrentKey` (:1492), plus the DTO mappings at :560, :1246,
:1544 and the machine-removal preview's address strings at :1001.
`cmOtherEnvironment` (:486) compares `reg.App`/`reg.Role` across environments to
raise the name-squatting signal — with a project it should compare across
`(project, environment)`, which makes the signal sharper rather than changing it.

**Config model** (`internal/config/recovery.go`) — `RecoveryWrap.Project` (:72)
and `RecoveryWrap.Addr()` (:91).

**Projection** (`internal/projection/projection.go`) — `Instance.Project` (:294).
`Instance.Address()` (:304) becomes four-part and its "No project" note is
deleted. **`unitName` (:576) keeps escaping the THREE-part instance part** —
`<project>@systemd-escape(<environment>/<app>/<role>).service` — or the project
appears twice in every unit name; see `projection.go:554`.

**CLI** (`cmd/hz/cm*.go`, post-rename)
- `parseCMAddr` (`cm.go:89`) takes four parts;
  `keyAddr`/`valueAddr`/`addrOfConfig` (`cm.go:122–133`); the query set in
  `cmCurrentKey` (`cm.go:150`).
- `cm_key.go` — all five subcommands call `parseCMAddr` (`:70, 171, 295, 383, 445`);
  usage strings at `:68, 162, 293, 381, 443`; the printed keystore path at `:97`;
  `cmKeyAddresses` (`:232`) gains a nesting level.
- `cm_config.go` — `cmResolve` (`:196, :201`), `cmOpenValues` (`:153, 166, 169`),
  `cmPromote`'s `dstAddr` (`:331`) and the re-seal `valueAddr` (`:446`).
- `cm_recovery.go` — `parseCMAddr` (`:602`), `keyAddr` (`:420, 508`),
  `cmWrapToRecovery` (`:721`), usage text at `:126, 186, 385, 555, 600`.
- `cmd/hz/main.go` — the command list at `:83–91` and `:154`, and the CONFIG
  MANAGER / KEY CUSTODY blocks at `:227` (whose `recovery verify prod/app/role`
  example is *already* wrong — a three-part address written with two segments).

**UI** (`ui/src/`)
- `components/CMMachines.tsx` — the local `Addr = { environment, app, role }` type
  (`:32`) and `addrString` (`:35`); uses at `:94, 284, 352`; the help text at
  `:386` quoting a `hz … key current <env>/<app>/<role>` invocation.
- `components/CMConfigs.tsx` — the local `CMAddress = { env, app, role }` (`:34`),
  `emptyCMAddress` (`:36`), `isCompleteAddress` (`:39`), `addressString` (`:43`),
  the three form fields (`:133–152`), and the comparisons at `:387, 399, 422, 466, 503`.
- `components/CMApprovals.tsx` — the address join at `:55`.
- `api/generated-types.ts` is tygo output and regenerates.

**Test surface, for sizing.** Roughly 300 address-construction sites across
~22 test files; the heavy ones are `internal/db/configmgr_test.go` (~130
occurrences), `configmgr/crypto_test.go` (~31), `cmd/hz/cm_test.go` (~28),
`internal/projection/projection_test.go` (~18), `cmd/hz/cm_recovery_test.go` (~17),
`internal/server/handlers_cm_recovery_test.go` (~16), `cmd/hz/cm_config_test.go`
(~16), `configmgr/state_test.go` (~15). Mechanical, but it is the bulk of the
diff — budget for it.

**Confirmed out of scope.** `internal/agent`, `hzclient` and `hzapi` carry no cm
address and import no `configmgr` type. The agent reconciles a
`projection.MachineConfig`; `hzclient` is the deploy/ban/maintenance verbs;
`hzapi` is the version handshake. None of them changes.

---

## 3. The migration, concretely

### A row that has no project

**There is no correct value to backfill.** The project is derivable only from
`config.json`'s `Services` (`svc.Name == app → svc.Project`) — which is (a) the
exact backwards resolution this change exists to delete, (b) one-to-one only while
no two projects name an app the same, and (c) in a **different store**: the
config lives in `config.json`, the registrations in `hz.db`, and a SQL migration
cannot read across them.

So 0013 does not guess. Per table:

- **`cm_registrations` — every row is DELETED.** Each row's `wrapped_env_key` is a
  kind 0x02 envelope whose AAD no longer reproduces (CLAIM-2c). The grant is dead
  bytes, and a registration whose grant is dead is a registration that is not
  approved, whatever the `state` column says. Deleting is the truthful record.
  A box re-registers at its next boot and re-enters `pending`, which is the
  designed response to a new address (`configmgr/types.go:13`). Nothing references
  `cm_registrations.id` — `cm_secret_reads` references `machine_id` and
  `config_id` only — so no evidence is orphaned. The `observed_version` columns
  from 0011 die with the row and are re-reported at the next boot.

- **`cm_configs` — rows are CARRIED FORWARD under the reserved project
  `unmigrated`, and `cm_config_values` are TOMBSTONED.** This is the one place a
  sentinel earns its keep. Deleting the configs would fire `ON DELETE SET NULL`
  into `cm_secret_reads.config_id` and blank the audit rows that name them, and
  0009 already ruled on exactly this trade ("gutting evidence is the worse trade",
  `0009…up.sql` line 75). The bytes are worthless at any address, so dropping them
  costs nothing; the tombstone is the schema's own word for "the row, its key
  name, its binding and its lineage survive; the bytes do not". Concretely:
  `ciphertext = NULL`, `tombstoned_at = CURRENT_TIMESTAMP`, `tombstoned_by = NULL`
  — which the existing CHECK permits. `unmigrated` is a legal segment under the
  charset, so nothing refuses it; it is never a live address because nothing will
  ever register there.

  **Decision recorded:** sentinel + tombstone rather than delete. Reversible in
  the sense that matters — an operator can `DELETE FROM cm_configs WHERE project =
  'unmigrated'` at leisure once the audit trail is no longer interesting.

- **`cm_current_keys` — every row is DELETED.** The row names a key id at an
  address that no longer exists. The key id itself is still valid and still on
  disk; re-publish it with `hz config key current <addr> --set <keyid>` after the
  keystore move. Not carrying it forward under a sentinel, because unlike a config
  it is evidence of nothing — it is a live pointer, and a live pointer at a dead
  address is a trap.

- **`cm_machines`, `cm_machine_secrets`, `cm_secret_reads` — untouched.**

### A keystore tree already on disk

**A `mv`, and nothing else.** A key file holds `{key, created_at, label}` — no
address (`keystore.go:468`). So the material survives verbatim:

```
mv ~/.hz/secrets/keys/<env> ~/.hz/secrets/keys/<project>/<env>     # per project
```

Afterwards, per address:
1. `hz config key ls` — confirm the ids and `created_at` are unchanged.
2. `hz config key current <project>/<env>/<app>/<role> --set <keyid>` — the
   pointer was dropped.
3. `hz config recovery backfill` — **required.** Every existing recovery wrap is a
   kind 0x02 envelope bound to the old `EnvKeyAddr` and no longer opens
   (CLAIM-2c). `backfill` re-wraps every key in the keystore to every registered
   recipient, from the machine that holds them. The old `RecoveryWrap` entries in
   `config.json` must be removed in the same pass, or `hz config recovery ls` will
   report coverage that does not exist.
4. `hz config recovery verify <project>/<env>/<app>/<role>` — a backup nobody has
   restored from is not a backup, and this is the only command that proves it.

**On this estate there is nothing to move.** `~/.hz` does not exist (§1 Q3, item 1).

### Can a pre-migration sealed value still be opened?

**Not by the shipped code: no.** Kind 0x01 and kind 0x02 both refuse at the AEAD
with the correct key in hand — measured, CLAIM-2c.

**By hand: yes, in principle.** The plaintext is not destroyed; it is unreachable
through the new context. Anyone holding the key material and a build of the *old*
`configmgr` can open the old ciphertext, because the old context is still
computable. So a real migration tool is possible — open under the old context,
re-seal under the new, in one process holding the key — and it is the thing that
would have to be built if this change happened later.

**We are not building it.** Not because it is hard, but because there is nothing
for it to migrate (§1 Q3), and a decryption tool nobody needs is a decryption tool
nobody tests and everybody can run.

**Is losing them acceptable now?** Yes: the only sealed values that have ever
existed were on a throwaway VM, and the only environment keys are on a machine
whose keystore does not exist. **Why it will not be acceptable later:** see §7.

---

## 4. What this deletes

The backwards resolution and its gap both become unnecessary. Exactly:

| deleted | where |
|---|---|
| `resolveEnvironment`'s service→project map (the loop over `cfg.Services`) | `internal/projection/projection.go:669–674` |
| its `case 0` fallback to a globally-unique environment name | `:685–693` |
| its `case default` ambiguity error ("app %q is a service of %s") | `:694–696` |
| the 30-line doc comment justifying all of the above | `:639–667` |
| the exported wrapper `ResolveEnvironment` and its 16-line "two callers must not disagree" rationale | `:700–716` |
| the instances gap "cannot be resolved to a project … `hz service assign`" | `:460–461` |
| the drift handler's call through the projection's export | `internal/server/handlers_version_drift.go:167` |
| the prose describing the derivation as the design ("THE APP COORDINATE SUPPLIES THE PROJECT") | `plan/architecture.md:758–778` |

What replaces the function body is one line:

```go
env, err := cfg.LookupEnvironment(inst.Project, inst.Environment)
```

and `handlers_version_drift.go` calls `cfg.LookupEnvironment` directly, because
with the project on the address there is no derivation left for two callers to
disagree about.

**What does NOT get deleted, stated so the claim is not overstated:**

- **`apitypes.VersionDriftUnresolved` survives with a narrower meaning.** Its doc
  comment (`internal/apitypes/version_drift.go:114`) currently says the state
  exists because "a registration's address is environment/app/role with no project
  coordinate". That sentence goes. The state stays reachable for the honest
  remaining reason: the registration names a project or rung hz does not declare.
  The comment must be rewritten, not the constant removed.
- **`apitypes.InstanceVersion.Project` survives** (`version_drift.go:188`). The
  field does not change; only its provenance does — carried on the address rather
  than derived. Nothing in the UI has to move.
- **`config.LookupEnvironment`'s empty-project branch and `ErrAmbiguousEnvironment`
  survive** (`config.go:749–762`). A human still types a bare `--to prod` at
  `hz config promote`, and hz still has to say "which one". What changes is that no
  cm *address* path uses the empty-project form any more.
- **The `hz service assign` command survives.** It joins a service to a project for
  every other reason it exists; it just stops being the config manager's crutch.
- **The unit-name scheme is unchanged** (`projection.go:576`). Its one-unit-is-one-
  instance duplicate gap stays as the standing guard on the input.

---

## 5. Compatibility — an enrolled box sends a three-part address

**Decision: REJECT it, with a 400 naming the missing field.** Not "accept as
project-less", not "refuse to start".

Why:

1. **Project-less is the defect, re-created.** A row with a NULL or empty project
   needs an AAD that encodes an empty project field — a fourth encoding the
   browser and every client must reproduce exactly, for an address nobody can
   promote, route or name. It also makes `project` nullable, which forecloses
   putting it in the PRIMARY KEY of `cm_current_keys` and in the UNIQUE of
   `cm_registrations` — i.e. it gives up the thing the change is for.
2. **"Refuse to start" has the wrong blast radius.** The trigger is a *client's*
   message; hz must not die because one box is old.
3. **Rejection is already the endpoint's shape.** `handleAPICMRegister:188` today
   400s with "a registration needs machine, environment, app, role and version".
   Adding one word to the check and one to the message is the whole change.
4. **A rejected box does not go down; it goes loud.** `configmgr.Client` falls back
   to its last-known-good cache on any unsettled answer (`client.go:121` `ErrUnsettled`,
   `Source == SourceCache`) and logs it. That is the design's chosen failure mode
   and it is the right one here.
5. **The exposed population is zero and is the operator's own.**
   `/api/v1/cm/register` is peer-gated (`cmPeerGate` — WireGuard source address),
   so nothing outside the fleet can send anything; and per §1 Q3 nothing inside it
   is enrolled.

**No protocol version field.** There is none on the wire today, and the presence
of `project` is a sufficient discriminator. A version field would be a second
source of truth about the same fact, able to disagree with it. (Optional
extension, explicitly out of scope: if a second breaking protocol change is ever
needed, that is the moment to add one — not this one.)

---

## 6. Cheaper alternatives, honestly considered

### (A) Keep the three-part address; require globally-unique environment names

The genuinely cheaper option: **no flag day, no migration, no re-registration.**

Rejected, for three reasons in increasing order of weight.

1. **It costs the estate its own naming.** `plan/example-projection.md` §1's six
   `prod` rungs become `storefront-prod`, `client-a-prod`, … — which is the project
   coordinate, spelled as a prefix inside a single field, with no delimiter
   anything can parse and no CHECK that enforces it. `configmgr/doc.go:134` argues
   at length that a length-prefixed encoding exists precisely so a composite field
   cannot be ambiguous; this puts the ambiguity back by hand, one layer up.
2. **It contradicts a declared invariant.** `envKey struct{project, name string}`
   and the separate `Environment.Name`/`Posture` fields exist because "every
   project gets to have a prod" (`config.go:678–698`). Global uniqueness is a new
   validation in `ValidateProjects` that refuses the estate the docs already
   describe.
3. **It does not fix the AAD, which is the actual defect.** `prod/redline/app`
   would be unique by convention, and the context bytes still would not name the
   project — so the CLAIM-1 corollary (one imported key, two projects, cross-read)
   remains reachable. Convention is not a binding. And the day two projects want
   the same environment name anyway, the flag day comes back — by which time there
   *is* sealed data.

### (B) Derive the project server-side; never put it on the wire

hz looks up `Service{Name: app}.Project` at register/resolve and stamps it on the
row. No client change, no re-registration, no flag day.

Rejected, for two reasons, the first of which is fatal.

1. **A field hz derived cannot be bound.** The binding rule the whole scheme rests
   on is that the opener supplies the address from its own context — `doc.go:176`
   spells out the test ("binding only means something for fields the opener knows
   INDEPENDENTLY of the party serving the blob … seq arrives inside hz's answer,
   so an agent could only ever feed back the number hz just sent it —
   authenticating that binds nothing at all"). A server-derived project is
   precisely `seq`. Bind it and you have authenticated hz's own opinion; don't
   bind it and the substitution stays open. Neither branch obtains the property.
2. **The derivation is the thing being deleted.** It is one-to-one only while no
   two projects name an app the same — the identical collision, one layer down.
   Worse, it converts a legible refusal ("hz cannot tell which project's rung this
   is") into a silent stamp of the wrong project, which is the failure mode this
   codebase spends most of its comments avoiding.

**Where (B) looked right, and turned out not to be:** the promotion gate's
`--project` narrowing (`cm_config.go:339` → `handlers_configmgr.go:1394`). The
argument above was that a human typed a bare environment name at a CLI and hz
resolving it is fine, because nothing is sealed against the answer.

**Superseded at implementation.** With the project on the source config's
address, a caller-supplied `--project` is a second source of truth about which
project the promotion is in, able to disagree with the first — the identical
defect one layer down. And it could never legitimately select anything:
`cmPromotionEdge` already looks the SOURCE rung up inside the TARGET's project,
so a promotion cannot change project. The gate therefore reads `src.Project`,
and a `project=` that disagrees is a 400 naming both rather than an override.
The parameter is still read, so a caller sending the wrong one is told instead
of having it silently ignored.

### (C) Do nothing

Honest cost: it works on today's estate (one project per app name) and fails only
when a second project reuses an app name — and the failure is a *gap*, not a wrong
answer, so it is legible rather than dangerous. **The case for acting is not that
it is broken.** It is that the price is currently zero and rises monotonically;
see §7.

---

## 7. Is this really the last cheap moment?

**Yes, and the claim is not overstated — but be precise about what "cheap" means.**

Cheap today = a schema rebuild on a database with no rows worth keeping, plus
deleting 80 lines of derivation. There is no ceremony, no re-key, no fleet.

What the same change costs at each later point on `plan/architecture.md`'s own
path:

| after… | added cost |
|---|---|
| **key custody runs** (Phase 1 step 2 — `keygen`/`add`/`backfill`/`verify`) | every recovery wrap is invalidated (kind 0x02). Recoverable — `backfill` re-wraps from the keystore — but it must be re-run and re-**verified** per address, by the human holding the keystore. Small. |
| **redline imports `configmgr`** (Phase 1 step 3) | every production secret becomes a kind 0x01 envelope. The change now needs an open-old/re-seal-new tool, or a full re-push of every config from a machine holding every environment key. |
| **the first real approval** | every approved box needs a fresh **typed-fingerprint ceremony** per address (`plan/config-manager.md` — a human comparing a 24-hex string on two screens, deliberately un-automatable). That cost is per address and scales with the fleet. |
| **rotation exists** | an address may hold several keys and several wraps; the re-wrap path that "has no re-wrap path at all" today has to be correct *during* a flag day. |

The order matters: items 2 and 3 are the next two steps on the declared path.
Phase 1 step 3 is the one that turns a 200-line refactor into a coordinated
secrets migration. **Do this before that step, or accept paying for it there.**

**The one caveat on the "no data" claim:** I verified the development machine, the
repository, and redline's imports. I did not read the gateway's `hz.db`, and I
should not. Run the freeze check below before stage 3.

---

## 8. Staged plan

Status marks: ◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.
All ✅ — every stage below landed in one merge, as §8's last assumption said it
would have to.

### ✅ Stage 0 — precondition: the rename has landed

**Must be true before starting:** `hz cm` → `hz config` is merged on `dev`;
`go build ./...` and `go test ./... -count=1` are green.
**Why:** both changes rewrite every `cmd/hz/cm*.go` usage string. Doing them at
once is one conflict per file.

### ✅ Stage 1 — freeze check: prove there is nothing to migrate

On the gateway, and on any box that has ever run `hz config`:

```
hz config machines                       # expect: no approved registration
hz config key ls                         # expect: "No keys in <root>."
ls -R "${HZ_HOME:-$HOME/.hz}/secrets/keys" 2>&1   # expect: no such directory
sqlite3 /var/lib/homelab-horizon/hz.db \
  'select count(*) from cm_registrations;
   select count(*) from cm_config_values where ciphertext is not null;
   select count(*) from cm_current_keys;'
jq '.recovery_wraps | length' <config.json>       # expect: 0 or null
```

**Must be true:** all counts zero, or the operator has explicitly accepted the
loss for each non-zero one.
**Record the output in the stage-3 commit message.** A migration that destroys
capability grants must carry the evidence that there were none.
**Risk:** a non-zero count found here turns stage 3 from a rebuild into the
re-seal exercise §3 declines to build. If that happens, stop and re-plan — do not
proceed and "fix it later".

### ✅ Stage 2 — `configmgr` gains the coordinate

`Addr`, `EnvKeyAddr`, both `context()`s, both `String()`s, the keystore segments,
the state tree, `Options`, `PushOptions`, the four wire types, and `doc.go`'s three
context rows + the WebCrypto paragraph.

**Must be true:** `go test ./configmgr/... -count=1` green. **Make the throwaway
test permanent here** — a test asserting that a 4-field context does not open a
3-field seal, for kinds 0x01 and 0x02, and that kind 0x03 is unaffected. That test
is the standing guard on the flag day ever being reversed by accident.
**Not true yet:** hz still speaks three parts. The tree does not build end to end
between stages 2 and 3.

### ✅ Stage 3 — hz's schema and handlers

Migration `0013_project_coordinate`, following 0009's restate-everything shape and
its header-comment convention (say what it does to existing data, in the file).
The nine `internal/db/configmgr.go` signatures. Every cm handler reads
`QueryProject`; `handleAPICMRegister` adds it to the required set and the message.

**Must be true:** `go test ./internal/db/... ./internal/server/... -count=1` green.
Add a migration test that applies 0013 over a 0012 database holding one config
with two values, one registration and one current-key row, and asserts: the values
come back **tombstoned** under project `unmigrated`, the registration is **gone**,
the current-key row is **gone**, and `cm_machine_secrets` and `cm_secret_reads` are
**untouched**.
**Risk:** SQLite's constraint on dropping a parent with cascading children — 0009
hit it and documents the workaround (stage into constraint-free `_temp` tables
first). Reuse that shape rather than rediscovering it.

### ✅ Stage 4 — projection and drift

`Instance.Project`; `resolveEnvironment` collapses to one line; delete the export,
the map, the fallback, the ambiguity branch and the gap (§4's table);
`handlers_version_drift.go` calls `cfg.LookupEnvironment` directly; rewrite
`VersionDriftUnresolved`'s doc comment.

**Must be true:** `go test ./internal/projection/... -count=1` green, **and the
unit names in `projection_test.go` are byte-identical to today's** — `unitName`
must keep its three-part instance part, or every unit on every box is renamed as a
side effect of this change.

### ✅ Stage 5 — CLI, config model, UI

`parseCMAddr` and every usage string; `cmd/hz/main.go`'s CONFIG MANAGER block;
`RecoveryWrap.Project` + `Addr()`; the three `CM*.tsx` components; regenerate
`generated-types.ts`.

**Must be true:** `go build ./... && go test ./... -count=1` green; the tygo
regeneration leaves no unexpected diff; `make lint` (or the repo's equivalent)
clean.

### ✅ Stage 6 — docs

- `plan/config-manager.md` — the keystore path at `:381`, and `:102, 188, 207,
  320, 384, 425, 458, 746, 1286`, every one of which keys off the triple.
- `plan/architecture.md` — `:758–778`, the section that *describes the backwards
  resolution as the design* ("THE APP COORDINATE SUPPLIES THE PROJECT"). That
  section is what this change retires; `:83` already states the target form and
  needs no edit. `:774`'s unit-name scheme is unchanged and should say so
  explicitly, or someone will "fix" it.
- `plan/example-projection.md:179` — `new-box`'s "wants: staging/web/app ←
  (environment, app, role). No project: the address has no project coordinate; it
  is derived from the app" becomes the worked four-part example. Also `:224, 332,
  336, 351, 424, 469, 483`.
- `plan/ui-redesign.md:26` (the `AddressPicker` concept) and `:383` ("already
  per-`(environment, app, role)`").
- `plan/plan.md:466`.
- `configmgr/doc.go` (done in stage 2), and this file's status line.

**Must be true:** no plan file still describes the config address as three-part.

### Assumptions recorded

- The project is compiled into the client beside the app name. If some future
  consumer wants it as a launch flag, the binding argument in §2 has to be
  re-made, not assumed.
- `unmigrated` is an acceptable reserved project name. It is a legal segment and
  the schema refuses nothing; if a real project is ever called that, the sentinel
  rows are indistinguishable from live ones. Low risk, but it is an assumption.
- Stages 2–5 land as one merge. `dev` does not build end to end between 2 and 3.

### Blocking decisions — none

Both calls that looked like questions were answerable from the code and are
decided above: the migration disposition (§3 — delete registrations, tombstone
configs under a sentinel, drop current keys) and the compatibility stance (§5 —
reject with a 400). Neither is a product or taste call.

### Optional extensions — out of scope

- `cm_machines.enrolled_project`, for the same reason `enrolled_environment`
  exists.
- A protocol version field on the machine wire (§5).
- An `open-old / re-seal-new` envelope migration tool (§3). Only worth building if
  stage 1's freeze check comes back non-zero.

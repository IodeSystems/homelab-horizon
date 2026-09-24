# homelab-horizon — the invariants

hz is a **registry you consult**, not a control plane you are on call for. One
person cannot be on call for their own orchestrator; every rule below follows
from that.

Code map: `AGENTS.md`. Work queue and release: `plan/plan.md`. Design detail:
`plan/design/`. **This file is the constitution** — extracted 2026-09-24 from
doc prose and Go doc comments, because every agent working here had to be told
it in a prompt. Each rule states where it is enforced; **an invariant with no
enforcement point says so**, and that is a finding, not a formality.

---

## 1 · hz declares and observes. Agents pull and apply locally.

hz never reaches into a machine. `internal/agent/source.go:15-56` — *"THE AGENT
POLLS. hz NEVER INITIATES."* Transport is a conditional GET whose ETag **is** the
payload's content hash (`Desired.Fingerprint`), so an in-sync fleet costs a 304
and there is no generation counter to keep correct across restarts.

Why: hz holds no inbound credential, needs no route to a box, and works behind
NAT. A report back (`POST /api/v1/agent/observed`) is the *same* direction.

⚠ **Enforced structurally, by absence — no test pins the direction.** No SSH
client in `go.mod`; nothing in `internal/server` writes to a remote box.
Honest exceptions: `bin/deploy` (the operator's bootstrap — it ships the
binary), and `internal/monitor/remote.go` (reads remote vantages). hz reaches
off-box to **read**; it has never *executed* on a box it is not.

## 2 · Empty and unknown are different states.

The founding bug: an empty `BACKUP_BUCKET` selecting the production bucket,
because absent and empty were indistinguishable.

`projection.Gap` (`internal/projection/projection.go:282-297`, reasons
`Unmodelled` / `Unreadable` / `StoodDown`); `MachineConfig.Unresolved` (`:155-162`
— *"an empty section with a gap is hz saying 'I have no opinion'"*);
`Segment.Resolved` (`:167-205`). UI: `readSegment`
(`ui/src/components/model/model.ts:167-201`) refuses to render a blank field for
an unresolved membership.
Enforced: `internal/projection/segments_test.go:241`, `:281`;
`ui/src/components/model/model.selftest.ts:161`.

## 3 · Two channels. The agent never holds an environment key.

```
hz → agent   machine config   network, units, desired version   non-secret
hz → app     sealed config    (project, env, app, role)         the APP holds the key
```

`internal/agent/desired.go:79-131` — the type **has no field** for a key or a
decrypted value. `projection.Unit.ConfigGeneration` (`projection.go:260-280`) is
a sha256 digest, which is how a blessed config triggers a restart without
disclosing itself. `internal/server/handlers_configmgr.go` never constructs an
`*ecdh.PrivateKey` and never calls `Open`/`UnwrapEnvKey` — it parses public keys
only (`:147,735,791`).

⚠ **Unrepresentable-by-type, not test-pinned** in the hz→agent direction;
`internal/agent/observed_test.go:56` pins the reverse.

**Qualification — cert material and `wg0.conf` DO cross**, `Secret` forced by
`Desired.files()` and never trusted from the wire. An environment key decrypts
a whole rung's secrets; a TLS server key authenticates one hostname at one
machine's own edge. The **ACME account key and the DNS provider credentials
never cross** — the agent receives *issued* material, not the power to issue.
(`plan/design/architecture.md`, "Cert material and the two channels".)

## 4 · hz holds no private key — for the config manager. Not an absolute.

`internal/config/segment.go:47-51` — *"NO PRIVATE KEY, ever."* Machine keys are
minted client-side (`configmgr/crypto.go:283`), segment keys on the box
(`internal/agent/segmentkey.go:112`), and `wrapped_env_key` is a BLOB marked
*"Opaque to hz"* (migration `0013:141-143`).

⚠ **Where hz does hold one, say so rather than overclaiming:** the ACME account
key (`internal/acme/acme.go:28-41`), issued TLS private keys
(`internal/letsencrypt/apply.go:98`), and — in the legacy human-VPN path — both
halves of a client's WireGuard key, momentarily, to render a config and a QR
code (`internal/server/handlers_api_vpn.go:49,93-97`).

## 5 · Nothing in the boot path may depend on freshness.

Services here run unattended for years. This has killed at least two proposals
(leases, TUF-style expiry) — `plan/design/config-manager.md:52-92`.

`configmgr/client.go:360,388-392`: on anything but `ErrDenied`, boot from
last-known-good cache and log `LOUD`. `configmgr/state.go:56-59` — *"A
three-year-old cache is valid config."* Rollback is caught by a **monotonic
sequence floor, never a clock**.
Enforced: `configmgr/client_test.go:443 TestCachedBootIsLoud`, `:693
TestCacheNeverGoesStale` (backdates the cache 3 years with `os.Chtimes`),
`:470 TestNoCacheAndNoAnswer`, `:487 TestSequenceFloor`.

## 6 · A machine carries no project.

An environment is a coordinate of an **instance**, never of a box.
`config.Machine` is exactly `{Name, Segments, Note}`
(`internal/config/machine.go:42-67`). Enforced:
`internal/config/machine_test.go:21`, `:40`.

❓ **Live contradiction, unsettled, the operator's call:** `Segment.Project` is
REQUIRED (`internal/config/segment.go:68-74`, enforced `:241` and `:448`) while
`plan/design/example-projection.md:114` asserts `seg:people` has none. Both
cannot be true. It blocks "the VPN becomes a segment row".

## 7 · Declare, then enrol. A box cannot declare itself.

Enrolment **refuses an undeclared machine** (`handlers_api_machines.go:228-238`)
— a box that could declare itself could write itself into the model and then ask
for a credential, inverting the trust direction. `AddMachine` has exactly one
production caller (`:120`).

`hz machine add --self` is resolved **server-side** (`:63-69,92-99`): hz runs
where the gateway is, so a client filling in its own hostname would declare the
operator's laptop. Enforced: `handlers_api_machines_test.go:153`, `:333`;
`cmd/hz-agent/enroll_test.go:196`; and `cmd/hz-agent/selfname_test.go:28`, which
asks hz and the agent for their *real* hostnames and asserts both are the
kernel's — so agreeing on `"unknown"` also fails.

## 8 · Derived, not stored.

A stored derivation is a second answer free to disagree with the thing it is a
view of. WireGuard `Peers` derive from hub/spoke (`Segment.PeersOf`,
`internal/config/segment.go:187-202`); `AllowedIPs` derives from CIDR.
`SegmentMember` has **no such field**, so storing one is unrepresentable.
Enforced: `segment_test.go:617
TestMovingTheHubRewiresEveryPeerSetAndSaysSo` — *"the report is not a second
answer"*.

⚠ **False for the legacy human-VPN path**, which does store them:
`Config.WGPeers` (`internal/config/config.go:429`) and `WGPeer.AllowedIPs`
(`:572-579`), snapshotted from `wg0.conf` so HA peer-sync can replicate it.
Different subsystem; `architecture.md`'s own "Not true yet" table admits it.

## 9 · Posture is ordered, and the order is not the name.

`PostureRank` (`internal/config/config.go:669-683`) is the only legal
comparison. String order sorts `dev < prod < staging`, which would read a
promotion to prod as a **demotion**. Gate: `CheckPromotion` (`:789-808`).
Enforced: `environments_test.go:190`, `promotion_test.go:63`.

⚠ **Convention, not type.** `Posture` is a plain `string`; nothing stops
`a.Posture < b.Posture`. No non-test code does it today. Route every comparison
through `PostureRank`.

## 10 · A crossing is a declared exception with a reason.

*A reason nobody is obliged to give is a reason nobody gives.* A multi-homed
machine is **refused without a `Note`** (`internal/config/machine.go:58-66`,
enforced `:119-122` and `:170-173`; `machine_test.go:91`,
`handlers_api_machines_test.go:99`), and a segment must name its owning project
(`segment.go:449`) — the owner is what makes another project's machine on it
read as a crossing.

⚠ **The traffic half is designed, not built.** `projection.Forward{From, To,
Reason}` (`projection.go:208-219`) has no producer; `MachineConfig.Forwards` is
hardcoded `[]Forward{}` (`:513`). Deny-by-default between a machine's own
interfaces is what is live. Do not cite this invariant as one finished
mechanism.

## 11 · First sighting adopts, never acts.

The rule that stops a fleet-wide bounce the first time config generations
appear. `internal/agent/plan.go:249-250` puts an unseen unit in a separate
`Adopted` list, never `Restarts` (`:252`). `GenerationStore.Load` returns an
empty record with **no error** on first run (`generations.go:76-79,100-118`) —
and unreadable is NOT missing (`:110-116`). Enforced:
`config_restart_test.go:99`, `:126`, `:145`.

## 12 · The pure/privileged seam: decide in `plan.go`, act in `apply.go`.

`internal/agent/seam_test.go:31 TestPureHalfStaysPure` parses the pure files
with `go/parser` and fails on an import of `os`, `os/exec`, `net`, `net/http`,
`time`, `math/rand`, `crypto/rand` or `path/filepath`; `:60
TestPureHalfCannotApply` walks every `ast.CallExpr` for `Apply` /
`writeIfChanged` / `Observe`. The same guard sits in `internal/`{`haproxy`,
`dnsmasq`, `iptables`, `wireguard`, `letsencrypt`, `acme`, `projection`}.

`wireguard`'s and `acme`'s are **stricter and had to be** — they check
*selectors*, because those renderers genuinely need `net.ParseCIDR` and
`time.Unix` while `net.LookupHost` and `time.Now` must stay out, and an
import-only rule would have to allow all of `net`.

## 13 · Three properties bound any privileged verb.

Rescued from the deleted `privilege-classification.md` §5.2 — state them as a
rule, because `systemdRun` shows how fast a general-purpose root helper appears:

1. **Never a shell string.** A verb takes typed arguments or nothing.
2. **Never a subcommand from a request.** A privileged executor whose *verb* is
   caller-controlled cannot be narrowed by validation.
3. **Never reachable from the web process.** Not by exec, socket or
   `systemd-run`. If hz web can trigger it, it is hz web's privilege regardless
   of which uid holds it.

## 14 · A rule nothing checks is a comment.

Make it structural. 1750 test functions; these are the ones that fail the build
when a *rule* breaks rather than a behaviour:

| Guard | Makes impossible |
|---|---|
| `internal/server/handlers_api_model_test.go:32` (bidirectional wire mirror) | a field on `projection.MachineConfig` missing from `apitypes.MachineProjectionResp`, either way |
| `configmgr/project_binding_test.go:38 TestCrossProjectReadIsRefused` | a value sealed at one project's address opening at another's, same key |
| `cmd/hz-agent/selfname_test.go:28` | hz's declared name and the agent's enrolment name diverging |
| `cmd/hz/config_alias_test.go:45,66,89` | `hz config` and the deprecated `hz cm` alias diverging |
| `internal/server/handlers_api_assign_test.go:209` | a placement-free edit silently unassigning a service |
| `internal/server/hz_client_script_test.go:13` | `bin/hz-client` drifting from its embedded copy |
| `internal/agent/ownership.go` + `TestRemovalIsImpossibleOutsideAClaimedDirectory` | the agent deleting outside a claimed directory — the bound is re-asked immediately before each unlink, never trusted from the plan |

## 15 · Validate the instrument before trusting silence.

**An empty grep, an empty query or a zero count is a claim about the TOOL.**
This repo was burned five times in one day: a `sqlite3` that was not installed,
a `sudo` failing into `/dev/null`, a `pkill` pattern that never matched, a
`curl` against the wrong URL path, and a backup that passed `integrity_check`
while missing three migrations in the WAL.

Positive-control it. The house example is the project-coordinate flag day
(`plan/done.md`): the cross-project read was measured **succeeding** against the
old binding *before any code changed*, so `TestCrossProjectReadIsRefused` proves
a change rather than a tautology.

Corollary, same file: **an address assembled field by field goes silently
short** when the struct gains a field — every field is a `string` and the zero
value is legal. An audit that greps for the **type** name finds those sites; one
that greps for the field names does not.

---

## The one authority — know this before designing any gate

`RoleAdmin` is the **only** role (`internal/db/users.go:23,29,70-71`: *"hz has
exactly one privilege level"*), and API tokens have **no scope field**
(`internal/db/api_tokens.go:34-45`). `cmApprove`
(`internal/server/handlers_configmgr.go:756`) needs only a signed-in user; there
is no second-approver check and none is possible.

So **every promotion or approval gate gates one authority against itself**, and
*"intern opens, senior blesses"* has no representation at all. This is a
decision, not a build — see `plan/plan.md` Tier 1. Do not add another gate
before it is settled.

## House rules

- Status marks: ◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked · ⚠ caveat.
- Every "today" claim about the code carries a `file:line`, checked not recalled.
- **The icebox is what the next release EXCLUDES**, by definition — not a "later" pile.
- Maintain the plan in the same pass as the work. A stale plan is worse than none.
- A refactor claiming byte-identical output must not also change behaviour; what
  it finds goes to `plan/icebox.md` with the evidence, not into the diff.

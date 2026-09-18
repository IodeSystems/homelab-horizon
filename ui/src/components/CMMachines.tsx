import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Card,
  CardContent,
  Chip,
  Tooltip,
  Typography,
} from "@mui/material";
import DnsIcon from "@mui/icons-material/DnsOutlined";
import KeyIcon from "@mui/icons-material/VpnKeyOutlined";
import { useCMCurrentKey, useCMRegistrations } from "../api/hooks";
import type { CMRegistrationResp } from "../api/generated-types";

// The fleet view: every registration hz holds, grouped by machine.
//
// It shows no config value, because there are none to show — every value is
// sealed and hz holds no key. Names, addresses, states, key ids and lineage,
// nothing else.
//
// The one thing here that is not inventory is the key-id column. Key ids let
// old ciphertext keep opening, but they do not deliver a new key to a box that
// was already approved: hz holds no key, so every re-wrap needs an approver and
// the machine's public key. A rotation that is not followed by re-wrapping each
// approved registration leaves those boxes unable to open anything sealed under
// the new key, and until this page there was no way to see which ones.

// An address is (environment, app, role) — the unit an environment key is
// minted for, and the unit the current-key pointer is set on.
type Addr = { environment: string; app: string; role: string };

function addrKey(a: Addr): string {
  return `${a.environment}/${a.app}/${a.role}`;
}

// What hz says the current key at one address is. "unannounced" is its own
// state on purpose: a 404 from the pointer means nobody has announced a current
// key, which is a different fact from "the current key is X" and must not be
// rendered as though everything is up to date.
type AddrKey = {
  status: "checking" | "announced" | "unannounced" | "unknown";
  keyId: string;
};

type RegVerdict =
  | { kind: "checking" }
  | { kind: "unannounced" }
  | { kind: "current" }
  | { kind: "stale"; current: string }
  | { kind: "unknown" }
  | { kind: "nokey" }
  | { kind: "missing" };

function relativeTime(isoStr: string | undefined): string {
  if (!isoStr) return "never";
  const d = new Date(isoStr);
  if (isNaN(d.getTime()) || d.getTime() === 0) return "never";
  const seconds = Math.floor((Date.now() - d.getTime()) / 1000);
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function verdictFor(reg: CMRegistrationResp, ak: AddrKey | undefined): RegVerdict {
  // Only an approved registration holds a wrapped key at all. Pending and
  // denied ones have nothing to be stale.
  if (reg.state !== "approved") return { kind: "nokey" };
  if (!reg.wrapKeyId) return { kind: "missing" };
  if (!ak || ak.status === "checking") return { kind: "checking" };
  if (ak.status === "unknown") return { kind: "unknown" };
  if (ak.status === "unannounced") return { kind: "unannounced" };
  return ak.keyId === reg.wrapKeyId
    ? { kind: "current" }
    : { kind: "stale", current: ak.keyId };
}

// Asks hz for one address's current-key pointer and reports the answer upward.
// Rendered once per distinct address and always mounted, so the fleet counts at
// the top of the card cover every machine rather than only the filtered rows.
function CurrentKeyProbe({
  addr,
  onAnswer,
}: {
  addr: Addr;
  onAnswer: (key: string, value: AddrKey) => void;
}) {
  const { data, isLoading, isError } = useCMCurrentKey(
    addr.environment,
    addr.app,
    addr.role,
  );
  const key = addrKey(addr);
  // data === null is the 404 the hook translates: no pointer has been
  // announced. An announced pointer with an empty keyId means the same thing.
  const keyId = data?.keyId ?? "";
  const status: AddrKey["status"] = isLoading
    ? "checking"
    : isError
      ? "unknown"
      : keyId
        ? "announced"
        : "unannounced";

  useEffect(() => {
    onAnswer(key, { status, keyId });
  }, [key, status, keyId, onAnswer]);

  return null;
}

function KeyChip({ verdict }: { verdict: RegVerdict }) {
  switch (verdict.kind) {
    case "stale":
      return (
        <Tooltip title={`hz's current key at this address is ${verdict.current}`}>
          <Chip
            size="small"
            color="error"
            label="old key"
            sx={{ height: 20, fontSize: "0.7rem" }}
          />
        </Tooltip>
      );
    case "unannounced":
      return (
        <Tooltip title="Nobody has announced a current key for this address, so there is nothing to compare against">
          <Chip
            size="small"
            color="warning"
            variant="outlined"
            label="no current key"
            sx={{ height: 20, fontSize: "0.7rem" }}
          />
        </Tooltip>
      );
    case "current":
      return (
        <Chip
          size="small"
          color="success"
          variant="outlined"
          label="current key"
          sx={{ height: 20, fontSize: "0.7rem" }}
        />
      );
    case "missing":
      return (
        <Tooltip title="Approved, but hz recorded no key id for the blob it stored. Nothing can say whether this box holds the current key.">
          <Chip
            size="small"
            color="error"
            variant="outlined"
            label="no key id"
            sx={{ height: 20, fontSize: "0.7rem" }}
          />
        </Tooltip>
      );
    case "unknown":
      return (
        <Chip
          size="small"
          variant="outlined"
          label="key unknown"
          sx={{ height: 20, fontSize: "0.7rem" }}
        />
      );
    default:
      return null;
  }
}

function StateChip({ state }: { state: string }) {
  if (state === "approved") {
    return <Chip size="small" color="success" label="approved" sx={{ height: 20, fontSize: "0.7rem" }} />;
  }
  if (state === "denied") {
    return <Chip size="small" color="error" label="denied" sx={{ height: 20, fontSize: "0.7rem" }} />;
  }
  return <Chip size="small" label={state || "pending"} sx={{ height: 20, fontSize: "0.7rem" }} />;
}

function RegistrationRow({
  reg,
  verdict,
}: {
  reg: CMRegistrationResp;
  verdict: RegVerdict;
}) {
  const squat =
    !!reg.enrolledEnvironment && reg.enrolledEnvironment !== reg.environment;

  return (
    <Box sx={{ py: 0.75, pl: 2, borderTop: 1, borderColor: "divider" }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
        <Typography
          component="code"
          variant="body2"
          sx={{ fontFamily: "monospace", fontWeight: 600 }}
        >
          {addrKey(reg)}
        </Typography>
        <StateChip state={reg.state} />
        <Chip
          size="small"
          variant="outlined"
          label={`v${reg.version}`}
          sx={{ height: 20, fontSize: "0.7rem" }}
        />
        <KeyChip verdict={verdict} />
        {squat && (
          <Tooltip title={`Enrolled as "${reg.enrolledEnvironment}"`}>
            <Chip
              size="small"
              color="warning"
              label="environment mismatch"
              sx={{ height: 20, fontSize: "0.7rem" }}
            />
          </Tooltip>
        )}
      </Box>

      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
        {reg.state === "approved" &&
          `approved by ${reg.approvedBy || "(unrecorded)"} ${relativeTime(reg.approvedAt)}`}
        {reg.state === "pending" && `enrolled ${relativeTime(reg.createdAt)}`}
        {reg.state === "denied" && `denied — ${reg.deniedReason || "(no reason recorded)"}`}
        {` · last asked ${relativeTime(reg.lastSeenAt)}`}
      </Typography>

      {reg.wrapKeyId && (
        <Typography
          variant="caption"
          color="text.secondary"
          sx={{ display: "block", fontFamily: "monospace", wordBreak: "break-all" }}
        >
          holds {reg.wrapKeyId}
          {verdict.kind === "stale" ? ` · current is ${verdict.current}` : ""}
        </Typography>
      )}

      {verdict.kind === "stale" && (
        <Typography variant="caption" color="error.main" sx={{ display: "block", mt: 0.25 }}>
          This box cannot open anything sealed under the current key. Its config
          pulls at this address fail until it is granted the new one.
        </Typography>
      )}
    </Box>
  );
}

export function CMMachines() {
  // There is no "all states" listing: the endpoint defaults to pending and
  // accepts one state at a time. Three queries, merged here, so the counts
  // below describe the whole fleet rather than one filter's worth of it.
  const pending = useCMRegistrations("pending");
  const approved = useCMRegistrations("approved");
  const denied = useCMRegistrations("denied");

  const [filter, setFilter] = useState<"all" | "pending" | "approved" | "denied">("all");
  const [addrKeys, setAddrKeys] = useState<Record<string, AddrKey>>({});

  const onAnswer = useCallback((key: string, value: AddrKey) => {
    setAddrKeys((prev) => {
      const old = prev[key];
      if (old && old.status === value.status && old.keyId === value.keyId) return prev;
      return { ...prev, [key]: value };
    });
  }, []);

  const regs = useMemo(
    () => [...(pending.data ?? []), ...(approved.data ?? []), ...(denied.data ?? [])],
    [pending.data, approved.data, denied.data],
  );

  const addrs = useMemo(() => {
    const seen = new Map<string, Addr>();
    for (const r of regs) {
      const a: Addr = { environment: r.environment, app: r.app, role: r.role };
      const k = addrKey(a);
      if (!seen.has(k)) seen.set(k, a);
    }
    return [...seen.entries()].sort((x, y) => x[0].localeCompare(y[0]));
  }, [regs]);

  const machines = useMemo(() => {
    const byMachine = new Map<string, { name: string; regs: CMRegistrationResp[] }>();
    for (const r of regs) {
      const entry = byMachine.get(r.machineId);
      if (entry) {
        entry.regs.push(r);
      } else {
        byMachine.set(r.machineId, { name: r.machineName, regs: [r] });
      }
    }
    const out = [...byMachine.entries()].map(([id, m]) => ({
      id,
      name: m.name,
      regs: [...m.regs].sort((a, b) => addrKey(a).localeCompare(addrKey(b))),
    }));
    out.sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id));
    return out;
  }, [regs]);

  const staleCount = regs.filter((r) => verdictFor(r, addrKeys[addrKey(r)]).kind === "stale").length;
  // Addresses with approved boxes but no announced pointer. Not the same thing
  // as "everything is current" — nothing has been compared at all.
  const unannouncedAddrs = addrs
    .filter(([k]) => addrKeys[k]?.status === "unannounced")
    .filter(([k]) => regs.some((r) => r.state === "approved" && addrKey(r) === k))
    .map(([k]) => k);

  const isLoading = pending.isLoading || approved.isLoading || denied.isLoading;
  const error = pending.error ?? approved.error ?? denied.error;

  if (isLoading) return null;
  if (error) {
    return (
      <Alert severity="error" sx={{ mb: 3 }}>
        Could not load registrations: {error.message}
      </Alert>
    );
  }

  const counts = {
    all: regs.length,
    pending: pending.data?.length ?? 0,
    approved: approved.data?.length ?? 0,
    denied: denied.data?.length ?? 0,
  };

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      {addrs.map(([k, a]) => (
        <CurrentKeyProbe key={k} addr={a} onAnswer={onAnswer} />
      ))}

      <CardContent>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <DnsIcon fontSize="small" color="action" />
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Machines
          </Typography>
        </Box>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: 680 }}>
          Every registration hz holds, grouped by machine. One machine has a
          registration per <code>(environment, app, role)</code> it asks for, and
          each one is approved separately. No config value appears here or
          anywhere else in this UI: every value is sealed and hz holds no key.
        </Typography>

        {/* The rotation affordance. A box holding an old key is not degraded —
            it cannot open anything sealed under the new one at all — and
            nothing else in the system reports it, because the box's own report
            would go to hz. */}
        {staleCount > 0 && (
          <Alert severity="warning" sx={{ mt: 2 }}>
            <AlertTitle sx={{ fontSize: "0.9rem" }}>
              {staleCount} approved registration{staleCount === 1 ? "" : "s"} hold
              an old environment key
            </AlertTitle>
            <Typography variant="body2">
              Those boxes cannot open anything sealed under the current key, so
              their config pulls fail — a rotation is not finished until every
              approved registration at the address has been re-wrapped. There is
              no re-wrap command today: <code>hz cm approve</code> acts only on a
              pending registration, so a rotation strands these until that
              exists.
            </Typography>
          </Alert>
        )}

        {unannouncedAddrs.length > 0 && (
          <Alert severity="info" sx={{ mt: 2 }}>
            <AlertTitle sx={{ fontSize: "0.9rem" }}>
              No current key announced for {unannouncedAddrs.join(", ")}
            </AlertTitle>
            <Typography variant="body2">
              Nothing has been compared at these addresses — this is not a
              statement that the boxes are up to date. Set the pointer with{" "}
              <code>hz cm key current &lt;env&gt;/&lt;app&gt;/&lt;role&gt; --set
              &lt;keyid&gt;</code> and the column becomes meaningful.
            </Typography>
          </Alert>
        )}

        <Box sx={{ display: "flex", gap: 0.75, mt: 2, flexWrap: "wrap" }}>
          {(["all", "pending", "approved", "denied"] as const).map((f) => (
            <Chip
              key={f}
              size="small"
              label={`${f} (${counts[f]})`}
              variant={filter === f ? "filled" : "outlined"}
              color={filter === f ? "primary" : "default"}
              onClick={() => setFilter(f)}
              sx={{ height: 24, fontSize: "0.72rem" }}
            />
          ))}
        </Box>

        {machines.length === 0 ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            No machine has enrolled yet.
          </Typography>
        ) : (
          <Box sx={{ mt: 1.5 }}>
            {machines.map((m) => {
              const shown =
                filter === "all" ? m.regs : m.regs.filter((r) => r.state === filter);
              if (shown.length === 0) return null;
              const machineStale = m.regs.filter(
                (r) => verdictFor(r, addrKeys[addrKey(r)]).kind === "stale",
              ).length;
              return (
                <Box key={m.id} sx={{ mt: 2 }}>
                  <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>
                      {m.name || "(unnamed)"}
                    </Typography>
                    <Chip
                      size="small"
                      variant="outlined"
                      label={`${m.regs.length} registration${m.regs.length === 1 ? "" : "s"}`}
                      sx={{ height: 20, fontSize: "0.7rem" }}
                    />
                    {machineStale > 0 && (
                      <Chip
                        size="small"
                        color="error"
                        icon={<KeyIcon />}
                        label={`${machineStale} on an old key`}
                        sx={{ height: 20, fontSize: "0.7rem" }}
                      />
                    )}
                  </Box>
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ display: "block", fontFamily: "monospace", wordBreak: "break-all" }}
                  >
                    {m.id}
                  </Typography>
                  <Box sx={{ mt: 0.5 }}>
                    {shown.map((r) => (
                      <RegistrationRow
                        key={r.id}
                        reg={r}
                        verdict={verdictFor(r, addrKeys[addrKey(r)])}
                      />
                    ))}
                  </Box>
                </Box>
              );
            })}
          </Box>
        )}
      </CardContent>
    </Card>
  );
}

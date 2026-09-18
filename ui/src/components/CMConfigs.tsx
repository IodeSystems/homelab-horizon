import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import ArrowBackIcon from "@mui/icons-material/ArrowBackOutlined";
import ContentCopyIcon from "@mui/icons-material/ContentCopyOutlined";
import InventoryIcon from "@mui/icons-material/Inventory2Outlined";
import NorthEastIcon from "@mui/icons-material/NorthEastOutlined";
import { useCMConfig, useCMConfigs, useCMRegistrations } from "../api/hooks";
import type { CMConfigResp, CMConfigValueResp } from "../api/generated-types";

// Config inventory for one (environment, app, role).
//
// It shows key names, bindings, key ids, ranges, sequences and lineage. It
// never shows a value, because there is no value to show: everything is sealed
// client-side and hz holds no key. `sealed` is base64 ciphertext and is
// deliberately not rendered anywhere in this file — decrypting is
// `hz cm show <config-id>`, in the CLI, which is where the keys are.
//
// Nothing here touches WebCrypto. A browser served by hz cannot defend against
// hz, so the ceremony lives in a locally installed binary instead.

export type CMAddress = { env: string; app: string; role: string };

export const emptyCMAddress: CMAddress = { env: "", app: "", role: "" };

export function addressComplete(a: CMAddress): boolean {
  return a.env.trim() !== "" && a.app.trim() !== "" && a.role.trim() !== "";
}

export function addressLabel(a: CMAddress): string {
  return `${a.env.trim()}/${a.app.trim()}/${a.role.trim()}`;
}

// A range with no maxVer is OPEN, not unknown and not blank. The newest config
// at an address always has one, so a blank cell here would hide the normal case.
export function rangeLabel(c: CMConfigResp): string {
  return `${c.minVer} → ${c.maxVer ? c.maxVer : "open"}`;
}

export function whenLabel(iso: string | undefined): string {
  if (!iso) return "unknown";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function shortId(id: string): string {
  return id.length > 14 ? `${id.slice(0, 12)}…` : id;
}

// CopyBox is a command to run somewhere else — selectable, wrapped, with a copy
// button. Shared with CMPromote, where the promotion itself is a CLI command
// because this page holds neither key.
export function CopyBox({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "flex-start",
        gap: 1,
        p: 1,
        bgcolor: "action.hover",
        borderRadius: 1,
      }}
    >
      <Typography
        component="code"
        sx={{
          flex: 1,
          fontFamily: "monospace",
          fontSize: "0.72rem",
          whiteSpace: "pre-wrap",
          wordBreak: "break-all",
          lineHeight: 1.6,
        }}
      >
        {text}
      </Typography>
      <Tooltip title={copied ? "Copied" : "Copy"}>
        <IconButton
          size="small"
          onClick={() => {
            void navigator.clipboard?.writeText(text);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          <ContentCopyIcon fontSize="small" />
        </IconButton>
      </Tooltip>
    </Box>
  );
}

// Addresses are typed, not enumerated — hz has no route that lists them, and a
// config can be blessed for a release that does not exist yet, so no listing
// would be complete anyway. The registration queue is the one place addresses
// are already known, so it supplies suggestions rather than the set.
export function AddressPicker({
  value,
  onChange,
}: {
  value: CMAddress;
  onChange: (a: CMAddress) => void;
}) {
  const pending = useCMRegistrations("pending");
  const approved = useCMRegistrations("approved");

  const seen = new Map<string, CMAddress>();
  for (const r of [...(approved.data ?? []), ...(pending.data ?? [])]) {
    const a = { env: r.environment, app: r.app, role: r.role };
    seen.set(addressLabel(a), a);
  }
  const suggestions = [...seen.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  const current = addressComplete(value) ? addressLabel(value) : "";

  return (
    <Box>
      <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
        <TextField
          label="Environment"
          size="small"
          fullWidth
          value={value.env}
          onChange={(e) => onChange({ ...value, env: e.target.value })}
        />
        <TextField
          label="App"
          size="small"
          fullWidth
          value={value.app}
          onChange={(e) => onChange({ ...value, app: e.target.value })}
        />
        <TextField
          label="Role"
          size="small"
          fullWidth
          helperText="Role is a function — one app may hold several."
          value={value.role}
          onChange={(e) => onChange({ ...value, role: e.target.value })}
        />
      </Stack>
      {suggestions.length > 0 && (
        <Box sx={{ display: "flex", alignItems: "center", gap: 0.75, flexWrap: "wrap", mt: 1 }}>
          <Typography variant="caption" color="text.secondary">
            Registered:
          </Typography>
          {suggestions.map(([label, a]) => (
            <Chip
              key={label}
              size="small"
              label={label}
              variant={label === current ? "filled" : "outlined"}
              color={label === current ? "primary" : "default"}
              onClick={() => onChange(a)}
              sx={{ height: 22, fontSize: "0.7rem", fontFamily: "monospace" }}
            />
          ))}
        </Box>
      )}
    </Box>
  );
}

export function BindingChip({ binding }: { binding: string }) {
  const env = binding !== "invariant";
  return (
    <Tooltip
      title={
        env
          ? "Environment-bound: never promotes. The target must already have a value bound for this key."
          : "Invariant: promotes by client-side re-seal."
      }
    >
      <Chip
        size="small"
        variant="outlined"
        color={env ? "warning" : "default"}
        label={env ? "environment-bound" : "invariant"}
        sx={{ height: 20, fontSize: "0.7rem" }}
      />
    </Tooltip>
  );
}

// One key of a config. Everything on this row is metadata; the ciphertext is
// never rendered.
function ValueRow({
  value,
  onFollowLineage,
}: {
  value: CMConfigValueResp;
  onFollowLineage?: (sourceConfigID: string) => void;
}) {
  // A tombstoned value is destroyed on purpose, not missing. Lineage is
  // append-only and payloads are destructible, so the row and its provenance
  // outlive the bytes — and that must read as the design working, not a gap.
  const destroyed = !value.sealed && !!value.tombstonedAt;

  return (
    <Box sx={{ py: 1, borderTop: 1, borderColor: "divider" }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
        <Typography
          variant="body2"
          sx={{ fontFamily: "monospace", fontWeight: 600, textDecoration: destroyed ? "line-through" : "none" }}
        >
          {value.key}
        </Typography>
        <BindingChip binding={value.binding} />
        {value.origin === "promoted" ? (
          <Chip
            size="small"
            color="info"
            variant="outlined"
            label="promoted"
            sx={{ height: 20, fontSize: "0.7rem" }}
          />
        ) : (
          <Chip
            size="small"
            variant="outlined"
            label="set here"
            sx={{ height: 20, fontSize: "0.7rem" }}
          />
        )}
        {destroyed && (
          <Chip
            size="small"
            color="error"
            label="ciphertext destroyed"
            sx={{ height: 20, fontSize: "0.7rem" }}
          />
        )}
      </Box>

      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ display: "block", mt: 0.25, fontFamily: "monospace", wordBreak: "break-all" }}
      >
        <Tooltip title={value.keyId}>
          <span>sealed under key {shortId(value.keyId)}</span>
        </Tooltip>
      </Typography>

      {/* Lineage is the question the whole append-only design exists to
          answer: not "do these differ" but "where did this value come from". */}
      {value.origin === "promoted" && value.sourceConfigId && (
        <Box sx={{ display: "flex", alignItems: "center", gap: 0.5, mt: 0.25 }}>
          <Typography variant="caption" color="text.secondary">
            promoted from
          </Typography>
          <Button
            size="small"
            variant="text"
            endIcon={<NorthEastIcon sx={{ fontSize: "0.8rem !important" }} />}
            onClick={() => onFollowLineage?.(value.sourceConfigId!)}
            disabled={!onFollowLineage}
            sx={{ p: 0, minWidth: 0, fontSize: "0.7rem", fontFamily: "monospace", textTransform: "none" }}
          >
            {shortId(value.sourceConfigId)}
          </Button>
        </Box>
      )}
      {value.origin === "promoted" && !value.sourceConfigId && (
        <Typography variant="caption" color="warning.main" sx={{ display: "block", mt: 0.25 }}>
          promoted, but the promoting client recorded no source config — lineage
          stops here.
        </Typography>
      )}

      {destroyed && (
        <Typography variant="caption" color="error.main" sx={{ display: "block", mt: 0.25 }}>
          Destroyed {whenLabel(value.tombstonedAt)}
          {value.tombstonedBy ? ` by ${value.tombstonedBy}` : ""}. The row and its
          provenance survive on purpose; the bytes do not. A box starting against
          this config will not get this key.
        </Typography>
      )}
    </Box>
  );
}

function ConfigHeaderLine({ config }: { config: CMConfigResp }) {
  return (
    <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
      <Chip
        size="small"
        label={`seq ${config.sequence}`}
        sx={{ height: 20, fontSize: "0.7rem", fontFamily: "monospace" }}
      />
      <Typography variant="body2" sx={{ fontFamily: "monospace", fontWeight: 600 }}>
        {rangeLabel(config)}
      </Typography>
      {!config.maxVer && (
        <Tooltip title="Open-ended: it keeps covering every release above its minimum until a newer config supersedes it.">
          <Chip
            size="small"
            variant="outlined"
            color="success"
            label="open-ended"
            sx={{ height: 20, fontSize: "0.7rem" }}
          />
        </Tooltip>
      )}
    </Box>
  );
}

function ConfigRow({
  config,
  selected,
  onSelect,
}: {
  config: CMConfigResp;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <Box
      onClick={onSelect}
      sx={{
        py: 1.25,
        px: 1,
        borderTop: 1,
        borderColor: "divider",
        cursor: "pointer",
        bgcolor: selected ? "action.selected" : "transparent",
        "&:hover": { bgcolor: selected ? "action.selected" : "action.hover" },
      }}
    >
      <ConfigHeaderLine config={config} />
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
        blessed by {config.createdBy || "unknown"} on {whenLabel(config.createdAt)} ·{" "}
        <Box component="span" sx={{ fontFamily: "monospace" }}>
          {shortId(config.id)}
        </Box>
      </Typography>
    </Box>
  );
}

// The detail panel is driven by a config id rather than by the selected row,
// because following lineage lands on a config at a DIFFERENT address — a prod
// value's source lives in staging. The trail is a stack so "back" returns to
// where the question was asked.
function ConfigDetail({
  configID,
  address,
  trailDepth,
  onBack,
  onFollowLineage,
}: {
  configID: string;
  address: CMAddress;
  trailDepth: number;
  onBack: () => void;
  onFollowLineage: (id: string) => void;
}) {
  const { data, isLoading, error } = useCMConfig(configID);

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
        <CircularProgress size={20} />
      </Box>
    );
  }
  if (error) {
    return <Alert severity="error">Could not load config: {error.message}</Alert>;
  }
  if (!data) return null;

  const elsewhere =
    data.environment !== address.env.trim() ||
    data.app !== address.app.trim() ||
    data.role !== address.role.trim();
  const values = data.values ?? [];

  return (
    <Box>
      <Box sx={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 1 }}>
        <Box sx={{ minWidth: 0 }}>
          <ConfigHeaderLine config={data} />
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
            <Box component="span" sx={{ fontFamily: "monospace" }}>
              {data.environment}/{data.app}/{data.role}
            </Box>{" "}
            · blessed by {data.createdBy || "unknown"} on {whenLabel(data.createdAt)}
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ display: "block", fontFamily: "monospace", wordBreak: "break-all" }}
          >
            {data.id}
          </Typography>
        </Box>
        {trailDepth > 1 && (
          <Button size="small" startIcon={<ArrowBackIcon fontSize="small" />} onClick={onBack}>
            Back
          </Button>
        )}
      </Box>

      {elsewhere && (
        <Alert severity="info" sx={{ mt: 1.5 }}>
          Followed lineage out of {addressLabel(address)}. This config lives at{" "}
          <strong>
            {data.environment}/{data.app}/{data.role}
          </strong>
          .
        </Alert>
      )}

      <Box sx={{ mt: 1.5 }}>
        <Typography variant="body2" sx={{ fontWeight: 600 }}>
          {values.length} key{values.length === 1 ? "" : "s"}
        </Typography>
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 0.5 }}>
          Names, bindings and lineage only. Every value is sealed and hz holds no
          key, so there is nothing here to show and nothing here to leak.
        </Typography>

        {values.length === 0 ? (
          <Typography variant="body2" color="text.secondary" sx={{ fontStyle: "italic", mt: 1 }}>
            No keys. A config with no values resolves fine and supplies nothing.
          </Typography>
        ) : (
          values.map((v) => (
            <ValueRow key={v.key} value={v} onFollowLineage={onFollowLineage} />
          ))
        )}
      </Box>

      <Box sx={{ mt: 2 }}>
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 0.5 }}>
          To read the values, decrypt them where the keys are:
        </Typography>
        <CopyBox text={`hz cm show ${data.id}`} />
      </Box>
    </Box>
  );
}

export function CMConfigs({ initialAddress }: { initialAddress?: CMAddress }) {
  const [address, setAddress] = useState<CMAddress>(initialAddress ?? emptyCMAddress);
  const [trail, setTrail] = useState<string[]>([]);
  const selectedID = trail.length > 0 ? (trail[trail.length - 1] ?? null) : null;

  const ready = addressComplete(address);
  const { data, isLoading, error } = useCMConfigs(
    address.env.trim(),
    address.app.trim(),
    address.role.trim(),
  );

  // Newest first: the highest sequence is what a current box resolves to, so it
  // should not be at the bottom of the list.
  const configs = [...(data ?? [])].sort((a, b) => b.sequence - a.sequence);
  const openEnded = configs.filter((c) => !c.maxVer).length;

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      <CardContent>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <InventoryIcon fontSize="small" color="action" />
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Configs
          </Typography>
        </Box>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: 680 }}>
          Every config blessed for one address, with its version range, its
          sequence and where each of its keys came from. Values are sealed on a
          client and hz has no key for them, so this page shows the shape of a
          config and never its contents.
        </Typography>

        <Box sx={{ mt: 2 }}>
          <AddressPicker
            value={address}
            onChange={(a) => {
              setAddress(a);
              setTrail([]);
            }}
          />
        </Box>

        {!ready ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            Give an environment, an app and a role to list what is blessed there.
          </Typography>
        ) : error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            Could not list configs: {error.message}
          </Alert>
        ) : isLoading ? (
          <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
            <CircularProgress size={20} />
          </Box>
        ) : configs.length === 0 ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            Nothing blessed at {addressLabel(address)} yet. Configs are pushed with{" "}
            <code>hz cm push</code>, which seals the values on the machine that
            holds the key.
          </Typography>
        ) : (
          <Stack direction={{ xs: "column", md: "row" }} spacing={2} sx={{ mt: 2, alignItems: "flex-start" }}>
            <Box sx={{ flex: "1 1 40%", minWidth: 0, width: "100%" }}>
              <Typography variant="body2" sx={{ fontWeight: 600 }}>
                {configs.length} config{configs.length === 1 ? "" : "s"} at{" "}
                <Box component="span" sx={{ fontFamily: "monospace" }}>
                  {addressLabel(address)}
                </Box>
              </Typography>
              {/* Several open-ended configs at one address is how supersession
                  works — each new one is blessed open-ended, a box takes the
                  highest sequence whose range contains its version, and the
                  older ones keep serving older binaries. It is the normal
                  shape, so it is explained here and never flagged. */}
              {openEnded > 1 && (
                <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
                  {openEnded} of them are open-ended. That is how supersession
                  works: a box takes the highest sequence whose range contains
                  its version, and the older ones go on serving older releases.
                </Typography>
              )}
              <Box sx={{ mt: 1 }}>
                {configs.map((c) => (
                  <ConfigRow
                    key={c.id}
                    config={c}
                    selected={trail.length === 1 && selectedID === c.id}
                    onSelect={() => setTrail([c.id])}
                  />
                ))}
              </Box>
            </Box>

            <Box
              sx={{
                flex: "1 1 60%",
                minWidth: 0,
                width: "100%",
                p: 2,
                border: 1,
                borderColor: "divider",
                borderRadius: 1,
              }}
            >
              {selectedID ? (
                <ConfigDetail
                  configID={selectedID}
                  address={address}
                  trailDepth={trail.length}
                  onBack={() => setTrail((t) => t.slice(0, -1))}
                  onFollowLineage={(id) => setTrail((t) => [...t, id])}
                />
              ) : (
                <Typography variant="body2" color="text.secondary" sx={{ fontStyle: "italic" }}>
                  Pick a config to see its keys, their bindings and where each one
                  came from.
                </Typography>
              )}
            </Box>
          </Stack>
        )}
      </CardContent>
    </Card>
  );
}

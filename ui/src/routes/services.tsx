import { useState, useCallback, useEffect, useMemo } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Paper,
  Snackbar,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TableSortLabel,
  Tab,
  Tabs,
  TextField,
  Tooltip,
  Typography,
  MenuItem,
} from "@mui/material";
import KeyboardArrowDownIcon from "@mui/icons-material/KeyboardArrowDown";
import KeyboardArrowUpIcon from "@mui/icons-material/KeyboardArrowUp";
import AddIcon from "@mui/icons-material/Add";
import EditIcon from "@mui/icons-material/Edit";
import DeleteIcon from "@mui/icons-material/Delete";
import SyncIcon from "@mui/icons-material/Sync";
import IntegrationInstructionsIcon from "@mui/icons-material/IntegrationInstructions";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import LanIcon from "@mui/icons-material/Lan";
import {
  useServices,
  useAddService,
  useEditService,
  useDeleteService,
  useServiceIntegration,
  useSettings,
  useZones,
  useAddDomainSSL,
  useRemoveDomainSSL,
} from "../api/hooks";
import { useSyncContext } from "../components/SyncProvider";
import type { Service, Zone } from "../api/types";
import type { ServiceMutationInput } from "../api/hooks";

function StatusDot({
  configured,
  detected,
  indeterminate,
  title,
}: {
  configured: boolean;
  detected: boolean;
  // Configured but no signal to confirm health (e.g. proxy with no health
  // check). Yellow rather than red — we can't honestly call it broken.
  indeterminate?: boolean;
  title?: string;
}) {
  let color: string;
  let opacity = 1;
  if (configured && detected) {
    color = "#2ecc71";
  } else if (configured && indeterminate) {
    color = "#f39c12";
  } else if (configured && !detected) {
    color = "#e74c3c";
  } else if (!configured && detected) {
    color = "#f39c12";
  } else {
    color = "#555";
    opacity = 0.5;
  }
  return (
    <Box
      component="span"
      title={title}
      sx={{
        display: "inline-block",
        width: 10,
        height: 10,
        borderRadius: "50%",
        bgcolor: color,
        opacity,
      }}
    />
  );
}

function DetailCard({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <Paper
      variant="outlined"
      sx={{ p: 2, bgcolor: "rgba(255,255,255,0.02)" }}
    >
      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ display: "block", mb: 1, textTransform: "uppercase", letterSpacing: 1 }}
      >
        {title}
      </Typography>
      {children}
    </Paper>
  );
}

// --- Service Form Dialog ---

interface ServiceFormState {
  name: string;
  domains: string;
  internalIP: string;
  externalEnabled: boolean;
  externalIPs: string;
  externalTTL: string;
  proxyEnabled: boolean;
  proxyBackend: string;
  healthCheckPath: string;
  internalOnly: boolean;
  deployEnabled: boolean;
  deployNextBackend: string;
  deployBalance: string;
  // Per-backend HAProxy timeout overrides, in seconds. Blank = inherit defaults.
  timeoutServer: string;
  timeoutConnect: string;
  timeoutTunnel: string;
}

const emptyForm: ServiceFormState = {
  name: "",
  domains: "",
  internalIP: "",
  externalEnabled: false,
  externalIPs: "",
  externalTTL: "300",
  proxyEnabled: false,
  proxyBackend: "",
  healthCheckPath: "",
  internalOnly: false,
  deployEnabled: false,
  deployNextBackend: "",
  deployBalance: "first",
  timeoutServer: "",
  timeoutConnect: "",
  timeoutTunnel: "",
};

function serviceToForm(svc: Service): ServiceFormState {
  return {
    name: svc.name,
    domains: svc.domains.join(", "),
    internalIP: svc.internalDNS?.ip ?? "",
    externalEnabled: !!svc.externalDNS,
    // Only the *configured* IPs round-trip into the form. The legacy fields
    // (`ip`/`ips`) on the API response are the resolved fallback, so using
    // them here would silently promote auto-detection into a per-service
    // static override on save — exactly the staleness bug we're avoiding.
    externalIPs: svc.externalDNS?.configuredIPs?.join(", ") ?? "",
    externalTTL: String(svc.externalDNS?.ttl ?? 300),
    proxyEnabled: !!svc.proxy,
    proxyBackend: svc.proxy?.backend ?? "",
    healthCheckPath: svc.proxy?.healthCheck?.path ?? "",
    internalOnly: svc.proxy?.internalOnly ?? false,
    deployEnabled: !!svc.proxy?.deploy,
    deployNextBackend: svc.proxy?.deploy?.nextBackend ?? "",
    deployBalance: svc.proxy?.deploy?.balance || "first",
    timeoutServer: svc.proxy?.timeouts?.serverSeconds
      ? String(svc.proxy.timeouts.serverSeconds)
      : "",
    timeoutConnect: svc.proxy?.timeouts?.connectSeconds
      ? String(svc.proxy.timeouts.connectSeconds)
      : "",
    timeoutTunnel: svc.proxy?.timeouts?.tunnelSeconds
      ? String(svc.proxy.timeouts.tunnelSeconds)
      : "",
  };
}

function formToInput(form: ServiceFormState, originalName?: string): ServiceMutationInput {
  const domains = form.domains
    .split(",")
    .map((d) => d.trim())
    .filter(Boolean);

  const input: ServiceMutationInput = {
    name: form.name,
    domains,
  };

  if (originalName) {
    input.originalName = originalName;
  }

  if (form.internalIP) {
    input.internalDNS = { ip: form.internalIP };
  }

  if (form.externalEnabled) {
    const ips = form.externalIPs
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    input.externalDNS = {
      ip: ips[0] ?? "",
      ips: ips.length > 0 ? ips : undefined,
      ttl: parseInt(form.externalTTL, 10) || 300,
    };
  }

  if (form.proxyEnabled && form.proxyBackend) {
    input.proxy = {
      backend: form.proxyBackend,
      internalOnly: form.internalOnly,
    };
    if (form.healthCheckPath) {
      input.proxy.healthCheck = { path: form.healthCheckPath };
    }
    if (form.deployEnabled && form.deployNextBackend) {
      input.proxy.deploy = {
        nextBackend: form.deployNextBackend,
        balance: form.deployBalance || "first",
      };
    }
    const timeouts: Record<string, number> = {};
    const server = parseInt(form.timeoutServer, 10);
    const connect = parseInt(form.timeoutConnect, 10);
    const tunnel = parseInt(form.timeoutTunnel, 10);
    if (server > 0) timeouts.serverSeconds = server;
    if (connect > 0) timeouts.connectSeconds = connect;
    if (tunnel > 0) timeouts.tunnelSeconds = tunnel;
    if (Object.keys(timeouts).length > 0) {
      input.proxy.timeouts = timeouts;
    }
  }

  return input;
}

// ExternalIPsField shows the External-DNS IP picker in two modes:
//   - Blank (auto): renders as a button labeled with the resolved fallback
//     IP. Clicking it opens the text field for an explicit override.
//   - Typed: renders as the text field with a "Use auto" affordance to
//     return to the fallback. Saving empty also reverts to auto.
// The empty-string is the persisted "use fallback" value; this component is
// purely presentation around that.
function ExternalIPsField({
  value,
  onChange,
  ttl,
  onTTLChange,
  publicIP,
  proxyEnabled,
  externalMismatch,
}: {
  value: string;
  onChange: (v: string) => void;
  ttl: string;
  onTTLChange: (v: string) => void;
  publicIP: string;
  proxyEnabled: boolean;
  externalMismatch: boolean;
}) {
  const [editing, setEditing] = useState(value.trim().length > 0);

  // If the parent clears the value (e.g. dialog re-opens for a different
  // service), collapse back to the button presentation.
  useEffect(() => {
    if (value.trim().length === 0) setEditing(false);
  }, [value]);

  const helperText = proxyEnabled
    ? `Leave empty for auto (${publicIP || "public IP"}) so requests reach this HAProxy host`
    : "Comma-separated. Multiple IPs enable round-robin DNS. Leave empty to use public IP.";

  return (
    <>
      {editing ? (
        <Box sx={{ display: "flex", gap: 2 }}>
          <TextField
            label="External IPs"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            size="small"
            fullWidth
            autoFocus
            placeholder={publicIP ? `auto: ${publicIP}` : "auto-detected"}
            helperText={helperText}
          />
          <TextField
            label="TTL"
            value={ttl}
            onChange={(e) => onTTLChange(e.target.value)}
            size="small"
            sx={{ width: 120 }}
            type="number"
          />
        </Box>
      ) : (
        <Box sx={{ display: "flex", gap: 2, alignItems: "flex-start" }}>
          <Box sx={{ flex: 1 }}>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}
            >
              External IPs
            </Typography>
            <Button
              variant="outlined"
              size="small"
              onClick={() => setEditing(true)}
              fullWidth
              sx={{
                fontFamily: "monospace",
                textTransform: "none",
                justifyContent: "flex-start",
              }}
            >
              Public IP: {publicIP || "auto-detected"}
            </Button>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ display: "block", mt: 0.5, ml: 1.75 }}
            >
              Click to set a static override
            </Typography>
          </Box>
          <TextField
            label="TTL"
            value={ttl}
            onChange={(e) => onTTLChange(e.target.value)}
            size="small"
            sx={{ width: 120, mt: 2.5 }}
            type="number"
          />
        </Box>
      )}
      {editing && externalMismatch && (
        <Alert severity="warning" sx={{ mt: -1 }}>
          External IPs don't include this HAProxy host's public IP
          (<code>{publicIP}</code>). External traffic for these domains
          won't hit the proxy. Clear the field to use auto.
        </Alert>
      )}
    </>
  );
}

// --- SSL coverage analysis ---
//
// SSL coverage is a zone-level concept: each Zone owns one wildcard cert whose
// SANs are derived from `zone.SubZones`. A given service domain is "covered"
// if its label is literally a SubZone, or if a wildcard SubZone on the same
// zone matches it. Cross-zone wildcard coverage doesn't exist (the backend
// only absorbs within a single zone).

type CoverageState = "noZone" | "explicit" | "covered" | "uncovered";

interface DomainCoverage {
  domain: string;
  state: CoverageState;
  zone?: Zone;
  // Wildcard pattern that covers this domain (only set when state="covered").
  coveredBy?: string;
}

// Mirror of internal/server/handlers_domains.go:domainMatchesPattern.
// *.example.com matches one label only (foo.example.com, not a.b.example.com).
function domainMatchesPattern(domain: string, pattern: string): boolean {
  if (domain === pattern) return true;
  if (pattern.startsWith("*.")) {
    const suffix = pattern.slice(1);
    if (domain.endsWith(suffix)) {
      const prefix = domain.slice(0, -suffix.length);
      return prefix.length > 0 && !prefix.includes(".");
    }
  }
  return false;
}

function analyzeDomainCoverage(raw: string, zones: Zone[]): DomainCoverage | null {
  const domain = raw.trim().toLowerCase();
  if (!domain) return null;

  const isWildcard = domain.startsWith("*.");
  const check = isWildcard ? domain.slice(2) : domain;

  // Match the backend's GetZoneForDomain: first zone whose name equals the
  // domain or is a parent suffix. Picking a different zone here would mean
  // the addDomainSSL mutation lands in a zone the UI didn't analyze.
  const zone = zones.find((z) => z.name === check || check.endsWith("." + z.name));
  if (!zone) return { domain, state: "noZone" };

  let subZoneLabel: string;
  if (isWildcard) {
    subZoneLabel = check === zone.name ? "*" : "*." + check.slice(0, -(zone.name.length + 1));
  } else if (check === zone.name) {
    subZoneLabel = "";
  } else {
    subZoneLabel = check.slice(0, -(zone.name.length + 1));
  }

  const subZones = zone.subZones ?? [];
  if (subZones.includes(subZoneLabel)) {
    return { domain, state: "explicit", zone };
  }
  for (const sz of subZones) {
    if (!sz.startsWith("*")) continue;
    const pattern = sz === "*" ? "*." + zone.name : sz + "." + zone.name;
    if (domainMatchesPattern(domain, pattern)) {
      return { domain, state: "covered", zone, coveredBy: pattern };
    }
  }
  return { domain, state: "uncovered", zone };
}

// DomainCoverageRow shows one parsed domain with its current SSL state and a
// toggle. `isOrphanRisk` is true when toggling on would write a SubZone to the
// zone for a domain that isn't yet a service domain anywhere — cancelling the
// form afterwards leaves a dangling SAN.
function DomainCoverageRow({
  cov,
  zoneSSLOff,
  isOrphanRisk,
  onAdd,
  onRemove,
  isPending,
  error,
}: {
  cov: DomainCoverage;
  zoneSSLOff: boolean;
  isOrphanRisk: boolean;
  onAdd: () => void;
  onRemove: () => void;
  isPending: boolean;
  error: string | null;
}) {
  const canToggle = cov.state === "explicit" || cov.state === "uncovered";
  const sslOn = cov.state === "explicit" || cov.state === "covered";

  let statusLabel: string;
  let statusColor: "success" | "warning" | "error" | "default";
  let helperText: string | null = null;
  if (cov.state === "noZone") {
    statusLabel = "no matching zone";
    statusColor = "error";
    helperText = "Add this domain's zone on the Domains page to enable SSL.";
  } else if (cov.state === "explicit") {
    statusLabel = `SubZone of ${cov.zone!.name}`;
    statusColor = "success";
  } else if (cov.state === "covered") {
    statusLabel = `covered by ${cov.coveredBy}`;
    statusColor = "success";
  } else {
    statusLabel = `zone ${cov.zone!.name}`;
    statusColor = "warning";
  }

  const tooltip =
    cov.state === "noZone"
      ? "No matching zone — create one first"
      : cov.state === "covered"
      ? `Already covered by ${cov.coveredBy}; remove that wildcard to manage this domain individually`
      : cov.state === "explicit"
      ? "Remove this domain's SubZone (affects the zone immediately)"
      : "Add this domain as a SubZone (affects the zone immediately)";

  return (
    <Box sx={{ display: "flex", alignItems: "flex-start", gap: 1, py: 0.25 }}>
      <Box sx={{ flex: 1, minWidth: 0 }}>
        <Typography variant="body2" sx={{ fontFamily: "monospace", wordBreak: "break-all" }}>
          {cov.domain}
        </Typography>
        <Box sx={{ display: "flex", gap: 0.5, mt: 0.25, flexWrap: "wrap" }}>
          <Chip size="small" label={statusLabel} color={statusColor} variant="outlined" />
          {zoneSSLOff && (
            <Chip size="small" label="zone SSL disabled" color="warning" variant="outlined" />
          )}
        </Box>
        {helperText && (
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
            {helperText}
          </Typography>
        )}
        {isOrphanRisk && (
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
            New domain — toggling SSL writes a SubZone to{" "}
            <code>{cov.zone!.name}</code> immediately, even if you cancel this dialog.
          </Typography>
        )}
        {error && (
          <Typography variant="caption" color="error.main" sx={{ display: "block", mt: 0.25 }}>
            {error}
          </Typography>
        )}
      </Box>
      <Tooltip title={tooltip}>
        <span>
          <Switch
            size="small"
            checked={sslOn}
            disabled={!canToggle || isPending}
            onChange={() => (cov.state === "explicit" ? onRemove() : onAdd())}
          />
        </span>
      </Tooltip>
    </Box>
  );
}

function DomainCoverageList({
  domainsText,
  zones,
  savedDomains,
}: {
  domainsText: string;
  zones: Zone[];
  // Every domain currently bound to any saved service. Used to detect the
  // orphan case: a domain that isn't on any service yet would leave a SAN
  // without an owner if the form is cancelled after toggling SSL on.
  savedDomains: Set<string>;
}) {
  const addSSL = useAddDomainSSL();
  const removeSSL = useRemoveDomainSSL();
  // Per-domain error so a failure on one row doesn't blank the others.
  const [errors, setErrors] = useState<Record<string, string>>({});

  const rows = useMemo(() => {
    const seen = new Set<string>();
    const out: DomainCoverage[] = [];
    for (const raw of domainsText.split(",")) {
      const d = raw.trim().toLowerCase();
      if (!d || seen.has(d)) continue;
      seen.add(d);
      const cov = analyzeDomainCoverage(d, zones);
      if (cov) out.push(cov);
    }
    return out;
  }, [domainsText, zones]);

  if (rows.length === 0) return null;

  const clearError = (d: string) =>
    setErrors((e) => {
      if (!(d in e)) return e;
      const next = { ...e };
      delete next[d];
      return next;
    });

  const setError = (d: string, msg: string) =>
    setErrors((e) => ({ ...e, [d]: msg }));

  const handleAdd = (cov: DomainCoverage) => {
    clearError(cov.domain);
    addSSL.mutate(cov.domain, {
      onError: (err: unknown) =>
        setError(cov.domain, err instanceof Error ? err.message : String(err)),
    });
  };
  const handleRemove = (cov: DomainCoverage) => {
    clearError(cov.domain);
    removeSSL.mutate(cov.domain, {
      onError: (err: unknown) =>
        setError(cov.domain, err instanceof Error ? err.message : String(err)),
    });
  };

  return (
    <Box>
      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}
      >
        SSL coverage
      </Typography>
      <Stack spacing={0.25}>
        {rows.map((cov) => (
          <DomainCoverageRow
            key={cov.domain}
            cov={cov}
            zoneSSLOff={!!cov.zone && !cov.zone.sslEnabled}
            isOrphanRisk={cov.state === "uncovered" && !savedDomains.has(cov.domain)}
            onAdd={() => handleAdd(cov)}
            onRemove={() => handleRemove(cov)}
            isPending={addSSL.isPending || removeSSL.isPending}
            error={errors[cov.domain] ?? null}
          />
        ))}
      </Stack>
    </Box>
  );
}

function ServiceFormDialog({
  open,
  onClose,
  onSubmit,
  isSubmitting,
  initialValues,
  title,
  localInterface,
  publicIP,
}: {
  open: boolean;
  onClose: () => void;
  onSubmit: (form: ServiceFormState) => void;
  isSubmitting: boolean;
  initialValues: ServiceFormState;
  title: string;
  localInterface: string;
  publicIP: string;
}) {
  const [form, setForm] = useState<ServiceFormState>(initialValues);
  // Timeouts start expanded only when the service already overrides one.
  const [timeoutsOpen, setTimeoutsOpen] = useState(
    !!(initialValues.timeoutServer || initialValues.timeoutConnect || initialValues.timeoutTunnel),
  );
  const zonesQuery = useZones();
  const servicesQuery = useServices();
  // Set of domains owned by any saved service. Used downstream to detect when
  // toggling SSL on a typed-but-unsaved domain would leave an orphan SubZone.
  const savedDomains = useMemo(() => {
    const set = new Set<string>();
    for (const svc of servicesQuery.data ?? []) {
      for (const d of svc.domains) set.add(d.toLowerCase());
    }
    return set;
  }, [servicesQuery.data]);

  // Reset form when dialog opens with new values
  const prevOpen = useState(open)[0];
  if (open && !prevOpen) {
    // handled by key prop on caller
  }

  const update = useCallback(
    <K extends keyof ServiceFormState>(key: K, val: ServiceFormState[K]) =>
      setForm((f) => ({ ...f, [key]: val })),
    [],
  );

  // Sync form with initialValues when they change (dialog open)
  useState(() => setForm(initialValues));

  // When proxy is enabled and internal IP is blank, default it to the HAProxy
  // host's LAN IP — otherwise traffic skips the proxy entirely.
  useEffect(() => {
    if (form.proxyEnabled && !form.internalIP && localInterface) {
      setForm((f) => ({ ...f, internalIP: localInterface }));
    }
  }, [form.proxyEnabled, form.internalIP, localInterface]);

  const internalMismatch =
    form.proxyEnabled &&
    !!form.internalIP &&
    !!localInterface &&
    form.internalIP !== localInterface;

  const externalIPList = form.externalIPs
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  // Empty externalIPs is treated as "auto" (resolves to the host's PublicIP),
  // which is fine. Only warn when the user has typed IPs and the public one
  // isn't among them.
  const externalMismatch =
    form.proxyEnabled &&
    form.externalEnabled &&
    !!publicIP &&
    externalIPList.length > 0 &&
    !externalIPList.includes(publicIP);

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Name"
          value={form.name}
          onChange={(e) => update("name", e.target.value)}
          size="small"
          required
          fullWidth
        />
        <TextField
          label="Domains (comma-separated)"
          value={form.domains}
          onChange={(e) => update("domains", e.target.value)}
          size="small"
          required
          fullWidth
          helperText="e.g. grafana.example.com, monitor.example.com"
        />
        {zonesQuery.data && (
          <DomainCoverageList
            domainsText={form.domains}
            zones={zonesQuery.data}
            savedDomains={savedDomains}
          />
        )}
        <TextField
          label="Internal DNS IP"
          value={form.internalIP}
          onChange={(e) => update("internalIP", e.target.value)}
          size="small"
          fullWidth
          helperText={
            form.proxyEnabled
              ? `Should be the HAProxy host (${localInterface || "unknown"}) so requests route through the proxy`
              : "Leave empty to disable internal DNS"
          }
        />
        {internalMismatch && (
          <Alert severity="warning" sx={{ mt: -1 }}>
            Internal DNS IP <code>{form.internalIP}</code> doesn't match this
            HAProxy host (<code>{localInterface}</code>). Requests for these
            domains will bypass the proxy.
          </Alert>
        )}

        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <Switch
            checked={form.externalEnabled}
            onChange={(e) => update("externalEnabled", e.target.checked)}
            size="small"
          />
          <Typography variant="body2">External DNS</Typography>
        </Box>
        {form.externalEnabled && (
          <ExternalIPsField
            value={form.externalIPs}
            onChange={(v) => update("externalIPs", v)}
            ttl={form.externalTTL}
            onTTLChange={(v) => update("externalTTL", v)}
            publicIP={publicIP}
            proxyEnabled={form.proxyEnabled}
            externalMismatch={externalMismatch}
          />
        )}

        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <Switch
            checked={form.proxyEnabled}
            onChange={(e) => update("proxyEnabled", e.target.checked)}
            size="small"
          />
          <Typography variant="body2">Proxy</Typography>
        </Box>
        {form.proxyEnabled && (
          <>
            <TextField
              label="Backend (host:port)"
              value={form.proxyBackend}
              onChange={(e) => update("proxyBackend", e.target.value)}
              size="small"
              fullWidth
            />
            <TextField
              label="Health Check Path"
              value={form.healthCheckPath}
              onChange={(e) => update("healthCheckPath", e.target.value)}
              size="small"
              fullWidth
              helperText="e.g. /health"
            />
            <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
              <Switch
                checked={form.internalOnly}
                onChange={(e) => update("internalOnly", e.target.checked)}
                size="small"
              />
              <Typography variant="body2">Internal only</Typography>
            </Box>

            <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
              <Switch
                checked={form.deployEnabled}
                onChange={(e) => update("deployEnabled", e.target.checked)}
                size="small"
              />
              <Typography variant="body2">Blue-green deploy</Typography>
            </Box>
            {form.deployEnabled && (
              <Box sx={{ display: "flex", gap: 2 }}>
                <TextField
                  label="Next backend (host:port)"
                  value={form.deployNextBackend}
                  onChange={(e) => update("deployNextBackend", e.target.value)}
                  size="small"
                  fullWidth
                  helperText="Slot B address. Slot A uses the proxy backend above."
                />
                <TextField
                  label="Balance"
                  value={form.deployBalance}
                  onChange={(e) => update("deployBalance", e.target.value)}
                  size="small"
                  select
                  sx={{ minWidth: 160 }}
                  helperText="Routing strategy"
                >
                  <MenuItem value="first">Active/Standby</MenuItem>
                  <MenuItem value="roundrobin">Round Robin</MenuItem>
                </TextField>
              </Box>
            )}

            <Box>
              <Box
                onClick={() => setTimeoutsOpen((o) => !o)}
                sx={{
                  display: "flex",
                  alignItems: "center",
                  gap: 0.5,
                  cursor: "pointer",
                  userSelect: "none",
                }}
              >
                <IconButton size="small" sx={{ p: 0.25 }}>
                  {timeoutsOpen ? (
                    <KeyboardArrowUpIcon fontSize="small" />
                  ) : (
                    <KeyboardArrowDownIcon fontSize="small" />
                  )}
                </IconButton>
                <Typography variant="body2">Timeouts</Typography>
                <Typography variant="caption" color="text.secondary">
                  override HAProxy defaults
                </Typography>
              </Box>
              <Collapse in={timeoutsOpen} timeout="auto" unmountOnExit>
                <Box sx={{ display: "flex", gap: 2, mt: 1 }}>
                  <TextField
                    label="Server"
                    value={form.timeoutServer}
                    onChange={(e) => update("timeoutServer", e.target.value)}
                    size="small"
                    type="number"
                    fullWidth
                    helperText="Default 50s"
                  />
                  <TextField
                    label="Connect"
                    value={form.timeoutConnect}
                    onChange={(e) => update("timeoutConnect", e.target.value)}
                    size="small"
                    type="number"
                    fullWidth
                    helperText="Default 5s"
                  />
                  <TextField
                    label="Tunnel"
                    value={form.timeoutTunnel}
                    onChange={(e) => update("timeoutTunnel", e.target.value)}
                    size="small"
                    type="number"
                    fullWidth
                    helperText="WebSocket"
                  />
                </Box>
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ display: "block", mt: 0.5 }}
                >
                  Seconds. Leave blank to inherit HAProxy defaults. Raise Server for
                  long-running backend requests.
                </Typography>
              </Collapse>
            </Box>
          </>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={isSubmitting}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={() => onSubmit(form)}
          disabled={isSubmitting || !form.name || !form.domains}
        >
          {isSubmitting ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Delete Confirmation Dialog ---

function DeleteConfirmDialog({
  open,
  serviceName,
  onClose,
  onConfirm,
  isDeleting,
}: {
  open: boolean;
  serviceName: string;
  onClose: () => void;
  onConfirm: () => void;
  isDeleting: boolean;
}) {
  return (
    <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
      <DialogTitle>Delete Service</DialogTitle>
      <DialogContent>
        <Typography>
          Delete service <strong>{serviceName}</strong>? This cannot be undone.
        </Typography>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={isDeleting}>
          Cancel
        </Button>
        <Button
          variant="contained"
          color="error"
          onClick={onConfirm}
          disabled={isDeleting}
        >
          {isDeleting ? <CircularProgress size={20} /> : "Delete"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Integration Dialog ---

function IntegrationDialog({
  serviceName,
  open,
  onClose,
}: {
  serviceName: string;
  open: boolean;
  onClose: () => void;
}) {
  const { data, isLoading } = useServiceIntegration(open ? serviceName : "");
  const [copied, setCopied] = useState(false);

  const handleCopy = (text: string) => {
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const script = data
    ? [
        `# ${data.name} — Homelab Horizon Integration`,
        ``,
        `# Download hz-client`,
        `curl -sO "${data.baseURL}/admin/haproxy/hz-client"`,
        `chmod +x hz-client`,
        ``,
        `# Configure`,
        `export HZ_TOKEN="${data.token}"`,
        `export HZ_URL="${data.baseURL}"`,
        ``,
        `# --- IP Banning ---`,
        ``,
        `./hz-client bans                                               # List active bans`,
        `./hz-client ban 1.2.3.4 --timeout 3600 --reason "brute force"  # Ban an IP`,
        `./hz-client unban 1.2.3.4                                      # Unban an IP`,
        ...(data.hasDeploy
          ? [
              ``,
              `# --- Rolling Deploy ---`,
              ``,
              `./hz-client status              # Check deploy status`,
              `./hz-client rolling start       # Take next slot down — deploy new code to it`,
              `./hz-client rolling continue    # Bring next up, take current down — deploy to it`,
              `./hz-client rolling finalize    # Bring current up — both slots on new code`,
              ``,
              `# --- Maintenance Page (for incompatible upgrades) ---`,
              ``,
              `./hz-client maint-page set maintenance.html  # Serve custom 503 while down`,
              `./hz-client maint-page clear                 # Restore default after upgrade`,
            ]
          : []),
      ].join("\n")
    : "";

  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>
        Service Integration — {serviceName}
      </DialogTitle>
      <DialogContent>
        {isLoading ? (
          <Box sx={{ display: "flex", justifyContent: "center", py: 4 }}>
            <CircularProgress />
          </Box>
        ) : data ? (
          <>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Download and configure the hz-client to manage IP banning
              {data.hasDeploy ? ", rolling deploys, and maintenance pages" : ""} for this service.
            </Typography>
            <Box sx={{ position: "relative" }}>
              <IconButton
                size="small"
                onClick={() => handleCopy(script)}
                sx={{ position: "absolute", top: 8, right: 8, zIndex: 1 }}
                title="Copy to clipboard"
              >
                <ContentCopyIcon fontSize="small" />
              </IconButton>
              <Box
                component="pre"
                sx={{
                  bgcolor: "#0f3460",
                  color: "#eee",
                  p: 2,
                  borderRadius: 1,
                  overflow: "auto",
                  maxHeight: 500,
                  fontSize: "0.85rem",
                  fontFamily: "monospace",
                  whiteSpace: "pre",
                  m: 0,
                }}
              >
                {script}
              </Box>
            </Box>
          </>
        ) : null}
      </DialogContent>
      <DialogActions>
        {copied && (
          <Typography variant="body2" color="success.main" sx={{ mr: 2 }}>
            Copied!
          </Typography>
        )}
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Port Map Dialog ---
//
// Flat diagnostic view of every downstream host:port the proxy forwards to.
// One row per backend address — a blue-green service contributes two rows
// (current + next slot), since each slot is a distinct host:port we hit.

type PortMapOrderBy = "host" | "port" | "service";

interface PortMapRow {
  host: string;
  port: number; // NaN when the backend string has no parseable port
  portRaw: string;
  service: string;
  domains: string[];
  slot: string | null; // "current"/"next" for deploy services, else null
  state: string; // HAProxy server state: up/down/drain/maint, or ""
  up: boolean;
  // Configured but no health check and not confirmed up — yellow, not red.
  indeterminate: boolean;
}

// Split on the last colon so IPv4/hostnames work; IPv6 literals would need
// brackets, which the backend config doesn't use today.
function splitHostPort(addr: string): { host: string; port: number; portRaw: string } {
  const idx = addr.lastIndexOf(":");
  if (idx < 0) return { host: addr, port: NaN, portRaw: "" };
  const portRaw = addr.slice(idx + 1);
  return { host: addr.slice(0, idx), port: parseInt(portRaw, 10), portRaw };
}

function buildPortMapRows(services: Service[]): PortMapRow[] {
  const rows: PortMapRow[] = [];
  for (const svc of services) {
    if (!svc.proxy) continue;
    const hasDeploy = !!svc.proxy.deploy;
    const noCheck = !svc.proxy.healthCheck;
    rows.push({
      ...splitHostPort(svc.proxy.backend),
      service: svc.name,
      domains: svc.domains,
      slot: hasDeploy ? "current" : null,
      state: svc.status.proxyState ?? "",
      up: svc.status.proxyUp,
      indeterminate: !svc.status.proxyUp && noCheck,
    });
    if (hasDeploy && svc.proxy.deploy) {
      rows.push({
        ...splitHostPort(svc.proxy.deploy.nextBackend),
        service: svc.name,
        domains: svc.domains,
        slot: "next",
        state: svc.status.proxyNextState ?? "",
        up: svc.status.proxyNextState === "up",
        indeterminate: false,
      });
    }
  }
  return rows;
}

function stateColor(state: string): "success" | "error" | "warning" {
  return state === "up" ? "success" : state === "down" ? "error" : "warning";
}

function PortMapDialog({
  open,
  onClose,
  services,
}: {
  open: boolean;
  onClose: () => void;
  services: Service[];
}) {
  const [orderBy, setOrderBy] = useState<PortMapOrderBy>("host");
  const [order, setOrder] = useState<"asc" | "desc">("asc");
  const [filterHost, setFilterHost] = useState<string | null>(null);

  const rows = useMemo(() => buildPortMapRows(services), [services]);

  const sorted = useMemo(() => {
    const dir = order === "asc" ? 1 : -1;
    // NaN ports sort last (asc) via Infinity; string columns are case-folded.
    const keyOf = (r: PortMapRow): string | number =>
      orderBy === "port"
        ? Number.isNaN(r.port)
          ? Infinity
          : r.port
        : orderBy === "service"
        ? r.service.toLowerCase()
        : r.host.toLowerCase();
    return [...rows].sort((a, b) => {
      const av = keyOf(a);
      const bv = keyOf(b);
      let cmp = av < bv ? -1 : av > bv ? 1 : 0;
      if (cmp === 0) {
        // Stable, readable tiebreak: host then numeric port.
        cmp =
          a.host.localeCompare(b.host) ||
          (a.port || 0) - (b.port || 0) ||
          a.service.localeCompare(b.service);
      }
      return cmp * dir;
    });
  }, [rows, orderBy, order]);

  const visible = useMemo(
    () => (filterHost ? sorted.filter((r) => r.host === filterHost) : sorted),
    [sorted, filterHost],
  );

  const handleClose = () => {
    setFilterHost(null);
    onClose();
  };

  const handleSort = (col: PortMapOrderBy) => {
    if (orderBy === col) {
      setOrder((o) => (o === "asc" ? "desc" : "asc"));
    } else {
      setOrderBy(col);
      setOrder("asc");
    }
  };

  const sortableHead = (col: PortMapOrderBy, label: string, align?: "right") => (
    <TableCell align={align} sortDirection={orderBy === col ? order : false}>
      <TableSortLabel
        active={orderBy === col}
        direction={orderBy === col ? order : "asc"}
        onClick={() => handleSort(col)}
      >
        {label}
      </TableSortLabel>
    </TableCell>
  );

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="lg" fullWidth>
      <DialogTitle>Port Map</DialogTitle>
      <DialogContent>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Every downstream host:port the proxy forwards to. Sort by host, port,
          or service; click a host to filter.
        </Typography>
        {filterHost && (
          <Box sx={{ mb: 2 }}>
            <Chip
              label={`host: ${filterHost}`}
              onDelete={() => setFilterHost(null)}
              color="primary"
              variant="outlined"
              size="small"
            />
          </Box>
        )}
        {rows.length === 0 ? (
          <Typography variant="body2" color="text.secondary" sx={{ py: 4 }} align="center">
            No proxied services.
          </Typography>
        ) : (
          <TableContainer component={Paper} variant="outlined">
            <Table size="small">
              <TableHead>
                <TableRow>
                  {sortableHead("host", "Host")}
                  {sortableHead("port", "Port", "right")}
                  {sortableHead("service", "Service")}
                  <TableCell>Domains</TableCell>
                  <TableCell>Status</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {visible.map((row, i) => (
                  <TableRow key={`${row.service}-${row.slot ?? "single"}-${i}`} hover>
                    <TableCell>
                      <Box
                        component="button"
                        type="button"
                        onClick={() => setFilterHost(row.host)}
                        title="Filter by this host"
                        sx={{
                          all: "unset",
                          cursor: "pointer",
                          fontFamily: "monospace",
                          fontSize: "0.875rem",
                          color: "primary.main",
                          "&:hover": { textDecoration: "underline" },
                        }}
                      >
                        {row.host}
                      </Box>
                    </TableCell>
                    <TableCell align="right">
                      <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                        {row.portRaw || "—"}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <Typography variant="body2" sx={{ fontWeight: 600 }}>
                        {row.service}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                        {row.domains.map((d) => (
                          <Chip key={d} label={d} size="small" variant="outlined" />
                        ))}
                      </Box>
                    </TableCell>
                    <TableCell>
                      <Box sx={{ display: "flex", gap: 0.5, alignItems: "center" }}>
                        <StatusDot
                          configured
                          detected={row.up}
                          indeterminate={row.indeterminate}
                          title={row.indeterminate ? "No health check configured" : undefined}
                        />
                        {row.state && (
                          <Chip
                            label={row.state}
                            size="small"
                            variant="outlined"
                            color={stateColor(row.state)}
                          />
                        )}
                        {row.slot && (
                          <Chip label={row.slot} size="small" variant="outlined" />
                        )}
                      </Box>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={handleClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Service Row ---

function ServiceRow({
  service,
  zones,
  onEdit,
  onDelete,
}: {
  service: Service;
  zones: Zone[];
  onEdit: (svc: Service) => void;
  onDelete: (name: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [integrationOpen, setIntegrationOpen] = useState(false);

  const hasIntDNS = !!service.internalDNS;
  const hasExtDNS = !!service.externalDNS;
  const hasProxy = !!service.proxy;
  const hasDeploy = !!service.proxy?.deploy;
  // Without a health check HAProxy reports "no check" — not "down". Treat the
  // proxy status as indeterminate (yellow) instead of red.
  const proxyIndeterminate =
    hasProxy && !service.status.proxyUp && !service.proxy?.healthCheck;

  return (
    <>
      <TableRow
        hover
        onClick={() => setOpen(!open)}
        sx={{ cursor: "pointer", "& > *": { borderBottom: "unset" } }}
      >
        <TableCell sx={{ width: 40, p: 1 }}>
          <IconButton size="small">
            {open ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
          </IconButton>
        </TableCell>
        <TableCell>
          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            {service.name}
          </Typography>
        </TableCell>
        <TableCell>
          <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
            {service.domains.map((d) => {
              const cov = analyzeDomainCoverage(d, zones);
              // Green iff a cert will actually be issued: SubZone coverage
              // (explicit or absorbed by a wildcard) AND the zone has SSL
              // turned on. A SubZone in an SSL-disabled zone is dead weight.
              const sslOn =
                !!cov &&
                (cov.state === "explicit" || cov.state === "covered") &&
                !!cov.zone?.sslEnabled;
              return (
                <Chip
                  key={d}
                  label={d}
                  size="small"
                  variant="outlined"
                  color={sslOn ? "success" : "default"}
                />
              );
            })}
          </Box>
        </TableCell>
        <TableCell align="center">
          <StatusDot configured={hasIntDNS} detected={service.status.internalDNSUp} />
        </TableCell>
        <TableCell align="center">
          <StatusDot configured={hasExtDNS} detected={service.status.externalDNSUp} />
        </TableCell>
        <TableCell align="center">
          <StatusDot
            configured={hasProxy}
            detected={service.status.proxyUp}
            indeterminate={proxyIndeterminate}
            title={proxyIndeterminate ? "No health check configured" : undefined}
          />
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell sx={{ py: 0 }} colSpan={6}>
          <Collapse in={open} timeout="auto" unmountOnExit>
            <Box
              sx={{
                py: 2,
                display: "grid",
                gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr", md: "1fr 1fr 1fr" },
                gap: 2,
              }}
            >
              {(hasIntDNS || hasExtDNS || service.status.internalDNSUp || service.status.externalDNSUp) && (
                <DetailCard title="DNS">
                  <Box sx={{ display: "flex", gap: 1, alignItems: "center", mb: 0.5 }}>
                    <StatusDot configured={hasIntDNS} detected={service.status.internalDNSUp} />
                    <Typography variant="body2" sx={{ minWidth: 60 }}>Internal:</Typography>
                    {hasIntDNS ? (
                      <>
                        <Typography variant="body2"><code>{service.internalDNS!.ip}</code></Typography>
                        {service.status.internalDNSResolved && service.status.internalDNSResolved !== service.internalDNS!.ip ? (
                          <Typography variant="body2" color="error.main">
                            resolves to {service.status.internalDNSResolved}
                          </Typography>
                        ) : service.status.internalDNSUp ? (
                          <Typography variant="body2" color="success.main">ok</Typography>
                        ) : (
                          <Typography variant="body2" color="error.main">not resolving</Typography>
                        )}
                      </>
                    ) : service.status.internalDNSResolved ? (
                      <>
                        <Typography variant="body2" color="text.secondary">unconfigured</Typography>
                        <Typography variant="body2" color="warning.main">
                          resolves to {service.status.internalDNSResolved}
                        </Typography>
                      </>
                    ) : (
                      <Typography variant="body2" color="text.secondary">unconfigured</Typography>
                    )}
                  </Box>
                  <Box sx={{ display: "flex", gap: 1, alignItems: "center" }}>
                    <StatusDot configured={hasExtDNS} detected={service.status.externalDNSUp} />
                    <Typography variant="body2" sx={{ minWidth: 60 }}>External:</Typography>
                    {hasExtDNS ? (
                      <>
                        <Typography variant="body2"><code>{service.externalDNS!.ip || "auto"}</code></Typography>
                        {service.status.externalDNSResolved ? (
                          service.status.externalDNSResolved !== service.externalDNS!.ip && service.externalDNS!.ip ? (
                            <Typography variant="body2" color="error.main">
                              resolves to {service.status.externalDNSResolved}
                            </Typography>
                          ) : (
                            <Typography variant="body2" color="success.main">
                              {service.status.externalDNSResolved}
                            </Typography>
                          )
                        ) : (
                          <Typography variant="body2" color="error.main">not resolving</Typography>
                        )}
                      </>
                    ) : service.status.externalDNSResolved ? (
                      <>
                        <Typography variant="body2" color="text.secondary">unconfigured</Typography>
                        <Typography variant="body2" color="warning.main">
                          resolves to {service.status.externalDNSResolved}
                        </Typography>
                      </>
                    ) : (
                      <Typography variant="body2" color="text.secondary">unconfigured</Typography>
                    )}
                  </Box>
                </DetailCard>
              )}
              {hasProxy && (
                <DetailCard title="HAProxy">
                  <Box sx={{ display: "flex", gap: 1, alignItems: "center", mb: 0.5 }}>
                    <StatusDot
                      configured={true}
                      detected={service.status.proxyUp}
                      indeterminate={proxyIndeterminate}
                    />
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>
                      {hasDeploy ? "Current" : "Backend"}:
                    </Typography>
                    <Typography variant="body2"><code>{service.proxy!.backend}</code></Typography>
                    {service.status.proxyState && (
                      <Chip
                        label={service.status.proxyState}
                        size="small"
                        color={service.status.proxyState === "up" ? "success" : service.status.proxyState === "down" ? "error" : "warning"}
                        variant="outlined"
                      />
                    )}
                  </Box>
                  {hasDeploy && (
                    <Box sx={{ display: "flex", gap: 1, alignItems: "center", mb: 0.5 }}>
                      <StatusDot configured={true} detected={service.status.proxyNextState === "up"} />
                      <Typography variant="body2" sx={{ fontWeight: 600 }}>Next:</Typography>
                      <Typography variant="body2"><code>{service.proxy!.deploy!.nextBackend}</code></Typography>
                      {service.status.proxyNextState && (
                        <Chip
                          label={service.status.proxyNextState}
                          size="small"
                          color={service.status.proxyNextState === "up" ? "success" : service.status.proxyNextState === "down" ? "error" : "warning"}
                          variant="outlined"
                        />
                      )}
                      <Chip label={`slot ${service.proxy!.deploy!.activeSlot}`} size="small" variant="outlined" />
                      <Chip label={service.proxy!.deploy!.balance} size="small" variant="outlined" color="info" />
                    </Box>
                  )}
                  {service.status.proxyError && (
                    <Typography variant="body2" color="error.main" sx={{ mb: 0.5 }}>
                      {service.status.proxyError}
                    </Typography>
                  )}
                  {service.proxy!.healthCheck && (
                    <Typography variant="body2" color="text.secondary">
                      Health check: {service.proxy!.healthCheck.path}
                    </Typography>
                  )}
                  <Typography variant="body2" color="text.secondary">
                    {service.proxy!.internalOnly ? "Internal only" : "Public"}
                  </Typography>
                  {service.proxy!.timeouts && (
                    <Typography variant="body2" color="text.secondary">
                      Timeouts:{" "}
                      {[
                        service.proxy!.timeouts.serverSeconds &&
                          `server ${service.proxy!.timeouts.serverSeconds}s`,
                        service.proxy!.timeouts.connectSeconds &&
                          `connect ${service.proxy!.timeouts.connectSeconds}s`,
                        service.proxy!.timeouts.tunnelSeconds &&
                          `tunnel ${service.proxy!.timeouts.tunnelSeconds}s`,
                      ]
                        .filter(Boolean)
                        .join(", ")}
                    </Typography>
                  )}
                </DetailCard>
              )}
              {!hasIntDNS && !hasExtDNS && !hasProxy && (
                <Typography variant="body2" color="text.secondary">
                  No configuration details available.
                </Typography>
              )}
            </Box>
            <Box sx={{ display: "flex", gap: 1, pb: 2 }}>
              <Button
                size="small"
                variant="outlined"
                startIcon={<IntegrationInstructionsIcon />}
                onClick={(e) => {
                  e.stopPropagation();
                  setIntegrationOpen(true);
                }}
              >
                Integration
              </Button>
              <Button
                size="small"
                variant="outlined"
                startIcon={<EditIcon />}
                onClick={(e) => {
                  e.stopPropagation();
                  onEdit(service);
                }}
              >
                Edit
              </Button>
              <Button
                size="small"
                variant="outlined"
                color="error"
                startIcon={<DeleteIcon />}
                onClick={(e) => {
                  e.stopPropagation();
                  onDelete(service.name);
                }}
              >
                Delete
              </Button>
            </Box>
          </Collapse>
        </TableCell>
      </TableRow>
      <IntegrationDialog
        serviceName={service.name}
        open={integrationOpen}
        onClose={() => setIntegrationOpen(false)}
      />
    </>
  );
}

// --- Snackbar state ---

interface SnackState {
  open: boolean;
  message: string;
  severity: "success" | "error";
}

// --- Main page ---

function ServicesPage() {
  const { data, isLoading, error } = useServices();
  const { data: settings } = useSettings();
  const { data: zonesData } = useZones();
  const addMutation = useAddService();
  const editMutation = useEditService();
  const deleteMutation = useDeleteService();
  const { startSync } = useSyncContext();

  const localInterface = settings?.config?.localInterface ?? "";
  const publicIP = settings?.config?.publicIP ?? "";
  const zones = zonesData ?? [];

  const [addOpen, setAddOpen] = useState(false);
  const [portMapOpen, setPortMapOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<Service | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [snack, setSnack] = useState<SnackState>({ open: false, message: "", severity: "success" });

  const showSnack = (message: string, severity: "success" | "error") =>
    setSnack({ open: true, message, severity });

  const handleAdd = (form: ServiceFormState) => {
    addMutation.mutate(formToInput(form), {
      onSuccess: () => {
        setAddOpen(false);
        showSnack("Service added", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleEdit = (form: ServiceFormState) => {
    if (!editTarget) return;
    editMutation.mutate(formToInput(form, editTarget.name), {
      onSuccess: () => {
        setEditTarget(null);
        showSnack("Service updated", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleDelete = () => {
    if (!deleteTarget) return;
    deleteMutation.mutate(deleteTarget, {
      onSuccess: () => {
        setDeleteTarget(null);
        showSnack("Service deleted", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const [tab, setTab] = useState("all");

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">Failed to load services: {error.message}</Alert>;
  }

  const services = data ?? [];

  const hasError = (svc: Service) =>
    (svc.internalDNS && !svc.status.internalDNSUp) ||
    (svc.externalDNS && !svc.status.externalDNSUp) ||
    (svc.proxy && !svc.status.proxyUp && !!svc.proxy.healthCheck);

  const counts = {
    all: services.length,
    errors: services.filter(hasError).length,
    internal: services.filter((s) => s.internalDNS && !s.externalDNS).length,
    external: services.filter((s) => s.externalDNS).length,
    proxy: services.filter((s) => s.proxy).length,
  };

  const filtered = services.filter((svc) => {
    switch (tab) {
      case "errors": return hasError(svc);
      case "internal": return !!svc.internalDNS && !svc.externalDNS;
      case "external": return !!svc.externalDNS;
      case "proxy": return !!svc.proxy;
      default: return true;
    }
  });

  return (
    <Box>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          Services
        </Typography>
        <Box sx={{ display: "flex", gap: 1 }}>
          <Button
            variant="outlined"
            startIcon={<LanIcon />}
            onClick={() => setPortMapOpen(true)}
          >
            Port Map
          </Button>
          <Button
            variant="outlined"
            startIcon={<SyncIcon />}
            onClick={startSync}
          >
            Sync
          </Button>
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => setAddOpen(true)}
          >
            Add Service
          </Button>
        </Box>
      </Box>

      <Tabs
        value={tab}
        onChange={(_, v) => setTab(v)}
        sx={{ mb: 2 }}
      >
        <Tab label={`All (${counts.all})`} value="all" />
        {counts.errors > 0 && (
          <Tab
            label={`Errors (${counts.errors})`}
            value="errors"
            sx={{ color: "error.main" }}
          />
        )}
        <Tab label={`Internal (${counts.internal})`} value="internal" />
        <Tab label={`External (${counts.external})`} value="external" />
        <Tab label={`Proxy (${counts.proxy})`} value="proxy" />
      </Tabs>

      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell sx={{ width: 40 }} />
              <TableCell>Name</TableCell>
              <TableCell>Domains</TableCell>
              <TableCell align="center">Int DNS</TableCell>
              <TableCell align="center">Ext DNS</TableCell>
              <TableCell align="center">Proxy</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {filtered.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    {services.length === 0 ? "No services configured." : "No matching services."}
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              filtered.map((svc) => (
                <ServiceRow
                  key={svc.name}
                  service={svc}
                  zones={zones}
                  onEdit={setEditTarget}
                  onDelete={setDeleteTarget}
                />
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>

      {/* Port map dialog */}
      <PortMapDialog
        open={portMapOpen}
        onClose={() => setPortMapOpen(false)}
        services={services}
      />

      {/* Add dialog */}
      {addOpen && (
        <ServiceFormDialog
          open
          title="Add Service"
          initialValues={emptyForm}
          onClose={() => setAddOpen(false)}
          onSubmit={handleAdd}
          isSubmitting={addMutation.isPending}
          localInterface={localInterface}
          publicIP={publicIP}
        />
      )}

      {/* Edit dialog */}
      {editTarget && (
        <ServiceFormDialog
          open
          title="Edit Service"
          initialValues={serviceToForm(editTarget)}
          onClose={() => setEditTarget(null)}
          onSubmit={handleEdit}
          isSubmitting={editMutation.isPending}
          localInterface={localInterface}
          publicIP={publicIP}
        />
      )}

      {/* Delete confirmation */}
      <DeleteConfirmDialog
        open={!!deleteTarget}
        serviceName={deleteTarget ?? ""}
        onClose={() => setDeleteTarget(null)}
        onConfirm={handleDelete}
        isDeleting={deleteMutation.isPending}
      />

      {/* Snackbar feedback */}
      <Snackbar
        open={snack.open}
        autoHideDuration={4000}
        onClose={() => setSnack((s) => ({ ...s, open: false }))}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert
          severity={snack.severity}
          onClose={() => setSnack((s) => ({ ...s, open: false }))}
          variant="filled"
        >
          {snack.message}
        </Alert>
      </Snackbar>
    </Box>
  );
}

export const Route = createFileRoute("/services")({
  component: ServicesPage,
});

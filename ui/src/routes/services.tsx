import { useState, useCallback } from "react";
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
  Switch,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tab,
  Tabs,
  TextField,
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
import {
  useServices,
  useAddService,
  useEditService,
  useDeleteService,
  useServiceIntegration,
} from "../api/hooks";
import { useSyncContext } from "../components/SyncProvider";
import type { Service } from "../api/types";
import type { ServiceMutationInput } from "../api/hooks";

function StatusDot({
  configured,
  detected,
  title,
}: {
  configured: boolean;
  detected: boolean;
  title?: string;
}) {
  let color: string;
  let opacity = 1;
  if (configured && detected) {
    color = "#2ecc71";
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
};

function serviceToForm(svc: Service): ServiceFormState {
  return {
    name: svc.name,
    domains: svc.domains.join(", "),
    internalIP: svc.internalDNS?.ip ?? "",
    externalEnabled: !!svc.externalDNS,
    externalIPs: svc.externalDNS?.ips?.join(", ") ?? svc.externalDNS?.ip ?? "",
    externalTTL: String(svc.externalDNS?.ttl ?? 300),
    proxyEnabled: !!svc.proxy,
    proxyBackend: svc.proxy?.backend ?? "",
    healthCheckPath: svc.proxy?.healthCheck?.path ?? "",
    internalOnly: svc.proxy?.internalOnly ?? false,
    deployEnabled: !!svc.proxy?.deploy,
    deployNextBackend: svc.proxy?.deploy?.nextBackend ?? "",
    deployBalance: svc.proxy?.deploy?.balance || "first",
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
  }

  return input;
}

function ServiceFormDialog({
  open,
  onClose,
  onSubmit,
  isSubmitting,
  initialValues,
  title,
}: {
  open: boolean;
  onClose: () => void;
  onSubmit: (form: ServiceFormState) => void;
  isSubmitting: boolean;
  initialValues: ServiceFormState;
  title: string;
}) {
  const [form, setForm] = useState<ServiceFormState>(initialValues);

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
        <TextField
          label="Internal DNS IP"
          value={form.internalIP}
          onChange={(e) => update("internalIP", e.target.value)}
          size="small"
          fullWidth
          helperText="Leave empty to disable internal DNS"
        />

        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <Switch
            checked={form.externalEnabled}
            onChange={(e) => update("externalEnabled", e.target.checked)}
            size="small"
          />
          <Typography variant="body2">External DNS</Typography>
        </Box>
        {form.externalEnabled && (
          <Box sx={{ display: "flex", gap: 2 }}>
            <TextField
              label="External IPs"
              value={form.externalIPs}
              onChange={(e) => update("externalIPs", e.target.value)}
              size="small"
              fullWidth
              helperText="Comma-separated. Multiple IPs enable round-robin DNS. Leave empty to use public IP."
            />
            <TextField
              label="TTL"
              value={form.externalTTL}
              onChange={(e) => update("externalTTL", e.target.value)}
              size="small"
              sx={{ width: 120 }}
              type="number"
            />
          </Box>
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
              {data.hasDeploy ? " and rolling deploys" : ""} for this service.
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

// --- Service Row ---

function ServiceRow({
  service,
  onEdit,
  onDelete,
}: {
  service: Service;
  onEdit: (svc: Service) => void;
  onDelete: (name: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [integrationOpen, setIntegrationOpen] = useState(false);

  const hasIntDNS = !!service.internalDNS;
  const hasExtDNS = !!service.externalDNS;
  const hasProxy = !!service.proxy;
  const hasDeploy = !!service.proxy?.deploy;

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
            {service.domains.map((d) => (
              <Chip key={d} label={d} size="small" variant="outlined" />
            ))}
          </Box>
        </TableCell>
        <TableCell align="center">
          <StatusDot configured={hasIntDNS} detected={service.status.internalDNSUp} />
        </TableCell>
        <TableCell align="center">
          <StatusDot configured={hasExtDNS} detected={service.status.externalDNSUp} />
        </TableCell>
        <TableCell align="center">
          <StatusDot configured={hasProxy} detected={service.status.proxyUp} />
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
                    <StatusDot configured={true} detected={service.status.proxyUp} />
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
  const addMutation = useAddService();
  const editMutation = useEditService();
  const deleteMutation = useDeleteService();
  const { startSync } = useSyncContext();

  const [addOpen, setAddOpen] = useState(false);
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
    (svc.proxy && !svc.status.proxyUp);

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
                  onEdit={setEditTarget}
                  onDelete={setDeleteTarget}
                />
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>

      {/* Add dialog */}
      {addOpen && (
        <ServiceFormDialog
          open
          title="Add Service"
          initialValues={emptyForm}
          onClose={() => setAddOpen(false)}
          onSubmit={handleAdd}
          isSubmitting={addMutation.isPending}
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

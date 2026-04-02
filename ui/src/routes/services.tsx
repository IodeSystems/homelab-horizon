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
  TextField,
  Typography,
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

function StatusDot({ active }: { active: boolean }) {
  return (
    <Box
      component="span"
      sx={{
        display: "inline-block",
        width: 10,
        height: 10,
        borderRadius: "50%",
        bgcolor: active ? "success.main" : "text.secondary",
        opacity: active ? 1 : 0.4,
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
  externalIP: string;
  externalTTL: string;
  proxyEnabled: boolean;
  proxyBackend: string;
  healthCheckPath: string;
  internalOnly: boolean;
}

const emptyForm: ServiceFormState = {
  name: "",
  domains: "",
  internalIP: "",
  externalEnabled: false,
  externalIP: "",
  externalTTL: "300",
  proxyEnabled: false,
  proxyBackend: "",
  healthCheckPath: "",
  internalOnly: false,
};

function serviceToForm(svc: Service): ServiceFormState {
  return {
    name: svc.name,
    domains: svc.domains.join(", "),
    internalIP: svc.internalDNS?.ip ?? "",
    externalEnabled: !!svc.externalDNS,
    externalIP: svc.externalDNS?.ip ?? "",
    externalTTL: String(svc.externalDNS?.ttl ?? 300),
    proxyEnabled: !!svc.proxy,
    proxyBackend: svc.proxy?.backend ?? "",
    healthCheckPath: svc.proxy?.healthCheck?.path ?? "",
    internalOnly: svc.proxy?.internalOnly ?? false,
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
    input.externalDNS = {
      ip: form.externalIP,
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
              label="External IP"
              value={form.externalIP}
              onChange={(e) => update("externalIP", e.target.value)}
              size="small"
              fullWidth
              helperText="Leave empty to use public IP"
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
        `# Base URL: ${data.baseURL}`,
        ``,
        `TOKEN="${data.token}"`,
        `BASE="${data.baseURL}"`,
        ``,
        `# --- IP Banning ---`,
        ``,
        `# Ban an IP (timeout in seconds, 0 = permanent)`,
        `curl -X POST "$BASE/api/ban/ban" \\`,
        `  -H "Authorization: Bearer $TOKEN" \\`,
        `  -H "Content-Type: application/json" \\`,
        `  -d '{"ip":"1.2.3.4","timeout":3600,"reason":"brute force"}'`,
        ``,
        `# Unban an IP`,
        `curl -X POST "$BASE/api/ban/unban" \\`,
        `  -H "Authorization: Bearer $TOKEN" \\`,
        `  -H "Content-Type: application/json" \\`,
        `  -d '{"ip":"1.2.3.4"}'`,
        ``,
        `# List active bans`,
        `curl -s "$BASE/api/ban/list" \\`,
        `  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool`,
        ...(data.hasDeploy
          ? [
              ``,
              `# --- Blue-Green Deploy ---`,
              ``,
              `# Download deploy control script`,
              `curl -sO "$BASE/admin/haproxy/deploy-script"`,
              `chmod +x deploy-service`,
              ``,
              `# Check deploy status`,
              `curl -s "$BASE/api/deploy/status" \\`,
              `  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool`,
              ``,
              `# Swap active/next slots`,
              `curl -X POST "$BASE/api/deploy/swap" \\`,
              `  -H "Authorization: Bearer $TOKEN"`,
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
              Use these commands to integrate your service with Homelab Horizon.
              The token authenticates your service for IP banning
              {data.hasDeploy ? " and blue-green deploy" : ""} operations.
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
          <StatusDot active={hasIntDNS} />
        </TableCell>
        <TableCell align="center">
          <StatusDot active={hasExtDNS} />
        </TableCell>
        <TableCell align="center">
          <StatusDot active={hasProxy} />
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
              {hasIntDNS && (
                <DetailCard title="Internal DNS">
                  <Typography variant="body2">
                    IP: {service.internalDNS!.ip}
                  </Typography>
                </DetailCard>
              )}
              {hasExtDNS && (
                <DetailCard title="External DNS">
                  <Typography variant="body2">
                    IP: {service.externalDNS!.ip}
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    TTL: {service.externalDNS!.ttl}
                  </Typography>
                </DetailCard>
              )}
              {hasProxy && (
                <DetailCard title="Proxy">
                  <Typography variant="body2">
                    Backend: {service.proxy!.backend}
                  </Typography>
                  {service.proxy!.healthCheck && (
                    <Typography variant="body2" color="text.secondary">
                      Health: {service.proxy!.healthCheck.path}
                    </Typography>
                  )}
                  <Typography variant="body2" color="text.secondary">
                    {service.proxy!.internalOnly ? "Internal only" : "Public"}
                  </Typography>
                </DetailCard>
              )}
              {hasDeploy && (
                <DetailCard title="Deploy">
                  <Typography variant="body2">
                    Active: {service.proxy!.deploy!.activeSlot}
                  </Typography>
                  <Typography variant="body2">
                    Next: {service.proxy!.deploy!.nextBackend}
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    Balance: {service.proxy!.deploy!.balance}
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

  const handleSync = () => {
    startSync();
  };

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
            onClick={handleSync}
          >
            Sync All
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
            {services.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No services configured.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              services.map((svc) => (
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

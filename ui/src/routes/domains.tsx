import { useState } from "react";
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
import WarningIcon from "@mui/icons-material/Warning";
import SyncIcon from "@mui/icons-material/Sync";
import HttpsIcon from "@mui/icons-material/Https";
import AddIcon from "@mui/icons-material/Add";
import DeleteIcon from "@mui/icons-material/Delete";
import {
  useDomains,
  useSyncDNS,
  useSyncAllDNS,
  useAddDomainSSL,
  useRemoveDomainSSL,
} from "../api/hooks";
import type { DomainAnalysis } from "../api/types";

/**
 * 4-state status dot:
 *   gray  = not configured, not detected
 *   yellow = not configured, but detected (unexpected presence)
 *   green  = configured and present/working
 *   red    = configured but not present/broken
 */
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
    color = "#2ecc71"; // green
  } else if (configured && !detected) {
    color = "#e74c3c"; // red
  } else if (!configured && detected) {
    color = "#f39c12"; // yellow
  } else {
    color = "#555"; // gray
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

function SummaryChip({ label, count }: { label: string; count: number }) {
  return (
    <Chip
      label={`${label}: ${count}`}
      size="small"
      variant="outlined"
      sx={{ fontVariantNumeric: "tabular-nums" }}
    />
  );
}

interface SnackState {
  open: boolean;
  message: string;
  severity: "success" | "error";
}

function DomainRow({
  domain,
  onSnack,
}: {
  domain: DomainAnalysis;
  onSnack: (message: string, severity: "success" | "error") => void;
}) {
  const [open, setOpen] = useState(false);
  const syncDNS = useSyncDNS();
  const addSSL = useAddDomainSSL();
  const removeSSL = useRemoveDomainSSL();

  const handleSyncDNS = (e: React.MouseEvent) => {
    e.stopPropagation();
    syncDNS.mutate(domain.domain, {
      onSuccess: (data) => {
        onSnack(
          data.changed
            ? `DNS record updated for ${domain.domain}`
            : `DNS already up to date for ${domain.domain}`,
          "success",
        );
      },
      onError: (err) => onSnack(err.message, "error"),
    });
  };

  const handleAddSSL = (e: React.MouseEvent) => {
    e.stopPropagation();
    addSSL.mutate(domain.domain, {
      onSuccess: () =>
        onSnack(
          `SSL coverage added for ${domain.domain}. Sync will request the certificate.`,
          "success",
        ),
      onError: (err) => onSnack(err.message, "error"),
    });
  };

  const handleRemoveSSL = (e: React.MouseEvent) => {
    e.stopPropagation();
    removeSSL.mutate(domain.domain, {
      onSuccess: () =>
        onSnack(`SSL coverage removed for ${domain.domain}`, "success"),
      onError: (err) => onSnack(err.message, "error"),
    });
  };

  const anyPending =
    syncDNS.isPending || addSSL.isPending || removeSSL.isPending;

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
            {domain.domain}
          </Typography>
        </TableCell>
        <TableCell>
          <Typography variant="body2" color="text.secondary">
            {domain.zoneName}
          </Typography>
        </TableCell>
        <TableCell align="center">
          <StatusDot
            configured={domain.hasInternalDNS}
            detected={!!domain.dnsmasqResolvedIP}
            title={domain.hasInternalDNS
              ? domain.dnsmasqResolvedIP ? `${domain.internalIP} → ${domain.dnsmasqResolvedIP}` : `${domain.internalIP} (not resolving)`
              : domain.dnsmasqResolvedIP ? `Resolves to ${domain.dnsmasqResolvedIP} (not configured)` : undefined}
          />
        </TableCell>
        <TableCell align="center">
          <StatusDot
            configured={domain.hasExternalDNS}
            detected={!!domain.remoteResolvedIP}
            title={domain.hasExternalDNS
              ? domain.remoteResolvedIP ? `${domain.externalIP} → ${domain.remoteResolvedIP}` : `${domain.externalIP} (not resolving)`
              : domain.remoteResolvedIP ? `Resolves to ${domain.remoteResolvedIP} (not configured)` : undefined}
          />
        </TableCell>
        <TableCell align="center">
          <StatusDot configured={domain.hasProxy} detected={domain.hasProxy} />
        </TableCell>
        <TableCell align="center">
          <StatusDot
            configured={domain.hasSSLCoverage}
            detected={domain.certExists}
            title={domain.hasSSLCoverage
              ? domain.certExists ? `Covered by ${domain.certDomain}` : `Covered by ${domain.certDomain} (no cert on disk)`
              : domain.certExists ? "Cert exists but not in SubZones" : undefined}
          />
        </TableCell>
        <TableCell align="right" sx={{ p: 0.5 }}>
          {!domain.hasService && domain.hasSSLCoverage && (
            <IconButton
              size="small"
              color="error"
              title="Remove SSL coverage"
              onClick={(e) => { e.stopPropagation(); handleRemoveSSL(e); }}
              disabled={anyPending}
            >
              <DeleteIcon fontSize="small" />
            </IconButton>
          )}
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell sx={{ py: 0 }} colSpan={8}>
          <Collapse in={open} timeout="auto" unmountOnExit>
            <Box
              sx={{
                py: 2,
                display: "grid",
                gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr", md: "1fr 1fr 1fr" },
                gap: 2,
              }}
            >
              <Paper variant="outlined" sx={{ p: 2, bgcolor: "rgba(255,255,255,0.02)" }}>
                <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1, textTransform: "uppercase", letterSpacing: 1 }}>
                  Zone
                </Typography>
                <Typography variant="body2">Zone: {domain.zoneName}</Typography>
                <Typography variant="body2">Service: {domain.serviceName}</Typography>
                <Typography variant="body2" color="text.secondary">
                  Zone SSL: <StatusDot configured={domain.zoneHasSSL} detected={domain.zoneHasSSL} /> {domain.zoneHasSSL ? "Enabled" : "Disabled"}
                </Typography>
              </Paper>

              <Paper variant="outlined" sx={{ p: 2, bgcolor: "rgba(255,255,255,0.02)" }}>
                <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1, textTransform: "uppercase", letterSpacing: 1 }}>
                  DNS Resolution
                </Typography>
                {domain.hasInternalDNS && (
                  <Typography variant="body2">Internal IP: {domain.internalIP}</Typography>
                )}
                {domain.hasExternalDNS && (
                  <Typography variant="body2">External IP: {domain.externalIP}</Typography>
                )}
                {domain.dnsmasqResolvedIP && (
                  <Typography variant="body2">
                    Dnsmasq: {domain.dnsmasqResolvedIP}{" "}
                    {domain.dnsmasqDNSMatch ? (
                      <Chip label="match" size="small" color="success" sx={{ height: 18 }} />
                    ) : (
                      <Chip label="mismatch" size="small" color="error" sx={{ height: 18 }} />
                    )}
                  </Typography>
                )}
                {domain.remoteResolvedIP && (
                  <Typography variant="body2">
                    Remote: {domain.remoteResolvedIP}{" "}
                    {domain.remoteDNSMatch ? (
                      <Chip label="match" size="small" color="success" sx={{ height: 18 }} />
                    ) : (
                      <Chip label="mismatch" size="small" color="error" sx={{ height: 18 }} />
                    )}
                  </Typography>
                )}
              </Paper>

              <Paper variant="outlined" sx={{ p: 2, bgcolor: "rgba(255,255,255,0.02)" }}>
                <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1, textTransform: "uppercase", letterSpacing: 1 }}>
                  SSL / Proxy
                </Typography>
                {domain.hasProxy && (
                  <>
                    <Typography variant="body2">Backend: {domain.proxyBackend}</Typography>
                    <Typography variant="body2" color="text.secondary">
                      {domain.internalOnly ? "Internal only" : "Public"}
                    </Typography>
                    {domain.hasHealthCheck && (
                      <Typography variant="body2" color="text.secondary">
                        Health: {domain.healthPath}
                      </Typography>
                    )}
                  </>
                )}
                {domain.certExists && (
                  <>
                    <Typography variant="body2">Cert domain: {domain.certDomain}</Typography>
                    <Typography variant="body2" color="text.secondary">
                      Expires: {domain.certExpiry}
                    </Typography>
                  </>
                )}
                {!domain.hasProxy && !domain.certExists && (
                  <Typography variant="body2" color="text.secondary">
                    No proxy or SSL configured.
                  </Typography>
                )}
              </Paper>
            </Box>

            {/* Action buttons */}
            {(domain.canSyncDNS || domain.canEnableHTTPS || (domain.hasSSLCoverage && !domain.hasService)) && (
              <Box sx={{ display: "flex", gap: 1, pb: 2, flexWrap: "wrap" }}>
                {domain.canSyncDNS && (
                  <Button
                    size="small"
                    variant="outlined"
                    startIcon={syncDNS.isPending ? <CircularProgress size={16} /> : <SyncIcon />}
                    onClick={handleSyncDNS}
                    disabled={anyPending}
                  >
                    Sync External DNS
                  </Button>
                )}
                {domain.canEnableHTTPS && (
                  <Button
                    size="small"
                    variant="outlined"
                    startIcon={addSSL.isPending ? <CircularProgress size={16} /> : <HttpsIcon />}
                    onClick={handleAddSSL}
                    disabled={anyPending}
                  >
                    Add SSL ({domain.neededSubZoneDisplay})
                  </Button>
                )}
                {domain.hasSSLCoverage && !domain.hasService && (
                  <Button
                    size="small"
                    variant="outlined"
                    color="warning"
                    startIcon={removeSSL.isPending ? <CircularProgress size={16} /> : <HttpsIcon />}
                    onClick={handleRemoveSSL}
                    disabled={anyPending}
                  >
                    Remove SSL
                  </Button>
                )}
              </Box>
            )}
          </Collapse>
        </TableCell>
      </TableRow>
    </>
  );
}

function DomainsPage() {
  const { data, isLoading, error } = useDomains();
  const syncAllDNS = useSyncAllDNS();
  const addSSLMutation = useAddDomainSSL();
  const [addOpen, setAddOpen] = useState(false);
  const [addDomain, setAddDomain] = useState("");
  const [snack, setSnack] = useState<SnackState>({ open: false, message: "", severity: "success" });

  const showSnack = (message: string, severity: "success" | "error") =>
    setSnack({ open: true, message, severity });

  const handleAddDomain = () => {
    if (!addDomain.trim()) return;
    addSSLMutation.mutate(addDomain.trim(), {
      onSuccess: () => {
        showSnack(`SSL coverage added for ${addDomain.trim()}`, "success");
        setAddOpen(false);
        setAddDomain("");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleSyncAll = () => {
    syncAllDNS.mutate(undefined, {
      onSuccess: (data) => {
        showSnack(
          `DNS sync complete: ${data.updated} updated, ${data.failed} failed`,
          data.failed > 0 ? "error" : "success",
        );
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">Failed to load domains: {error.message}</Alert>;
  }

  if (!data) return null;

  return (
    <Box>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          Domains
        </Typography>
        <Box sx={{ display: "flex", gap: 1 }}>
          <Button
            variant="outlined"
            startIcon={syncAllDNS.isPending ? <CircularProgress size={16} /> : <SyncIcon />}
            onClick={handleSyncAll}
            disabled={syncAllDNS.isPending}
          >
            {syncAllDNS.isPending ? "Syncing..." : "Sync All DNS"}
          </Button>
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => setAddOpen(true)}
          >
            Add Domain
          </Button>
        </Box>
      </Box>

      <Paper sx={{ p: 2, mb: 3 }}>
        <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap" }}>
          <SummaryChip label="Total" count={data.totalCount} />
          <SummaryChip label="Internal DNS" count={data.intDNSCount} />
          <SummaryChip label="External DNS" count={data.extDNSCount} />
          <SummaryChip label="HTTPS" count={data.httpsCount} />
          <SummaryChip label="Proxy" count={data.proxyCount} />
        </Box>
      </Paper>

      {data.sslGaps.length > 0 && (
        <Alert
          severity="warning"
          icon={<WarningIcon />}
          sx={{ mb: 3 }}
        >
          <Typography variant="subtitle2" sx={{ fontWeight: 600, mb: 1 }}>
            SSL Coverage Gaps ({data.sslGaps.length})
          </Typography>
          <Box component="ul" sx={{ m: 0, pl: 2 }}>
            {data.sslGaps.map((gap) => (
              <li key={gap.domain}>
                <Box sx={{ display: "flex", alignItems: "center", gap: 1, my: 0.5 }}>
                  <Typography variant="body2">
                    {gap.display} &mdash; {gap.reason}
                  </Typography>
                  <Button
                    size="small"
                    variant="outlined"
                    startIcon={addSSLMutation.isPending ? <CircularProgress size={14} /> : <HttpsIcon />}
                    onClick={() =>
                      addSSLMutation.mutate(gap.domain, {
                        onSuccess: () =>
                          showSnack(`SSL coverage added for ${gap.display}`, "success"),
                        onError: (err) => showSnack(err.message, "error"),
                      })
                    }
                    disabled={addSSLMutation.isPending}
                    sx={{ whiteSpace: "nowrap", flexShrink: 0 }}
                  >
                    Add SSL
                  </Button>
                </Box>
              </li>
            ))}
          </Box>
        </Alert>
      )}

      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell sx={{ width: 40 }} />
              <TableCell>Domain</TableCell>
              <TableCell>Zone</TableCell>
              <TableCell align="center">Int DNS</TableCell>
              <TableCell align="center">Ext DNS</TableCell>
              <TableCell align="center">Proxy</TableCell>
              <TableCell align="center">HTTPS</TableCell>
              <TableCell sx={{ width: 50 }} />
            </TableRow>
          </TableHead>
          <TableBody>
            {data.domains.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No domains found.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              data.domains.map((d) => (
                <DomainRow key={d.domain} domain={d} onSnack={showSnack} />
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>

      {/* Add Domain dialog */}
      <Dialog open={addOpen} onClose={() => setAddOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Add Domain SSL Coverage</DialogTitle>
        <DialogContent>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            Enter a domain name to add SSL coverage. The zone and wildcard
            pattern will be determined automatically.
          </Typography>
          <TextField
            fullWidth
            label="Domain"
            placeholder="app.example.com"
            value={addDomain}
            onChange={(e) => setAddDomain(e.target.value)}
            autoFocus
            onKeyDown={(e) => { if (e.key === "Enter") handleAddDomain(); }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setAddOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            onClick={handleAddDomain}
            disabled={!addDomain.trim() || addSSLMutation.isPending}
          >
            {addSSLMutation.isPending ? "Adding..." : "Add"}
          </Button>
        </DialogActions>
      </Dialog>

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

export const Route = createFileRoute("/domains")({
  component: DomainsPage,
});

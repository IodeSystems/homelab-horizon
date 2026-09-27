import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { buildProjectIndex, type ProjectIndex } from "../components/model/projectRoutes.ts";
import { LocationCell, PROJECT_COLUMN_LABEL } from "../components/model/ProjectBits";
import { AssignDialog, RungCell } from "../components/model/AssignBits";
import {
  buildAssignIndex,
  readPlacement,
  readState,
  worstState,
  PLACEMENT_COLUMN_LABEL,
  type AssignIndex,
  type Placement,
} from "../components/model/assign.ts";
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
import HttpsIcon from "@mui/icons-material/Https";
import AddIcon from "@mui/icons-material/Add";
import DeleteIcon from "@mui/icons-material/Delete";
import {
  useDomains,
  useAddDomainSSL,
  useRemoveDomainSSL,
  useDNSDriftStatus,
  useClearDNSDrift,
  useEnvironments,
  useProjects,
  useServices,
} from "../api/hooks";
import type { DomainAnalysis, DNSDriftInfoResp } from "../api/types";

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
  projectIndex,
  assignIndex,
  project,
  environment,
  onAssign,
  onSnack,
}: {
  domain: DomainAnalysis;
  projectIndex: ProjectIndex;
  assignIndex: AssignIndex;
  /** The project of the service this domain belongs to, if hz lists one. */
  project: string | undefined;
  /** The rung of that service. Independent of the project — a service can have one and not the other. */
  environment: string | undefined;
  /**
   * Assign the SERVICE this domain belongs to. A domain has no placement of
   * its own, so this edits the service — which is why it is only offered when
   * hz actually lists one, and why the dialog names the service, not the
   * domain.
   */
  onAssign: (() => void) | undefined;
  onSnack: (message: string, severity: "success" | "error") => void;
}) {
  const [open, setOpen] = useState(false);
  const addSSL = useAddDomainSSL();
  const removeSSL = useRemoveDomainSSL();

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

  const anyPending = addSSL.isPending || removeSSL.isPending;

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
          <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
            <Typography variant="body2" sx={{ fontWeight: 600 }}>
              {domain.domain}
            </Typography>
            {domain.isRedundant && (
              <Chip label="redundant" size="small" color="warning" variant="outlined" sx={{ fontSize: "0.7rem", height: 20 }} />
            )}
            {domain.absorbedDomains && domain.absorbedDomains.length > 0 && (
              <Chip label={`covers ${domain.absorbedDomains.length}`} size="small" color="info" variant="outlined" sx={{ fontSize: "0.7rem", height: 20 }} />
            )}
          </Box>
          {domain.coveredBy && !domain.hasService && (
            <Typography variant="caption" color="text.secondary">
              covered by {domain.coveredBy}
            </Typography>
          )}
        </TableCell>
        {/* A domain has no record of its own: it is Service.Domains, so it is
            scoped through the service that serves it. Uniqueness is
            gateway-wide, which is why this flat list survives — a clash between
            two projects is only ever visible here. */}
        <TableCell>
          <LocationCell index={projectIndex} project={project} />
        </TableCell>
        {/* The rung, and the control that moves it. Editing here edits the
            SERVICE — the domain has no placement of its own — so the button is
            absent for a domain hz lists no service for, and the cell says why
            rather than showing a control that would have nothing to write to. */}
        <TableCell sx={{ minWidth: 240 }}>
          {domain.hasService ? (
            <RungCell
              reading={readPlacement(project, environment, assignIndex)}
              onAssign={onAssign}
            />
          ) : (
            <Typography variant="caption" sx={{ color: "text.secondary" }}>
              hz lists no service for this domain, and a domain has no project of its own — it is
              scoped through the service that serves it. There is nothing here to assign until one
              does.
            </Typography>
          )}
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
          {!domain.hasService && domain.hasSSLCoverage && !(domain.absorbedDomains && domain.absorbedDomains.length > 0) && (
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
        <TableCell sx={{ py: 0 }} colSpan={9}>
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

              {domain.absorbedDomains && domain.absorbedDomains.length > 0 && (
                <Paper variant="outlined" sx={{ p: 2, bgcolor: "rgba(255,255,255,0.02)" }}>
                  <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1, textTransform: "uppercase", letterSpacing: 1 }}>
                    Covers ({domain.absorbedDomains.length} domains)
                  </Typography>
                  {domain.absorbedDomains.map((d) => (
                    <Box key={d.domain} sx={{ display: "flex", gap: 1, alignItems: "center", mb: 0.25 }}>
                      <Typography variant="body2" sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}>
                        {d.domain}
                      </Typography>
                      {d.service && (
                        <Chip label={d.service} size="small" variant="outlined" sx={{ fontSize: "0.7rem", height: 18 }} />
                      )}
                    </Box>
                  ))}
                </Paper>
              )}

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
            {(domain.canEnableHTTPS || (domain.hasSSLCoverage && !domain.hasService)) && (
              <Box sx={{ display: "flex", gap: 1, pb: 2, flexWrap: "wrap" }}>
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
                {domain.hasSSLCoverage && !domain.hasService && !(domain.absorbedDomains && domain.absorbedDomains.length > 0) && (
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

// DNS drift halts ALL sync server-side until an operator reviews the
// out-of-band change and clears it. Shown at the top of the page whenever
// blocked — this is the primary surface for the drift-detection safety
// system, so it's an error-severity banner rather than a dismissible warning.
function DriftBanner({ detail }: { detail: DNSDriftInfoResp }) {
  const clearDrift = useClearDNSDrift();

  return (
    <Alert
      severity="error"
      icon={<WarningIcon />}
      sx={{ mb: 3 }}
    >
      <Typography variant="subtitle2" sx={{ fontWeight: 600, mb: 1 }}>
        DNS sync halted &mdash; drift detected
      </Typography>
      <Typography variant="body2" sx={{ mb: 1 }}>
        An out-of-band change was found at the DNS provider for{" "}
        <strong>{detail.name}</strong> ({detail.type}) in zone{" "}
        <strong>{detail.zone}</strong>. All DNS sync is paused until this is
        reviewed and cleared.
      </Typography>
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" },
          gap: 2,
          mb: 1,
        }}
      >
        <Box>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", textTransform: "uppercase", letterSpacing: 1 }}>
            Expected (published by hz)
          </Typography>
          {detail.expected.length === 0 ? (
            <Typography variant="body2" color="text.secondary">(none)</Typography>
          ) : (
            detail.expected.map((v) => (
              <Typography key={v} variant="body2" sx={{ fontFamily: "monospace", fontSize: "0.8rem", wordBreak: "break-all" }}>
                {v}
              </Typography>
            ))
          )}
        </Box>
        <Box>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", textTransform: "uppercase", letterSpacing: 1 }}>
            Live at provider
          </Typography>
          {detail.live.length === 0 ? (
            <Typography variant="body2" color="text.secondary">(none)</Typography>
          ) : (
            detail.live.map((v) => (
              <Typography key={v} variant="body2" sx={{ fontFamily: "monospace", fontSize: "0.8rem", wordBreak: "break-all" }}>
                {v}
              </Typography>
            ))
          )}
        </Box>
      </Box>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
        Detected {new Date(detail.detectedAt * 1000).toLocaleString()}
      </Typography>
      {clearDrift.isError && (
        <Alert severity="error" sx={{ mb: 1 }}>
          {(clearDrift.error as Error).message}
        </Alert>
      )}
      <Button
        variant="contained"
        color="error"
        size="small"
        startIcon={clearDrift.isPending ? <CircularProgress size={16} color="inherit" /> : undefined}
        disabled={clearDrift.isPending}
        onClick={() => clearDrift.mutate()}
      >
        {clearDrift.isPending ? "Clearing..." : "Accept live & resume sync"}
      </Button>
    </Alert>
  );
}

function DomainsPage() {
  const { data, isLoading, error } = useDomains();
  const projects = useProjects();
  const environments = useEnvironments();
  const servicesQuery = useServices();
  const driftQuery = useDNSDriftStatus();
  const projectIndex = buildProjectIndex(projects.data ?? []);
  // What may be OFFERED, and whether hz has said yet. `services` is in the
  // worst-state too: a domain's placement is read off its service, so a domains
  // screen that has no service list knows nothing about placement either.
  const assignIndex = buildAssignIndex(
    projects.data,
    environments.data,
    worstState(
      worstState(readState(projects), readState(environments)),
      readState(servicesQuery),
    ),
  );
  // serviceName → where that service sits. A domain whose service hz does not
  // list falls through as unowned rather than silently borrowing somebody's
  // project.
  const placementOf = new Map<string, Placement>();
  for (const svc of servicesQuery.data ?? []) {
    placementOf.set(svc.name, { project: svc.project ?? "", environment: svc.environment ?? "" });
  }
  const addSSLMutation = useAddDomainSSL();
  const [addOpen, setAddOpen] = useState(false);
  const [addDomain, setAddDomain] = useState("");
  // The service whose placement is being edited, not the domain — a domain has
  // no placement of its own. Two domains on one service open the same dialog.
  const [assignService, setAssignService] = useState<string | null>(null);
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
      {driftQuery.data?.blocked && driftQuery.data.detail && (
        <DriftBanner detail={driftQuery.data.detail} />
      )}

      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          Domains
        </Typography>
        <Box sx={{ display: "flex", gap: 1 }}>
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

      {(() => {
        // Group domains by zone and sort canonically within each group.
        // Canonical sort: split on ".", reverse to get TLD-first, then
        // compare segments descending so deeper subdomains sort naturally.
        const byZone = new Map<string, DomainAnalysis[]>();
        for (const d of data.domains) {
          const zone = d.zoneName || "(no zone)";
          if (!byZone.has(zone)) byZone.set(zone, []);
          byZone.get(zone)!.push(d);
        }

        const canonicalKey = (domain: string) =>
          domain.split(".").reverse().join(".");

        for (const domains of byZone.values()) {
          domains.sort((a, b) =>
            canonicalKey(b.domain).localeCompare(canonicalKey(a.domain)),
          );
        }

        // Sort zone groups by zone name
        const zones = [...byZone.entries()].sort((a, b) =>
          a[0].localeCompare(b[0]),
        );

        return zones.map(([zone, domains]) => (
          <Box key={zone} sx={{ mb: 3 }}>
            <Typography
              variant="subtitle1"
              sx={{ fontWeight: 600, mb: 1, color: "text.secondary" }}
            >
              {zone}
            </Typography>
            <TableContainer component={Paper}>
              <Table>
                <TableHead>
                  <TableRow>
                    <TableCell sx={{ width: 40 }} />
                    <TableCell>Domain</TableCell>
                    <TableCell>{PROJECT_COLUMN_LABEL}</TableCell>
                    <TableCell>{PLACEMENT_COLUMN_LABEL}</TableCell>
                    <TableCell align="center">Int DNS</TableCell>
                    <TableCell align="center">Ext DNS</TableCell>
                    <TableCell align="center">Proxy</TableCell>
                    <TableCell align="center">HTTPS</TableCell>
                    <TableCell sx={{ width: 50 }} />
                  </TableRow>
                </TableHead>
                <TableBody>
                  {domains.map((d) => (
                    <DomainRow
                      key={d.domain}
                      domain={d}
                      projectIndex={projectIndex}
                      assignIndex={assignIndex}
                      project={placementOf.get(d.serviceName)?.project}
                      environment={placementOf.get(d.serviceName)?.environment}
                      onAssign={
                        d.hasService && d.serviceName
                          ? () => setAssignService(d.serviceName)
                          : undefined
                      }
                      onSnack={showSnack}
                    />
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          </Box>
        ));
      })()}

      {data.domains.length === 0 && (
        <TableContainer component={Paper}>
          <Table>
            <TableBody>
              <TableRow>
                <TableCell colSpan={9} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No domains found.
                  </Typography>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Records live on the DNS page — they are zone-scoped, and editing them
          against the live provider set deserves a page of its own rather than a
          section under a per-domain status table. */}
      <Box sx={{ mt: 3 }}>
        <Button component={Link} to="/dns" size="small" variant="outlined">
          Manage DNS records
        </Button>
      </Box>

      {/* Assign dialog — the SERVICE behind the row, not the domain. It offers
          only projects and rungs hz declares, and reports a refusal inline
          rather than through the snackbar below. */}
      {assignService && (
        <AssignDialog
          service={assignService}
          current={placementOf.get(assignService) ?? { project: "", environment: "" }}
          index={assignIndex}
          onClose={() => setAssignService(null)}
        />
      )}

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

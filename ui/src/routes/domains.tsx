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
  MenuItem,
  Paper,
  Select,
  Snackbar,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import KeyboardArrowDownIcon from "@mui/icons-material/KeyboardArrowDown";
import KeyboardArrowUpIcon from "@mui/icons-material/KeyboardArrowUp";
import WarningIcon from "@mui/icons-material/Warning";
import HttpsIcon from "@mui/icons-material/Https";
import AddIcon from "@mui/icons-material/Add";
import DeleteIcon from "@mui/icons-material/Delete";
import EditIcon from "@mui/icons-material/Edit";
import {
  useDomains,
  useAddDomainSSL,
  useRemoveDomainSSL,
  useZones,
  useZoneRecords,
  useAddRecord,
  useEditRecord,
  useDeleteRecord,
  useDNSDriftStatus,
  useClearDNSDrift,
} from "../api/hooks";
import SyncButton from "../components/SyncButton";
import type { DomainAnalysis, DNSRecordResp, DNSDriftInfoResp } from "../api/types";

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

// --- DNS Records manager (per zone) ---

const RECORD_TYPES = ["TXT", "A", "AAAA", "CNAME"];

interface RecordGroup {
  name: string;
  type: string;
  records: DNSRecordResp[];
}

// Groups live zone records by (name, type) — TXT records at the same name
// commonly carry multiple values, so this is what makes that clear in the UI
// and is also the unit `expectedFrom` (the drift guard) is built from.
function groupRecords(records: DNSRecordResp[]): RecordGroup[] {
  const map = new Map<string, RecordGroup>();
  for (const rec of records) {
    const key = `${rec.name}|${rec.type}`;
    const existing = map.get(key);
    if (existing) {
      existing.records.push(rec);
    } else {
      map.set(key, { name: rec.name, type: rec.type, records: [rec] });
    }
  }
  return [...map.values()].sort((a, b) =>
    a.name === b.name
      ? a.type.localeCompare(b.type)
      : a.name.localeCompare(b.name),
  );
}

function AddRecordDialog({
  open,
  zoneName,
  groups,
  onClose,
}: {
  open: boolean;
  zoneName: string;
  groups: RecordGroup[];
  onClose: () => void;
}) {
  const addRecord = useAddRecord();
  const [form, setForm] = useState({ name: "", type: "TXT", value: "", ttl: 300 });

  const handleClose = () => {
    setForm({ name: "", type: "TXT", value: "", ttl: 300 });
    onClose();
  };

  const handleSubmit = () => {
    const name = form.name.trim();
    // expectedFrom is the drift guard: the values we last saw live for this
    // exact (name, type). If nothing exists yet, it's an empty set.
    const existing = groups.find((g) => g.name === name && g.type === form.type);
    const expectedFrom = existing ? existing.records.map((r) => r.value) : [];
    addRecord.mutate(
      {
        zone: zoneName,
        name,
        type: form.type,
        value: form.value.trim(),
        ttl: form.ttl,
        expectedFrom,
      },
      { onSuccess: handleClose },
    );
  };

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>Add DNS Record &mdash; {zoneName}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Name"
          value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })}
          placeholder={zoneName}
          size="small"
          fullWidth
          helperText="Full record name, e.g. _acme-challenge.example.com"
        />
        <Select
          value={form.type}
          onChange={(e) => setForm({ ...form, type: e.target.value })}
          size="small"
          fullWidth
        >
          {RECORD_TYPES.map((t) => (
            <MenuItem key={t} value={t}>
              {t}
            </MenuItem>
          ))}
        </Select>
        <TextField
          label="Value"
          value={form.value}
          onChange={(e) => setForm({ ...form, value: e.target.value })}
          placeholder={form.type === "TXT" ? "google-site-verification=..." : undefined}
          size="small"
          fullWidth
          multiline={form.type === "TXT"}
          minRows={form.type === "TXT" ? 2 : 1}
        />
        <TextField
          label="TTL"
          type="number"
          value={form.ttl}
          onChange={(e) => setForm({ ...form, ttl: parseInt(e.target.value, 10) || 300 })}
          size="small"
          fullWidth
        />
        {addRecord.isError && (
          <Alert severity="error">{(addRecord.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={handleClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!form.name.trim() || !form.value.trim() || addRecord.isPending}
        >
          {addRecord.isPending ? <CircularProgress size={20} /> : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

interface EditRecordTarget {
  zoneName: string;
  name: string;
  type: string;
  value: string;
  ttl: number;
  expectedFrom: string[];
}

function EditRecordDialog({
  target,
  onClose,
}: {
  target: EditRecordTarget | null;
  onClose: () => void;
}) {
  const editRecord = useEditRecord();
  const [value, setValue] = useState(target?.value ?? "");
  const [ttl, setTtl] = useState(target?.ttl ?? 300);

  // Sync local state when a new target is picked (same trick as
  // EditZoneDialog in settings.tsx).
  const [lastKey, setLastKey] = useState<string | null>(null);
  const key = target
    ? `${target.zoneName}|${target.name}|${target.type}|${target.value}`
    : null;
  if (target && key !== lastKey) {
    setValue(target.value);
    setTtl(target.ttl);
    setLastKey(key);
  }

  if (!target) return null;

  const handleSubmit = () => {
    editRecord.mutate(
      {
        zone: target.zoneName,
        name: target.name,
        type: target.type,
        value: value.trim(),
        oldValue: target.value,
        ttl,
        expectedFrom: target.expectedFrom,
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>
        Edit {target.type} Record &mdash; {target.name}
      </DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Value"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          size="small"
          fullWidth
          multiline={target.type === "TXT"}
          minRows={target.type === "TXT" ? 2 : 1}
        />
        <TextField
          label="TTL"
          type="number"
          value={ttl}
          onChange={(e) => setTtl(parseInt(e.target.value, 10) || 300)}
          size="small"
          fullWidth
        />
        {editRecord.isError && (
          <Alert severity="error">{(editRecord.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!value.trim() || editRecord.isPending}
        >
          {editRecord.isPending ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function ZoneRecordsTable({ zoneName }: { zoneName: string }) {
  const { data, isLoading, error } = useZoneRecords(zoneName);
  const deleteRecord = useDeleteRecord();
  const [addOpen, setAddOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<EditRecordTarget | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{
    name: string;
    type: string;
    value: string;
    expectedFrom: string[];
  } | null>(null);

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", py: 2 }}>
        <CircularProgress size={20} />
      </Box>
    );
  }
  if (error) {
    return <Alert severity="error">Failed to load records: {error.message}</Alert>;
  }
  if (!data) return null;

  const groups = groupRecords(data.records);

  const handleDelete = () => {
    if (!deleteTarget) return;
    deleteRecord.mutate(
      {
        zone: zoneName,
        name: deleteTarget.name,
        type: deleteTarget.type,
        value: deleteTarget.value,
        expectedFrom: deleteTarget.expectedFrom,
      },
      { onSuccess: () => setDeleteTarget(null) },
    );
  };

  return (
    <Box>
      <Box sx={{ display: "flex", justifyContent: "flex-end", mb: 1 }}>
        <Button
          size="small"
          variant="outlined"
          startIcon={<AddIcon />}
          onClick={() => setAddOpen(true)}
        >
          Add Record
        </Button>
      </Box>
      <TableContainer>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell sx={{ width: 80 }}>Type</TableCell>
              <TableCell>Value</TableCell>
              <TableCell sx={{ width: 80 }}>TTL</TableCell>
              <TableCell sx={{ width: 110 }}>Managed</TableCell>
              <TableCell sx={{ width: 90 }} align="right">
                Actions
              </TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {groups.length === 0 && (
              <TableRow>
                <TableCell colSpan={6}>
                  <Typography variant="body2" color="text.secondary" sx={{ py: 2, textAlign: "center" }}>
                    No records found for this zone.
                  </Typography>
                </TableCell>
              </TableRow>
            )}
            {groups.map((group) => {
              const expectedFrom = group.records.map((r) => r.value);
              return group.records.map((rec, i) => (
                <TableRow key={`${group.name}|${group.type}|${rec.value}`} hover>
                  {i === 0 && (
                    <TableCell
                      rowSpan={group.records.length}
                      sx={{ verticalAlign: "top", fontWeight: 600 }}
                    >
                      {group.name}
                    </TableCell>
                  )}
                  {i === 0 && (
                    <TableCell rowSpan={group.records.length} sx={{ verticalAlign: "top" }}>
                      <Chip label={group.type} size="small" variant="outlined" />
                    </TableCell>
                  )}
                  <TableCell sx={{ fontFamily: "monospace", fontSize: "0.8rem", wordBreak: "break-all" }}>
                    {rec.value}
                  </TableCell>
                  <TableCell>{rec.ttl}</TableCell>
                  <TableCell>
                    <Chip
                      label={rec.managed ? "managed" : "unmanaged"}
                      size="small"
                      color={rec.managed ? "success" : "default"}
                      variant="outlined"
                    />
                  </TableCell>
                  <TableCell align="right">
                    <Tooltip title="Edit">
                      <IconButton
                        size="small"
                        onClick={() =>
                          setEditTarget({
                            zoneName,
                            name: group.name,
                            type: group.type,
                            value: rec.value,
                            ttl: rec.ttl,
                            expectedFrom,
                          })
                        }
                      >
                        <EditIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Delete">
                      <IconButton
                        size="small"
                        color="error"
                        onClick={() =>
                          setDeleteTarget({
                            name: group.name,
                            type: group.type,
                            value: rec.value,
                            expectedFrom,
                          })
                        }
                      >
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ));
            })}
          </TableBody>
        </Table>
      </TableContainer>

      <AddRecordDialog
        open={addOpen}
        zoneName={zoneName}
        groups={groups}
        onClose={() => setAddOpen(false)}
      />
      <EditRecordDialog target={editTarget} onClose={() => setEditTarget(null)} />

      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)} maxWidth="xs" fullWidth>
        <DialogTitle>Delete Record</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            Delete {deleteTarget?.type} record <strong>{deleteTarget?.name}</strong>:
          </Typography>
          <Typography variant="body2" sx={{ fontFamily: "monospace", wordBreak: "break-all", mt: 1 }}>
            {deleteTarget?.value}
          </Typography>
          {deleteRecord.isError && (
            <Alert severity="error" sx={{ mt: 1 }}>{(deleteRecord.error as Error).message}</Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button
            onClick={handleDelete}
            color="error"
            variant="contained"
            disabled={deleteRecord.isPending}
          >
            {deleteRecord.isPending ? <CircularProgress size={20} /> : "Delete"}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

// Collapsed by default and mounts ZoneRecordsTable (which fires the
// live-records fetch) only on expand — ListRecords hits the real DNS
// provider, so we don't want every zone querying it on page load.
function ZoneRecordsSection({ zoneName }: { zoneName: string }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <Paper variant="outlined" sx={{ mb: 2 }}>
      <Box
        onClick={() => setExpanded((e) => !e)}
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          p: 1.5,
          cursor: "pointer",
        }}
      >
        <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
          {zoneName}
        </Typography>
        <IconButton size="small">
          {expanded ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
        </IconButton>
      </Box>
      <Collapse in={expanded} timeout="auto" unmountOnExit>
        <Box sx={{ px: 2, pb: 2 }}>
          <ZoneRecordsTable zoneName={zoneName} />
        </Box>
      </Collapse>
    </Paper>
  );
}

function DomainRow({
  domain,
  onSnack,
}: {
  domain: DomainAnalysis;
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
        <TableCell sx={{ py: 0 }} colSpan={7}>
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
  const zonesQuery = useZones();
  const driftQuery = useDNSDriftStatus();
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
          <SyncButton />
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
                    <TableCell align="center">Int DNS</TableCell>
                    <TableCell align="center">Ext DNS</TableCell>
                    <TableCell align="center">Proxy</TableCell>
                    <TableCell align="center">HTTPS</TableCell>
                    <TableCell sx={{ width: 50 }} />
                  </TableRow>
                </TableHead>
                <TableBody>
                  {domains.map((d) => (
                    <DomainRow key={d.domain} domain={d} onSnack={showSnack} />
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
                <TableCell colSpan={7} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No domains found.
                  </Typography>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <Typography variant="h6" sx={{ mt: 4, mb: 2 }}>
        DNS Records
      </Typography>
      {zonesQuery.isLoading ? (
        <Box sx={{ display: "flex", justifyContent: "center", pt: 2 }}>
          <CircularProgress size={20} />
        </Box>
      ) : zonesQuery.error ? (
        <Alert severity="error">
          Failed to load zones: {zonesQuery.error.message}
        </Alert>
      ) : zonesQuery.data && zonesQuery.data.length > 0 ? (
        [...zonesQuery.data]
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((zone) => <ZoneRecordsSection key={zone.name} zoneName={zone.name} />)
      ) : (
        <Typography variant="body2" color="text.secondary">
          No zones configured.
        </Typography>
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

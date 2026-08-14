import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  IconButton,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import AddIcon from "@mui/icons-material/Add";
import DeleteIcon from "@mui/icons-material/Delete";
import EditIcon from "@mui/icons-material/Edit";
import { useZones, useDNSDriftStatus, useClearDNSDrift, useDeleteZone } from "../api/hooks";
import SyncButton from "../components/SyncButton";
import { AddZoneDialog, EditZoneDialog, ZoneCertChip } from "../components/ZoneDialogs";
import type { Zone } from "../api/types";

export const Route = createFileRoute("/dns/")({
  component: DNSZonesPage,
});

// The zone list is a directory, not a workspace: reading a zone means hitting
// the real DNS provider, so nothing is loaded until you descend into one.
function DNSZonesPage() {
  const navigate = useNavigate();
  const { data: zones, isLoading, error } = useZones();
  const { data: drift } = useDNSDriftStatus();
  const clearDrift = useClearDNSDrift();
  const deleteZone = useDeleteZone();
  const [addOpen, setAddOpen] = useState(false);
  const [editZone, setEditZone] = useState<Zone | null>(null);

  return (
    <Box>
      <Box sx={{ display: "flex", alignItems: "center", mb: 1 }}>
        <Typography variant="h4" sx={{ flexGrow: 1 }}>
          DNS
        </Typography>
        <Button
          variant="contained"
          size="small"
          startIcon={<AddIcon />}
          onClick={() => setAddOpen(true)}
          sx={{ mr: 1 }}
        >
          Add Zone
        </Button>
        <SyncButton />
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Zones hz publishes to. Open one to view and edit its records.
      </Typography>

      {/* The block halts every DNS write, so the action belongs beside the
          explanation — this is the page you are on when you care. */}
      {drift?.blocked && (
        <Alert
          severity="warning"
          sx={{ mb: 2 }}
          action={
            <Button
              size="small"
              color="inherit"
              disabled={clearDrift.isPending}
              onClick={() => clearDrift.mutate()}
            >
              {clearDrift.isPending ? "Clearing…" : "Clear & resume"}
            </Button>
          }
        >
          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            DNS sync is halted
          </Typography>
          <Typography variant="caption" component="div">
            {drift.detail?.reason === "unclaimed-name"
              ? `${drift.detail?.type} ${drift.detail?.name} in ${drift.detail?.zone} already exists and hz has never published it — publishing would replace a record hz did not create.`
              : `${drift.detail?.type} ${drift.detail?.name} in ${drift.detail?.zone} changed outside hz.`}
          </Typography>
          <Typography variant="caption" component="div" sx={{ mt: 0.5 }}>
            Clearing adopts what is live and lets the next sync proceed.
          </Typography>
        </Alert>
      )}

      {isLoading ? (
        <Box sx={{ display: "flex", justifyContent: "center", py: 4 }}>
          <CircularProgress size={24} />
        </Box>
      ) : error ? (
        <Alert severity="error">Failed to load zones: {error.message}</Alert>
      ) : !zones || zones.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 4, textAlign: "center" }}>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            No zones configured yet.
          </Typography>
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => setAddOpen(true)}>
            Add Zone
          </Button>
        </Paper>
      ) : (
        <TableContainer component={Paper} variant="outlined">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Zone</TableCell>
                <TableCell>Provider</TableCell>
                <TableCell>Zone ID</TableCell>
                <TableCell>SSL</TableCell>
                <TableCell>Cert</TableCell>
                <TableCell>Sub-Zones</TableCell>
                <TableCell align="right">Pending deletions</TableCell>
                <TableCell align="right" sx={{ width: 120 }} />
              </TableRow>
            </TableHead>
            <TableBody>
              {[...zones]
                .sort((a, b) => a.name.localeCompare(b.name))
                .map((zone) => {
                  const pending = zone.pendingDeletions ?? 0;
                  return (
                    <TableRow
                      key={zone.name}
                      hover
                      sx={{ cursor: "pointer" }}
                      onClick={() =>
                        navigate({ to: "/dns/$zone", params: { zone: zone.name } })
                      }
                    >
                      <TableCell sx={{ fontWeight: 600 }}>{zone.name}</TableCell>
                      <TableCell>
                        {zone.providerType ? (
                          <Chip size="small" variant="outlined" label={zone.providerType} />
                        ) : (
                          <Typography variant="caption" color="text.secondary">
                            none
                          </Typography>
                        )}
                      </TableCell>
                      <TableCell>
                        <Typography variant="caption" color="text.secondary">
                          {zone.zoneId || "—"}
                        </Typography>
                      </TableCell>
                      <TableCell>
                        <Chip
                          size="small"
                          variant="outlined"
                          color={zone.sslEnabled ? "success" : "default"}
                          label={zone.sslEnabled ? "enabled" : "off"}
                        />
                      </TableCell>
                      <TableCell>
                        <ZoneCertChip zoneName={zone.name} />
                      </TableCell>
                      <TableCell>
                        {zone.subZones && zone.subZones.length > 0 ? (
                          <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                            {zone.subZones.map((sz) => (
                              <Chip key={sz} label={sz || "(root)"} size="small" variant="outlined" />
                            ))}
                          </Box>
                        ) : (
                          <Typography variant="caption" color="text.secondary">
                            —
                          </Typography>
                        )}
                      </TableCell>
                      <TableCell align="right">
                        {pending > 0 ? (
                          <Chip size="small" color="warning" variant="outlined" label={pending} />
                        ) : (
                          <Typography variant="caption" color="text.secondary">
                            —
                          </Typography>
                        )}
                      </TableCell>
                      <TableCell align="right" onClick={(e) => e.stopPropagation()}>
                        <IconButton size="small" onClick={() => setEditZone(zone)} title="Edit zone">
                          <EditIcon fontSize="small" />
                        </IconButton>
                        <IconButton
                          size="small"
                          title="Delete zone"
                          onClick={() => {
                            if (
                              window.confirm(
                                `Delete zone ${zone.name}? Records already published at the provider are left alone.`,
                              )
                            ) {
                              deleteZone.mutate(zone.name);
                            }
                          }}
                        >
                          <DeleteIcon fontSize="small" />
                        </IconButton>
                        <ChevronRightIcon fontSize="small" color="disabled" />
                      </TableCell>
                    </TableRow>
                  );
                })}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <AddZoneDialog open={addOpen} onClose={() => setAddOpen(false)} />
      <EditZoneDialog zone={editZone} onClose={() => setEditZone(null)} />
    </Box>
  );
}

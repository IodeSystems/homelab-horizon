import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
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
import { useZones, useDNSDriftStatus, useClearDNSDrift } from "../api/hooks";
import SyncButton from "../components/SyncButton";

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

  return (
    <Box>
      <Box sx={{ display: "flex", alignItems: "center", mb: 1 }}>
        <Typography variant="h4" sx={{ flexGrow: 1 }}>
          DNS
        </Typography>
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
          <Typography variant="body2" color="text.secondary">
            No zones configured. Add one in Settings.
          </Typography>
        </Paper>
      ) : (
        <TableContainer component={Paper} variant="outlined">
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Zone</TableCell>
                <TableCell>Provider</TableCell>
                <TableCell>SSL</TableCell>
                <TableCell align="right">Pending deletions</TableCell>
                <TableCell sx={{ width: 48 }} />
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
                        <Chip
                          size="small"
                          variant="outlined"
                          color={zone.sslEnabled ? "success" : "default"}
                          label={zone.sslEnabled ? "enabled" : "off"}
                        />
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
                      <TableCell>
                        <ChevronRightIcon fontSize="small" color="disabled" />
                      </TableCell>
                    </TableRow>
                  );
                })}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}

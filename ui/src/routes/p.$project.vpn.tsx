/**
 * `/p/$project/vpn` — the VPN clients tab inside a project (plan/design/ui.md,
 * Decision 1, amendment 6). Rows whose `project` is in scope; `""` is global
 * and shows only on the unscoped `/vpn` tab.
 *
 * SCAFFOLD: read-only. The Add flow (prefilled with this project) lands with
 * the VPN clients agent's work.
 */
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
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
import { useVPNPeers } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import { EmptyRow, TabHeader } from "../components/model/FlowBits";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectVPNclients() {
  const { index, resolution, scope, param } = useProjectContext();
  const q = useVPNPeers();

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);
  const mine = (q.data ?? []).filter((r) => inScope(reading.names, r.project));

  return (
    <Box>
      <TabHeader title={`VPN clients in ${route.name}`} />
      <ScopeControl reading={reading} to="/p/$project/vpn" param={param} />
      {q.error ? (
        <Alert severity="error">hz could not be asked: {q.error.message}</Alert>
      ) : q.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No VPN clients attributed to ${route.name}.`} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Client</TableCell><TableCell>Allowed IPs</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((r) => (
                <TableRow key={r.publicKey} hover>
                  <TableCell sx={{ fontFamily: "monospace" }}>{r.name}</TableCell><TableCell sx={{ fontFamily: "monospace" }}>{r.allowedIPs}</TableCell>
                  <TableCell>
                    <LocationCell index={index} project={r.project} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/vpn")({
  component: ProjectVPNclients,
});

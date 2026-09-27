/**
 * `/p/$project/bans` — the IP bans tab inside a project (plan/design/ui.md,
 * Decision 1, amendment 6). Rows whose `project` is in scope; `""` is global
 * and shows only on the unscoped `/bans` tab.
 *
 * SCAFFOLD: read-only. The Add flow (prefilled with this project) lands with
 * the IP bans agent's work.
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
import { useBans } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import { EmptyRow, TabHeader } from "../components/model/FlowBits";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectIPbans() {
  const { index, resolution, scope, param } = useProjectContext();
  const q = useBans();

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);
  const mine = (q.data?.bans ?? []).filter((r) => inScope(reading.names, r.project));

  return (
    <Box>
      <TabHeader title={`IP bans in ${route.name}`} />
      <ScopeControl reading={reading} to="/p/$project/bans" param={param} />
      {q.error ? (
        <Alert severity="error">hz could not be asked: {q.error.message}</Alert>
      ) : q.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No IP bans attributed to ${route.name}.`} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>IP</TableCell><TableCell>Reason</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((r) => (
                <TableRow key={r.ip} hover>
                  <TableCell sx={{ fontFamily: "monospace" }}>{r.ip}</TableCell><TableCell>{r.reason}</TableCell>
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

export const Route = createFileRoute("/p/$project/bans")({
  component: ProjectIPbans,
});

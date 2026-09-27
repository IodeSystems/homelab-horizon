/**
 * `/p/$project/checks` — the Checks tab inside a project (plan/design/ui.md,
 * Decision 1, amendment 6). Rows whose `project` is in scope; `""` is global
 * and shows only on the unscoped `/checks` tab.
 *
 * [Add check] opens the same dialog as `/checks`, prefilled with this
 * project and with no project picker — the tab you clicked from already
 * says where it goes. There is no edit endpoint: re-attributing a check is
 * delete-and-re-add, so this screen does not pretend otherwise.
 */
import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from "@mui/material";
import { useChecks } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { AddCheckDialog, StatusDot, svcFollows } from "./checks";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectChecks() {
  const { index, resolution, scope, param } = useProjectContext();
  const q = useChecks();
  const [adding, setAdding] = useState(false);

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);
  const mine = (q.data ?? []).filter((r) => inScope(reading.names, r.project));
  const add = <AddButton label="Add check" onClick={() => setAdding(true)} />;

  return (
    <Box>
      <TabHeader title={`Checks in ${route.name}`} action={add} />
      <ScopeControl reading={reading} to="/p/$project/checks" param={param} />
      {q.error ? (
        <Alert severity="error">hz could not be asked: {q.error.message}</Alert>
      ) : q.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No checks attributed to ${route.name}.`} action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Check</TableCell><TableCell>Status</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((r) => {
                const follows = svcFollows(r.name);
                return (
                  <TableRow key={r.name} hover>
                    <TableCell sx={{ fontFamily: "monospace" }}>
                      <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
                        {r.name}
                        {follows && (
                          <Tooltip title={`Follows service ${follows} — its project is the service's, not its own.`}>
                            <Chip
                              label={`follows ${follows}`}
                              size="small"
                              variant="outlined"
                              sx={{ height: 20, fontSize: "0.7rem", fontFamily: "inherit" }}
                            />
                          </Tooltip>
                        )}
                      </Box>
                    </TableCell>
                    <TableCell>
                      <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
                        <StatusDot status={r.status} />
                        <Typography variant="body2">{r.status}</Typography>
                      </Box>
                    </TableCell>
                    <TableCell>
                      <LocationCell index={index} project={r.project} />
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {adding ? <AddCheckDialog project={route.name} onClose={() => setAdding(false)} /> : null}
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/checks")({
  component: ProjectChecks,
});

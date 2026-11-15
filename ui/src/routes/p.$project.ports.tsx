/**
 * `/p/$project/ports` — the Ports tab inside a project (plan/design/ui.md,
 * Decision 1, amendment 6). Two sections, because two different records are
 * attributed here:
 *
 * - Reservations: `HostPortEntry.project`, DERIVED from the reserving
 *   service's project — never stored — so there is nothing to add directly;
 *   [Add service] opens `AddServiceDialog` (`services.tsx`) instead.
 * - Exclusions: `PortRange.project`, stored on the custom exclusion itself;
 *   [Add exclusion] opens `AddExclusionDialog` (`ports.tsx`), prefilled with
 *   this project but still editable — "global" is itself a legal choice.
 *
 * `""` is global and shows only on the unscoped `/ports` tab.
 */
import { useState } from "react";
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
import { usePorts } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { AddServiceDialog } from "./services";
import { AddExclusionDialog } from "./ports";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectPorts() {
  const { index, resolution, scope, param } = useProjectContext();
  const q = usePorts();
  const [addingService, setAddingService] = useState(false);
  const [addingExclusion, setAddingExclusion] = useState(false);

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const reservations = Object.entries(q.data?.hosts ?? {})
    .flatMap(([host, es]) => es.map((e) => ({ ...e, host })))
    .filter((r) => inScope(reading.names, r.project));
  const exclusions = (q.data?.exclusions.custom ?? []).filter((r) => inScope(reading.names, r.project));

  const addService = <AddButton label="Add service" onClick={() => setAddingService(true)} />;
  const addExclusion = <AddButton label="Add exclusion" onClick={() => setAddingExclusion(true)} />;

  return (
    <Box>
      <TabHeader title={`Ports in ${route.name}`} />
      <ScopeControl reading={reading} to="/p/$project/ports" param={param} />

      {q.error ? (
        <Alert severity="error">hz could not be asked: {q.error.message}</Alert>
      ) : q.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : (
        <>
          <TabHeader title="Reservations" action={addService} />
          {reservations.length === 0 ? (
            <Box sx={{ mb: 3 }}>
              <EmptyRow
                text={`No port reservations attributed to ${route.name}. A reservation comes from a service.`}
                action={addService}
              />
            </Box>
          ) : (
            <TableContainer component={Paper} sx={{ mb: 3 }}>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Host</TableCell>
                    <TableCell>Port</TableCell>
                    <TableCell>Service</TableCell>
                    <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {reservations.map((r) => (
                    <TableRow key={`${r.host}:${r.port}/${r.proto}`} hover>
                      <TableCell sx={{ fontFamily: "monospace" }}>{r.host}</TableCell>
                      <TableCell sx={{ fontFamily: "monospace" }}>
                        {r.port}/{r.proto}
                      </TableCell>
                      <TableCell sx={{ fontFamily: "monospace" }}>{r.service}</TableCell>
                      <TableCell>
                        <LocationCell index={index} project={r.project} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}

          <TabHeader title="Exclusions" action={addExclusion} />
          {exclusions.length === 0 ? (
            <EmptyRow text={`No port exclusions attributed to ${route.name}.`} action={addExclusion} />
          ) : (
            <TableContainer component={Paper}>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Range</TableCell>
                    <TableCell>Note</TableCell>
                    <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {exclusions.map((r, i) => (
                    <TableRow key={i} hover>
                      <TableCell sx={{ fontFamily: "monospace" }}>
                        {r.to && r.to > r.from ? `${r.from}–${r.to}` : `${r.from}`}
                      </TableCell>
                      <TableCell>
                        <Typography variant="body2" color={r.note ? "text.primary" : "text.secondary"}>
                          {r.note ?? "—"}
                        </Typography>
                      </TableCell>
                      <TableCell>
                        <LocationCell index={index} project={r.project} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </>
      )}

      {addingService ? <AddServiceDialog project={route.name} onClose={() => setAddingService(false)} /> : null}
      {addingExclusion ? (
        <AddExclusionDialog project={route.name} onClose={() => setAddingExclusion(false)} />
      ) : null}
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/ports")({
  component: ProjectPorts,
});

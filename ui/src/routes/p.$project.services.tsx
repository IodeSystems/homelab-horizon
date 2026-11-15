/**
 * `/$project/services` — the services this project owns.
 *
 * `config.Service` carries `Project`, which is what puts this screen under the
 * parameter at all. The field is OPTIONAL, though, and that is why the flat
 * `/services` survives: a service may name no project, permanently and legally,
 * and a screen filtered by project structurally cannot show one. This screen
 * says so rather than implying by silence that there are none.
 *
 * [Add service] opens the same form as `/services` and places the new service
 * in this project. Editing and deleting stay on `/services`.
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
  Typography,
} from "@mui/material";
import { useServices } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { AddServiceDialog } from "./services";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectServices() {
  const { index, resolution, scope, param } = useProjectContext();
  const services = useServices();
  const [adding, setAdding] = useState(false);

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);
  const mine = (services.data ?? []).filter((s) => inScope(reading.names, s.project));
  const add = <AddButton label="Add service" onClick={() => setAdding(true)} />;

  return (
    <Box>
      <TabHeader title={`Services in ${route.name}`} action={add} />
      <ScopeControl reading={reading} to="/p/$project/services" param={param} />

      {services.error ? (
        <Alert severity="error">
          hz could not be asked which services it serves: {services.error.message}
        </Alert>
      ) : services.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which services it serves…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No services in ${route.name}.`} action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Service</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                <TableCell>Domains</TableCell>
                <TableCell>Serving</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((s) => (
                <TableRow key={s.name} hover>
                  <TableCell sx={{ fontFamily: "monospace", fontWeight: 600 }}>{s.name}</TableCell>
                  <TableCell>
                    <LocationCell index={index} project={s.project} />
                  </TableCell>
                  <TableCell>
                    <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                      {s.domains.map((d) => (
                        <Chip key={d} size="small" variant="outlined" label={d} />
                      ))}
                    </Box>
                  </TableCell>
                  <TableCell sx={{ color: "text.secondary" }}>
                    {s.dormant
                      ? "reserved slot"
                      : s.proxy?.backend || (s.proxy?.staticRoot ? "static folder" : "") || "no proxy"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {adding ? <AddServiceDialog project={route.name} onClose={() => setAdding(false)} /> : null}
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/services")({
  component: ProjectServices,
});

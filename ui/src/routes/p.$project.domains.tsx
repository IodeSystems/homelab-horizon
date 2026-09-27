/**
 * `/$project/domains` — the domains this project's services serve.
 *
 * A DOMAIN HAS NO RECORD OF ITS OWN. It is `Service.Domains`, so it is
 * project-scoped THROUGH its service and this screen joins the two: hz's
 * `/domains` read answers per domain and names the service, and the service
 * record is what names the project.
 *
 * That join is also why the flat `/domains` survives. Domain uniqueness is
 * gateway-wide — two projects cannot both serve `x.com` — so a conflict is only
 * ever visible on a list that crosses projects, and a per-project screen cannot
 * show one by construction.
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
import { useDomains, useServices } from "../api/hooks";
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

function ProjectDomains() {
  const { index, resolution, scope, param } = useProjectContext();
  const domains = useDomains();
  const services = useServices();
  const [adding, setAdding] = useState(false);

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  // serviceName → project. A domain whose service hz does not list falls
  // through as unowned rather than being silently dropped.
  const projectOf = new Map<string, string | undefined>();
  for (const s of services.data ?? []) projectOf.set(s.name, s.project);

  const rows = (domains.data?.domains ?? [])
    .map((d) => ({ d, project: projectOf.get(d.serviceName) }))
    .filter((r) => inScope(reading.names, r.project));

  const loading = domains.isLoading || services.isLoading;
  const add = <AddButton label="Add service" onClick={() => setAdding(true)} />;
  const error = domains.error ?? services.error;

  return (
    <Box>
      <TabHeader title={`Domains in ${route.name}`} action={add} />

      <ScopeControl reading={reading} to="/p/$project/domains" param={param} />

      {loading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which domains it serves…</Typography>
        </Box>
      ) : error ? (
        <Alert severity="error">hz could not be asked which domains it serves: {error.message}</Alert>
      ) : rows.length === 0 ? (
        <EmptyRow text={`No domains in ${route.name}. A domain comes with a service.`} action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Domain</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                <TableCell>Through service</TableCell>
                <TableCell>HTTPS</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map(({ d, project }) => (
                <TableRow key={d.domain} hover>
                  <TableCell sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                    {d.domain}
                  </TableCell>
                  <TableCell>
                    <LocationCell index={index} project={project} />
                  </TableCell>
                  <TableCell sx={{ fontFamily: "monospace" }}>{d.serviceName}</TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      variant="outlined"
                      color={d.hasSSLCoverage ? "success" : "default"}
                      label={
                        d.hasSSLCoverage
                          ? d.certExists
                            ? `covered by ${d.certDomain}`
                            : "covered, no cert on disk yet"
                          : "no SSL coverage"
                      }
                    />
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

export const Route = createFileRoute("/p/$project/domains")({
  component: ProjectDomains,
});

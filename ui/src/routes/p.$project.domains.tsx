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
import { createFileRoute, Link } from "@tanstack/react-router";
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
  const error = domains.error ?? services.error;

  return (
    <Box>
      <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
        Domains in {route.name}
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
        A domain has no record of its own — it belongs to a service, and the service names the
        project. Each row below names the service it came through and the project that service
        names.
      </Typography>

      <ScopeControl reading={reading} to="/$project/domains" param={param} />

      {loading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which domains it serves…</Typography>
        </Box>
      ) : error ? (
        <Alert severity="error">
          hz could not be asked which domains it serves: {error.message}. This is empty because the
          read failed, not because the project serves none.
        </Alert>
      ) : rows.length === 0 ? (
        <Alert severity="info">
          No service in {reading.scope === "own" ? route.name : `${route.name} or anything below it`}{" "}
          serves a domain. A service can exist without one — an internal-only backend has no name to
          resolve — so this is not necessarily an unfinished setup.
        </Alert>
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

      <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          Conflicts are not visible here
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          Domain uniqueness is gateway-wide: two projects cannot both serve the same name. A clash is
          therefore a fact ACROSS projects and a screen scoped to one cannot show it — it is on{" "}
          <Link to="/domains">Domains</Link>, which lists every domain on the gateway with the
          project each belongs to.
        </Typography>
      </Paper>
    </Box>
  );
}

export const Route = createFileRoute("/$project/domains")({
  component: ProjectDomains,
});

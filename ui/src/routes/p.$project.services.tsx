/**
 * `/$project/services` — the services this project owns.
 *
 * `config.Service` carries `Project`, which is what puts this screen under the
 * parameter at all. The field is OPTIONAL, though, and that is why the flat
 * `/services` survives: a service may name no project, permanently and legally,
 * and a screen filtered by project structurally cannot show one. This screen
 * says so rather than implying by silence that there are none.
 *
 * Read-only, like the rest of the project surface. Adding, editing and deleting
 * a service happen on `/services`, where every service is visible — including
 * the ones this screen cannot show.
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
import { useServices } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
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

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const all = services.data ?? [];
  const mine = all.filter((s) => inScope(reading.names, s.project));
  const unassigned = all.filter((s) => !s.project);

  return (
    <Box>
      <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
        Services in {route.name}
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
        Every service whose record names this project. A service carries its project on the record
        itself, which is why this screen exists at this URL rather than as a filter on the flat list.
      </Typography>

      <ScopeControl reading={reading} to="/$project/services" param={param} />

      {services.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which services it serves…</Typography>
        </Box>
      ) : services.error ? (
        <Alert severity="error">
          hz could not be asked which services it serves: {services.error.message}. This is empty
          because the read failed, not because the project has none.
        </Alert>
      ) : mine.length === 0 ? (
        <Alert severity="info">
          No service names {reading.scope === "own" ? route.name : `${route.name} or anything below it`}.
          That is an ordinary state — a project is declared before anything moves into it — and it is
          not the same as hz having no services: it serves {all.length} in total.
        </Alert>
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
                  <TableCell>
                    {s.dormant ? (
                      <Typography variant="caption" sx={{ color: "text.secondary" }}>
                        reserved slot — nothing is expected to answer
                        {s.dormantReason ? `: ${s.dormantReason}` : ""}
                      </Typography>
                    ) : (
                      <Typography variant="caption" sx={{ color: "text.secondary" }}>
                        {s.proxy?.backend || (s.proxy?.staticRoot ? "static folder" : "") || "no proxy"}
                      </Typography>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          Services with no project — {unassigned.length} of {all.length}
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          A service may name no project at all. It is explicitly legal and permanent, not a
          misconfiguration — every service in a config that predates the tree is in that state — and
          this screen cannot show one, because it selects rows by the project they name. They are on{" "}
          <Link to="/services">Services</Link>, which lists every service, assigned or not.
        </Typography>
      </Paper>
    </Box>
  );
}

export const Route = createFileRoute("/$project/services")({
  component: ProjectServices,
});

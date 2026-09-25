/**
 * `/$project/segments` — the networks this project declares.
 *
 * The cleanest project-scoped record in the config and the last one with no
 * screen: `Segment.Project` is REQUIRED, enforced in `ValidateSegments` and
 * again in `AddSegment`, with the field comment saying why — *"a segment owned
 * by nobody is a network nobody is responsible for"*. `GET /api/v1/segments`
 * has been served since the record landed and nothing in the browser had ever
 * called it (`grep -rn "/segments" ui/src/` returned nothing, against a
 * positive control of `useVPNPeers`). So this is a new screen over an existing
 * endpoint, not a new backend.
 *
 * # THE OWNER IS WHAT MAKES A CROSSING READABLE
 *
 * A member is a MACHINE, and a machine carries no project — so a segment owned
 * by one project can carry another project's box, and that is a real and
 * legitimate state rather than a misconfiguration. Each member's name links to
 * `/machines/$machine`, the one page a box has; nothing here links to a
 * per-project machine URL, because there is no such thing.
 *
 * # UNADDRESSED IS NOT EMPTY
 *
 * `SegmentResp.unaddressed` is every machine that NAMES this segment and has no
 * member entry — the state `hz machine add --segment` leaves. It is not an
 * error and it is not a member, and a screen that counted only members would
 * render the segment as smaller than it is. It gets its own column, always.
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
import { useSegments } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectSegments() {
  const { index, resolution, scope, param } = useProjectContext();
  const segments = useSegments();

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const all = segments.data ?? [];
  const mine = all.filter((s) => inScope(reading.names, s.project));

  return (
    <Box>
      <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
        Network segments in {route.name}
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
        Every segment whose record names this project. A segment must name one — it is the owner
        who is responsible for the network — which is why this screen selects rows directly rather
        than deriving them. Its members are machines, and a machine carries no project, so another
        project's box on this network is a crossing rather than a mistake.
      </Typography>

      <ScopeControl reading={reading} to="/$project/segments" param={param} />

      {/* Error first: an errored query holding no data is reset to pending on
          mount, so an isLoading check in front of it shows a spinner forever. */}
      {segments.error ? (
        <Alert severity="error">
          hz could not be asked which segments it declares:{" "}
          {segments.error instanceof Error ? segments.error.message : String(segments.error)}. This
          list is empty because the read failed, not because the project declares no network.
        </Alert>
      ) : segments.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which segments it declares…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <Alert severity="info">
          No segment names{" "}
          {reading.scope === "own" ? route.name : `${route.name} or anything below it`}. That is an
          ordinary state — a project is declared before it has a network — and it is not the same as
          hz having none: it declares {all.length} in total, every one of them owned by some project.
        </Alert>
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Segment</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                <TableCell>Range</TableCell>
                <TableCell>Members</TableCell>
                <TableCell>Named it, not addressed</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((s) => (
                <TableRow key={s.name} hover>
                  <TableCell sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                    {s.name}
                    {s.note ? (
                      <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
                        {s.note}
                      </Typography>
                    ) : null}
                  </TableCell>
                  <TableCell>
                    <LocationCell index={index} project={s.project} />
                  </TableCell>
                  <TableCell sx={{ fontFamily: "monospace" }}>
                    {s.cidr}
                    <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
                      on {s.interface}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    {(s.members ?? []).length === 0 ? (
                      <Typography variant="caption" sx={{ color: "text.secondary" }}>
                        no machine is addressed on this network yet
                      </Typography>
                    ) : (
                      <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                        {(s.members ?? []).map((m) => (
                          <Link key={m.machine} to="/machines/$machine" params={{ machine: m.machine }}>
                            <Chip
                              size="small"
                              variant={m.hub ? "filled" : "outlined"}
                              label={`${m.machine} ${m.address}${m.hub ? " · hub" : ""}`}
                              sx={{ fontFamily: "monospace", cursor: "pointer" }}
                            />
                          </Link>
                        ))}
                      </Box>
                    )}
                  </TableCell>
                  <TableCell>
                    {(s.unaddressed ?? []).length === 0 ? (
                      <Typography variant="caption" sx={{ color: "text.secondary" }}>
                        none — every machine naming this segment has an address on it
                      </Typography>
                    ) : (
                      <>
                        <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                          {(s.unaddressed ?? []).map((m) => (
                            <Link key={m} to="/machines/$machine" params={{ machine: m }}>
                              <Chip
                                size="small"
                                color="warning"
                                variant="outlined"
                                label={m}
                                sx={{ fontFamily: "monospace", cursor: "pointer" }}
                              />
                            </Link>
                          ))}
                        </Box>
                        <Typography variant="caption" sx={{ color: "text.secondary" }}>
                          these machines name this segment and have no address on it — half declared,
                          not absent
                        </Typography>
                      </>
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
          VPN clients are not on this screen
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          A segment's members are machines. A VPN client is `{"{Name, PublicKey, AllowedIPs}"}` and
          is not a machine record, so hz cannot say which segment — or which project — a client
          belongs to. Every client is on <Link to="/vpn">VPN Clients</Link>, gateway-wide, until the
          record carries the link.
        </Typography>
      </Paper>
    </Box>
  );
}

export const Route = createFileRoute("/$project/segments")({
  component: ProjectSegments,
});

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
 * # THE TABLE IS SHARED WITH `/segments`, THE ESTATE'S OWN NETWORK SCREEN
 *
 * `Network` is one of the five entries that are identical at every level of the
 * menu, so the same surface exists at the estate and inside every project. The
 * table, the crossing rule and the unaddressed column live in
 * `components/model/SegmentBits.tsx`; this screen is the SELECTION — which rows
 * are in scope — and the sentences that only make sense inside a project.
 */
import { createFileRoute, Link } from "@tanstack/react-router";
import { Alert, Box, CircularProgress, Typography } from "@mui/material";
import { useSegments } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import {
  LOCATION_COLUMN_LABEL,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";
import { ClientsNotHere, SegmentsTable } from "../components/model/SegmentBits";

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
          hz having none: it declares {all.length} in total, every one of them owned by some project —{" "}
          <Link to="/segments">Network at the estate</Link> lists them all.
        </Alert>
      ) : (
        <SegmentsTable segments={mine} index={index} ownerLabel={LOCATION_COLUMN_LABEL} />
      )}

      <ClientsNotHere />
    </Box>
  );
}

export const Route = createFileRoute("/$project/segments")({
  component: ProjectSegments,
});

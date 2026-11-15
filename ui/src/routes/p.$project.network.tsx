/**
 * `/p/$project/network` — Network, inside one project: the segments it declares.
 *
 * The heading matches the TAB WORD (`Network`), because the tab the operator
 * clicked and the screen they land on have to be recognisably the same thing.
 * What a network is here — a segment, owned by a project — is the first sentence.
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
 * # THE TABLE IS SHARED WITH `/network`, THE UNSCOPED NETWORK SCREEN
 *
 * `Network` is one of the six tabs that are identical at every scope, so the
 * same surface exists with no project selected and inside every project. The
 * table, the crossing rule and the unaddressed column live in
 * `components/model/SegmentBits.tsx`; this screen is the SELECTION — which rows
 * are in scope — and the sentences that only make sense inside a project.
 *
 * ADD defaults its owner to THIS project — the node the operator clicked "+"
 * from — but the select still lists every declared project: `AddSegment`
 * accepts any of them (`internal/config/segment.go:452`), not a descendant of
 * where you are, so restricting the choice here would be a UI rule the server
 * does not have.
 */
import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Alert, Box, CircularProgress, Typography } from "@mui/material";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { useSegments } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import {
  LOCATION_COLUMN_LABEL,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";
import { SegmentsTable } from "../components/model/SegmentBits";
import { AddSegmentDialog, EditSegmentDialog, RemoveSegmentDialog } from "../components/model/SegmentDialogs";
import type { SegmentResp } from "../api/generated-types";

function ProjectSegments() {
  const { index, resolution, scope, param } = useProjectContext();
  const segments = useSegments();

  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<SegmentResp | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const mine = (segments.data ?? []).filter((s) => inScope(reading.names, s.project));
  const add = <AddButton label="Add segment" onClick={() => setAdding(true)} />;

  return (
    <Box>
      <TabHeader title={`Network in ${route.name}`} action={add} />
      <ScopeControl reading={reading} to="/p/$project/network" param={param} />

      {/* Error first: an errored query holding no data is reset to pending on
          mount, so an isLoading check in front of it shows a spinner forever. */}
      {segments.error ? (
        <Alert severity="error">
          hz could not be asked which segments it declares:{" "}
          {segments.error instanceof Error ? segments.error.message : String(segments.error)}
        </Alert>
      ) : segments.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which segments it declares…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No network segments in ${route.name}.`} action={add} />
      ) : (
        <SegmentsTable
          segments={mine}
          index={index}
          ownerLabel={LOCATION_COLUMN_LABEL}
          onEdit={setEditing}
          onRemove={setRemoving}
        />
      )}

      <AddSegmentDialog open={adding} index={index} defaultProject={route.name} onClose={() => setAdding(false)} />
      <EditSegmentDialog segment={editing} index={index} onClose={() => setEditing(null)} />
      <RemoveSegmentDialog name={removing} onClose={() => setRemoving(null)} />
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/network")({
  component: ProjectSegments,
});

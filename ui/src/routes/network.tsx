/**
 * `/network` — the Network tab with no project selected. Every segment hz
 * declares.
 *
 * `Network` is one of the six tabs that exist at every scope (plan/design/ui.md,
 * Decision 1, amendment 5): the tab promises that a word means the same thing
 * wherever it appears, which it cannot do if the surface only exists inside a
 * project. Unscoped shows every row; a project narrows them.
 *
 * It is also the screen `plan/design/ui.md` already specified as *"Network —
 * who can reach what?"*: the segment table, gateway-wide, with the owner on
 * every row. The bridge report it also asks for is NOT here — that needs a
 * multi-segment-machine derivation hz does not serve yet, and inventing it from
 * the member lists would be a guess presented as a report.
 *
 * The owner column asks a different question here than it does inside a
 * project, and the code already has two labels for that: `PROJECT_COLUMN_LABEL`
 * ("which project at all") on a gateway-wide list, `LOCATION_COLUMN_LABEL`
 * ("where in this subtree") on a scoped one.
 *
 * ADD/EDIT/REMOVE landed here alongside the read (`plan/design/ui.md`'s own
 * "unreachable endpoints" list named `/segments/{add,set,rm}`): the same
 * finding `ProjectDialogs.tsx` made for `/projects/{add,rm}`, and the same
 * reference pattern applied — `SegmentDialogs.tsx`.
 */
import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { Alert, Box, Button, CircularProgress, Typography } from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import { useProjects, useSegments } from "../api/hooks";
import { buildProjectIndex } from "../components/model/projectRoutes.ts";
import { PROJECT_COLUMN_LABEL } from "../components/model/ProjectBits";
import { ClientsNotHere, SegmentsTable } from "../components/model/SegmentBits";
import { AddSegmentDialog, EditSegmentDialog, RemoveSegmentDialog } from "../components/model/SegmentDialogs";
import { ScreenHeading } from "../components/model/ModelBits";
import type { SegmentResp } from "../api/generated-types";

function UnscopedNetwork() {
  const segments = useSegments();
  const projects = useProjects();
  const index = buildProjectIndex(projects.data ?? []);
  const all = segments.data ?? [];

  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<SegmentResp | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);

  return (
    <Box>
      <ScreenHeading
        title="Network"
        blurb="Every segment hz declares, whoever owns it. A segment must name a project — the owner is who is responsible for the network — so this list is the same rows as every project's Network screen added together, with the owner on each row."
        right={
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => setAdding(true)}>
            Add segment
          </Button>
        }
      />

      {/* Error first: an errored query holding no data is reset to pending on
          mount, so an isLoading check in front of it shows a spinner forever. */}
      {segments.error ? (
        <Alert severity="error">
          hz could not be asked which segments it declares:{" "}
          {segments.error instanceof Error ? segments.error.message : String(segments.error)}. This
          list is empty because the read failed, not because hz declares no network.
        </Alert>
      ) : segments.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which segments it declares…</Typography>
        </Box>
      ) : all.length === 0 ? (
        <Alert severity="info">
          hz declares no segment at all. That is an ordinary state — the gateway routes and proxies
          without one — and it is not the same as a project having none: there is nothing here to
          scope.
        </Alert>
      ) : (
        <SegmentsTable
          segments={all}
          index={index}
          ownerLabel={PROJECT_COLUMN_LABEL}
          onEdit={setEditing}
          onRemove={setRemoving}
        />
      )}

      <ClientsNotHere />

      <AddSegmentDialog open={adding} index={index} defaultProject="" onClose={() => setAdding(false)} />
      <EditSegmentDialog segment={editing} index={index} onClose={() => setEditing(null)} />
      <RemoveSegmentDialog name={removing} onClose={() => setRemoving(null)} />
    </Box>
  );
}

export const Route = createFileRoute("/network")({
  component: UnscopedNetwork,
});

/**
 * `/$project/machines` — the boxes this project is responsible for, plus the
 * boxes it merely has something running on.
 *
 * # OWNED, AND CROSSING — TWO BUCKETS, NOT ONE DERIVED LIST
 *
 * `Machine.Project` names an OWNER now (CLAUDE.md invariant 6, amended
 * 2026-09-26: "responsibility, not placement"; `""` is global). So this
 * screen is no longer a pure query the way it was before the amendment — a
 * machine can be on it two ways:
 *
 *   OWNED     `machine.project` is in scope. Listed regardless of what runs
 *             on it — attribution is organisational, like a segment's owner.
 *   CROSSING  `machine.project` is NOT in scope, but an in-scope instance
 *             (approved, per `useCMRegistrations`) runs on it anyway. A
 *             machine owned by one project may host another's instances
 *             (invariant 6), and this is where that becomes visible: the row
 *             names which of this project's addresses put it here, and who
 *             actually owns the box.
 *
 * Which is still why `/$project/machines/$machine` does not exist and must
 * never: a box that hosts three projects has three "crossing" reasons but one
 * identity, and a detail route per hosting project would give it one URL per
 * reason instead of one URL, period. Every machine name below links to
 * `/machines/$machine`, the one page it has.
 *
 * # WHICH REGISTRATIONS, SAID OUT LOUD
 *
 * **Approved only, asked for explicitly.** `useCMRegistrations()` with no
 * argument keys itself `"all"` and sends no `state`, and the handler defaults an
 * empty `state` to PENDING — there is no "all". A crossing built on the bare
 * hook would read a box as hosting an address nobody has approved and look
 * entirely plausible doing it. Approved is the right list anyway: hz declares
 * a version for an approved address and none for a pending one, so a pending
 * row is a box asking to be here, not a box that is.
 *
 * The pending ones are counted and named in a panel of their own rather than
 * dropped, because a queue that does not say it is filtered hides a waiting
 * box.
 */
import { useState } from "react";
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
import type { CMRegistrationResp, MachineResp } from "../api/generated-types";
import { useCMRegistrations, useMachines } from "../api/hooks";
import { AddMachineDialog } from "../components/model/MachineDialogs";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { inScope } from "../components/model/projectRoutes.ts";
import {
  OWNER_COLUMN_LABEL,
  OwnerCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

export interface ProjectMachineRow {
  machine: string;
  /** `""` is global. */
  owner: string;
  /** `owner` is not one of the scope's names — the box is here only because
   * it hosts something of this project's, not because it answers to it. */
  crossing: boolean;
  /** The in-scope instances that put this row on the list. Owned rows may
   * carry none — ownership lists regardless of what runs there. */
  instances: CMRegistrationResp[];
}

/** Owned machines, plus machines an in-scope instance runs on but does not own. */
export function deriveProjectMachines(
  machines: MachineResp[],
  approved: CMRegistrationResp[],
  names: string[],
): ProjectMachineRow[] {
  const hostedBy = new Map<string, CMRegistrationResp[]>();
  for (const r of approved) {
    if (!r.machineName || !inScope(names, r.project)) continue;
    const list = hostedBy.get(r.machineName) ?? [];
    list.push(r);
    hostedBy.set(r.machineName, list);
  }

  const rows: ProjectMachineRow[] = [];
  for (const m of machines) {
    const owner = m.project ?? "";
    const hosted = hostedBy.get(m.name) ?? [];
    if (names.includes(owner)) {
      rows.push({ machine: m.name, owner, crossing: false, instances: hosted });
    } else if (hosted.length > 0) {
      rows.push({ machine: m.name, owner, crossing: true, instances: hosted });
    }
  }
  return rows.sort((a, b) => a.machine.localeCompare(b.machine));
}

function address(r: CMRegistrationResp): string {
  return [r.project, r.environment, r.app, r.role].filter(Boolean).join("/");
}

function ProjectMachines() {
  const { index, resolution, scope, param } = useProjectContext();
  // EXPLICIT STATES, BOTH OF THEM. The bare hook would ask for pending and
  // say "all" in its query key.
  const machines = useMachines();
  const approved = useCMRegistrations("approved");
  const pending = useCMRegistrations("pending");
  const [adding, setAdding] = useState(false);

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const rows = deriveProjectMachines(machines.data ?? [], approved.data ?? [], reading.names);
  const waiting = (pending.data ?? []).filter((r) => inScope(reading.names, r.project));

  const add = <AddButton label="Add machine" onClick={() => setAdding(true)} />;
  const loading = machines.isLoading || approved.isLoading;
  const failure = machines.error ?? approved.error;

  return (
    <Box>
      <TabHeader title={`Machines in ${route.name}`} action={add} />
      <ScopeControl reading={reading} to="/p/$project/machines" param={param} />

      {/* Error BEFORE loading: a failed query holding no data is reset to
          pending on mount by React Query's retryOnMount, so checking isLoading
          first renders a permanent spinner over a read that already failed. */}
      {failure ? (
        <Alert severity="error">
          hz could not be asked: {failure instanceof Error ? failure.message : String(failure)}. This
          list is empty because the read failed, not because {route.name} owns nothing.
        </Alert>
      ) : loading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : rows.length === 0 ? (
        <EmptyRow text={`No machines attributed to ${route.name}.`} action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Machine</TableCell>
                <TableCell>{OWNER_COLUMN_LABEL}</TableCell>
                <TableCell>What runs here</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.machine} hover>
                  <TableCell sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                    {/* The ONE page this box has. Never /$project/machines/… */}
                    <Link to="/machines/$machine" params={{ machine: row.machine }}>
                      {row.machine}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
                      <OwnerCell index={index} project={row.owner} />
                      {row.crossing ? (
                        <Chip
                          size="small"
                          variant="outlined"
                          color="warning"
                          label="crossing"
                          title={`Owned by ${row.owner === "" ? "global" : row.owner}, not ${route.name} — it hosts ${route.name}'s instance anyway.`}
                        />
                      ) : null}
                    </Box>
                  </TableCell>
                  <TableCell>
                    {row.instances.length === 0 ? (
                      <Typography variant="caption" sx={{ color: "text.secondary" }}>
                        Nothing of {route.name}&apos;s runs here yet.
                      </Typography>
                    ) : (
                      <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                        {row.instances.map((r) => (
                          <Chip
                            key={r.id}
                            size="small"
                            variant="outlined"
                            label={`hosts ${address(r)}`}
                            sx={{ fontFamily: "monospace" }}
                          />
                        ))}
                      </Box>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {waiting.length > 0 ? (
        <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
            Waiting for approval — {waiting.length} address{waiting.length === 1 ? "" : "es"} in this
            project
          </Typography>
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {waiting.map((r) => `${r.machineName || "an unnamed box"} has asked to run ${address(r)}`).join("; ")}{" "}
            — and nobody has granted it. Those boxes are NOT on the list above: hz declares no version
            for an address nobody approved. Approvals are on this project&apos;s{" "}
            <Link to="/p/$project/config" params={{ project: param }} search={{}}>
              Config
            </Link>{" "}
            screen.
          </Typography>
        </Paper>
      ) : null}

      <AddMachineDialog open={adding} project={route.name} onClose={() => setAdding(false)} />
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/machines")({
  component: ProjectMachines,
});

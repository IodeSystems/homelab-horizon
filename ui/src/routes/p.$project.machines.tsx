/**
 * `/$project/machines` — the boxes this project's instances run on.
 *
 * # A DERIVED LIST, AND THE ONLY ONE WITH NO DETAIL ROUTE UNDER IT
 *
 * `config.Machine` is `{Name, Segments, Note}` and the comment above it states
 * the absence as the model's shape: *"a Project field on a machine would be
 * false for that row the day it was added, and every screen built on it would
 * inherit the lie."* So a project does not OWN a machine. This list is a query
 * — the distinct machines over the project's instances — and `gw-1` appears on
 * every project it hosts and on every ancestor of each, carrying no project in
 * any of them.
 *
 * Which is why `/$project/machines/$machine` does not exist and must never.
 * A detail route here would give one box one URL per hosting project, and its
 * diff one home per URL — the exact failure plan/design/ui.md Decision 1's
 * reason 3 names. Every machine name below links to `/machines/$machine`: one
 * box, one page, every instance on it regardless of project.
 *
 * # WHICH REGISTRATIONS, SAID OUT LOUD
 *
 * **Approved only, asked for explicitly.** `useCMRegistrations()` with no
 * argument keys itself `"all"` and sends no `state`, and the handler defaults an
 * empty `state` to PENDING — there is no "all". A machines-per-project screen
 * built on the bare hook would list exactly the boxes nobody has approved yet
 * and look entirely plausible doing it. Approved is the right list anyway: hz
 * declares a version for an approved address and none for a pending one, so a
 * pending row is a box asking to be here, not a box that is.
 *
 * The pending ones are counted and named in a panel of their own rather than
 * dropped, because a queue that does not say it is filtered hides a waiting
 * box.
 *
 * # IT IS EMPTY ON THE LIVE GATEWAY TODAY
 *
 * All seven `cm_*` tables are empty (plan.md Tier 0, measured 2026-09-23), so
 * every project's list here is correctly empty until enrolment lands. That is
 * the state the operator will actually see, so the empty state says what it
 * means — no instance of this project runs on any box yet — rather than
 * rendering a blank area they have to interpret.
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
import type { CMRegistrationResp } from "../api/generated-types";
import { useCMRegistrations } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

interface DerivedMachine {
  machine: string;
  /** The instances of THIS project's scope that put the box on this list. */
  instances: CMRegistrationResp[];
  /** The distinct projects, in scope, with an instance on this box. */
  projects: string[];
}

/** Distinct machines over the in-scope instances, each carrying its reasons. */
export function deriveMachines(regs: CMRegistrationResp[]): DerivedMachine[] {
  const byMachine = new Map<string, DerivedMachine>();
  for (const r of regs) {
    const name = r.machineName || "(unnamed machine)";
    let row = byMachine.get(name);
    if (!row) {
      row = { machine: name, instances: [], projects: [] };
      byMachine.set(name, row);
    }
    row.instances.push(r);
    if (r.project && !row.projects.includes(r.project)) row.projects.push(r.project);
  }
  return [...byMachine.values()].sort((a, b) => a.machine.localeCompare(b.machine));
}

function address(r: CMRegistrationResp): string {
  return [r.project, r.environment, r.app, r.role].filter(Boolean).join("/");
}

function ProjectMachines() {
  const { index, resolution, scope, param } = useProjectContext();
  // EXPLICIT STATES, BOTH OF THEM. The bare hook would ask for pending and say
  // "all" in its query key.
  const approved = useCMRegistrations("approved");
  const pending = useCMRegistrations("pending");

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const allApproved = approved.data ?? [];
  const mine = allApproved.filter((r) => inScope(reading.names, r.project));
  const rows = deriveMachines(mine);
  const waiting = (pending.data ?? []).filter((r) => inScope(reading.names, r.project));

  return (
    <Box>
      <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
        Machines running {route.name}
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
        A machine carries no project — the same box hosts instances from several at once. This list
        is derived through this project's instances, so a box appears here because something of{" "}
        {route.name}'s runs on it, not because it belongs to {route.name}. Each name links to the
        machine's own page, which is the only page it has and lists every instance on it.
      </Typography>

      <ScopeControl reading={reading} to="/p/$project/machines" param={param} />

      {/* Error BEFORE loading: a failed query holding no data is reset to
          pending on mount by React Query's retryOnMount, so checking isLoading
          first renders a permanent spinner over a read that already failed. */}
      {approved.error ? (
        <Alert severity="error">
          hz could not be asked which instances it has approved:{" "}
          {approved.error instanceof Error ? approved.error.message : String(approved.error)}. This
          list is empty because the read failed, not because nothing runs here.
        </Alert>
      ) : approved.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which instances it has approved…</Typography>
        </Box>
      ) : rows.length === 0 ? (
        <Alert severity="info">
          No instance of{" "}
          {reading.scope === "own" ? route.name : `${route.name} or anything below it`} runs on any
          box yet, so there is no machine to derive. hz has {allApproved.length} approved instance
          {allApproved.length === 1 ? "" : "s"} in total across every project. This is an ordinary
          state — a project is declared, and rungs are declared, before a box ever registers — and it
          is not the same as hz having no machines: the ones it declares are on{" "}
          <Link to="/machines">Machines</Link>.
        </Alert>
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Machine</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                <TableCell>What of this project runs on it</TableCell>
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
                    {row.projects.map((p) => (
                      <LocationCell key={p} index={index} project={p} />
                    ))}
                  </TableCell>
                  <TableCell>
                    <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                      {row.instances.map((r) => (
                        <Chip
                          key={r.id}
                          size="small"
                          variant="outlined"
                          label={address(r)}
                          sx={{ fontFamily: "monospace" }}
                        />
                      ))}
                    </Box>
                    <Typography variant="caption" sx={{ color: "text.secondary" }}>
                      {row.instances.length} instance{row.instances.length === 1 ? "" : "s"} of this
                      project. The box may run others for other projects — its own page lists every
                      one.
                    </Typography>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          Waiting for approval — {waiting.length} address{waiting.length === 1 ? "" : "es"} in this
          project
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          {waiting.length === 0
            ? "No box has asked to run anything of this project's without being granted it. The list above is every approved instance and nothing is held back from it."
            : `${waiting.map((r) => `${r.machineName || "an unnamed box"} has asked to run ${address(r)}`).join("; ")} — and nobody has granted it. Those boxes are NOT on the list above: hz declares no version for an address nobody approved, and a box that could put itself on a screen by booting is a box that has approved itself.`}{" "}
          Approvals are on this project's <Link to="/p/$project/config" params={{ project: param }} search={{}}>Config</Link> screen.
        </Typography>
      </Paper>
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/machines")({
  component: ProjectMachines,
});

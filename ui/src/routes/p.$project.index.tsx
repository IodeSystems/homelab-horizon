/**
 * `/p/$project` — what one project declares. Its Overview tab.
 *
 * The overview that `/projects` used to render into a detail panel beside a
 * picker held in `useState`. It is a route now, so "the storefront project" has
 * an address: it can be linked, bookmarked, and put in a ticket.
 *
 * Amendment 6 (flows over prose): tables, one-line empty states with the Add
 * button beside them, and the explanation in each chip's title rather than in
 * a paragraph. The four states from plan/design/example-projection.md §4 still
 * render as different cells — they are just not paragraphs any more:
 *
 *   ONE ENVIRONMENT is the common case, not an unfinished ladder.
 *   NAME ≠ POSTURE: two fields, two meanings, never merged into one column.
 *   NO DECLARED VERSION means NO PACKAGE AT ALL, not "latest".
 *   NO MACHINE is the ordinary early state of a rung — and is different again
 *   from hz being unable to say, which is why placement has three answers.
 *
 * A fifth, which this screen is the only one to hit: NO ENVIRONMENT AT ALL.
 * Three of the eight live projects are in that state and it is not an error —
 * a project is declared before anything moves into it.
 */
import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Chip,
  IconButton,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import DeleteOutlineIcon from "@mui/icons-material/DeleteOutlined";
import EditOutlinedIcon from "@mui/icons-material/EditOutlined";
import { useEnvironments, useServices, useVersionDrift } from "../api/hooks";
import type { EnvironmentResp, InstanceVersion } from "../api/generated-types";
import {
  readFeed,
  readInstanceSource,
  readPlacement,
  readRung,
} from "../components/model/model";
import {
  inScope,
  CONFIG_AT,
  type ProjectIndex,
  type ProjectRoute,
} from "../components/model/projectRoutes.ts";
import { CannotAskBanner, ToneChip } from "../components/model/ModelBits";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ProjectLink,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";
import {
  AddEnvironmentDialog,
  EditEnvironmentDialog,
  RemoveEnvironmentDialog,
} from "../components/model/EnvironmentDialogs";

/** Posture colours the ladder, never the name. The name is plain text. */
function postureTone(posture: string) {
  switch (posture) {
    case "prod":
      return "fault" as const;
    case "staging":
      return "late" as const;
    case "dev":
      return "neutral" as const;
    default:
      return "hatched" as const;
  }
}

/**
 * One rung, one row. The sentence each cell used to carry is its title, so a
 * hover still says why "none" means NO PACKAGE AT ALL, not "latest".
 */
function RungRow({
  env,
  instances,
  index,
  onEdit,
  onRemove,
}: {
  env: EnvironmentResp;
  instances: InstanceVersion[] | null;
  index: ProjectIndex;
  /** Both act on `env.project` — the rung's OWN project, never the page's. */
  onEdit: (env: EnvironmentResp) => void;
  onRemove: (env: EnvironmentResp) => void;
}) {
  const rung = readRung(env);
  const placement = readPlacement(env, instances);
  return (
    <TableRow hover>
      <TableCell sx={{ fontFamily: "monospace", fontWeight: 700 }} title={rung.nameNote}>
        {env.name}
      </TableCell>
      <TableCell>
        <ToneChip label={env.posture || "none"} tone={postureTone(env.posture)} />
      </TableCell>
      <TableCell>
        <LocationCell index={index} project={env.project} />
      </TableCell>
      <TableCell sx={{ fontFamily: "monospace" }}>{env.from || "—"}</TableCell>
      <TableCell sx={{ fontFamily: "monospace" }} title={rung.versionNote}>
        {env.version || (
          <Typography component="span" variant="body2" sx={{ color: "warning.main" }}>
            none — installs nothing
          </Typography>
        )}
      </TableCell>
      <TableCell title={placement.meaning}>
        <ToneChip
          label={placement.headline}
          tone={placement.tone}
          hatched={placement.knowledge === "unknown"}
          dashed={placement.knowledge === "unknown"}
        />
      </TableCell>
      <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
        <IconButton
          size="small"
          aria-label={`Edit ${env.project}/${env.name}…`}
          title={`Edit ${env.project}/${env.name}…`}
          onClick={() => onEdit(env)}
          sx={{ color: "text.secondary" }}
        >
          <EditOutlinedIcon fontSize="small" />
        </IconButton>
        <IconButton
          size="small"
          aria-label={`Remove ${env.project}/${env.name}…`}
          title={`Remove ${env.project}/${env.name}… (shows what it would take first)`}
          onClick={() => onRemove(env)}
          sx={{ color: "text.secondary" }}
        >
          <DeleteOutlineIcon fontSize="small" />
        </IconButton>
      </TableCell>
    </TableRow>
  );
}

/** Where this project sits, in one line: its parent and its subprojects. */
function ProjectHeader({ route, index }: { route: ProjectRoute; index: ProjectIndex }) {
  const children = route.children
    .map((name) => index.byName.get(name))
    .filter((r): r is ProjectRoute => r !== undefined);
  const parent = route.parent ? index.byName.get(route.parent) : undefined;
  if (!parent && children.length === 0) return null;
  return (
    <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap", mb: 2, alignItems: "center" }}>
      {parent ? (
        <Typography variant="body2" sx={{ color: "text.secondary", mr: 1 }}>
          child of <ProjectLink index={index} route={parent} />
        </Typography>
      ) : null}
      {children.length > 0 ? (
        <>
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            subprojects:
          </Typography>
          {children.map((c) => (
            <Link
              key={c.name}
              to="/p/$project"
              params={{ project: c.name }}
              search={{}}
              style={{ textDecoration: "none" }}
            >
              <Chip
                size="small"
                variant="outlined"
                label={c.name}
                sx={{ fontFamily: "monospace", cursor: "pointer" }}
              />
            </Link>
          ))}
        </>
      ) : null}
    </Box>
  );
}

function ProjectOverview() {
  const { index, resolution, scope, param } = useProjectContext();
  const environments = useEnvironments();
  const services = useServices();
  const drift = useVersionDrift();
  const navigate = useNavigate();
  // "+" always declares into THIS page's own project (never a descendant
  // merely visible under the own+descendants scope); edit/remove act on
  // whichever project the targeted rung actually belongs to.
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<EnvironmentResp | null>(null);
  const [removing, setRemoving] = useState<EnvironmentResp | null>(null);

  // The layout above renders the no-such-project screen and no <Outlet/>, so
  // this component only ever runs with a resolved project.
  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const rungs = (environments.data ?? []).filter((e) => inScope(reading.names, e.project));
  const ownRungs = (environments.data ?? []).filter((e) => e.project === route.name);
  const editingSiblings = (environments.data ?? []).filter((e) => e.project === editing?.project);
  const mine = (services.data ?? []).filter((s) => inScope(reading.names, s.project));

  // null, deliberately, and not []: hz has not answered, which is a different
  // answer from "nothing is placed" (invariant 2).
  const source = readInstanceSource(drift, "The placement column below");
  const instances: InstanceVersion[] | null = source.known ? source.instances : null;
  const feed = readFeed(route.project);
  const resolvedFeed = route.project.resolvedFeed;
  const addEnv = (
    <AddButton
      label="Add environment"
      ariaLabel={`Add an environment to ${route.name}`}
      onClick={() => setAdding(true)}
    />
  );
  const addService = (
    <AddButton
      label="Add service"
      onClick={() => navigate({ to: "/p/$project/services", params: { project: param }, search: {} })}
    />
  );

  return (
    <Box>
      <ProjectHeader route={route} index={index} />
      <ScopeControl reading={reading} to="/p/$project" param={param} />

      {source.known ? null : <CannotAskBanner what={source.what} detail={source.detail} />}
      {source.known && source.unadmitted > 0 ? (
        <Alert severity="info" sx={{ mb: 2 }}>
          {source.unadmitted} registration{source.unadmitted === 1 ? "" : "s"} waiting for approval
          —{" "}
          <Link to={CONFIG_AT} params={{ project: param }} search={{}}>
            review in Config
          </Link>
          .
        </Alert>
      ) : null}

      <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap", mb: 3 }}>
        <Typography variant="body2" sx={{ fontWeight: 700 }}>
          Package feed
        </Typography>
        <ToneChip label={feed.headline} tone={feed.tone} />
        {resolvedFeed ? (
          <Typography variant="body2" sx={{ fontFamily: "monospace", color: "text.secondary" }} title={feed.meaning}>
            {resolvedFeed.url} {resolvedFeed.suite}/{resolvedFeed.component}
            {resolvedFeed.keyId ? ` · key ${resolvedFeed.keyId}` : " · unsigned"}
          </Typography>
        ) : null}
      </Box>

      <TabHeader title="Environments" action={addEnv} />
      {environments.error ? (
        <Alert severity="error" sx={{ mb: 3 }}>
          hz could not be asked which environments it declares: {environments.error.message}
        </Alert>
      ) : environments.isLoading ? (
        <Typography sx={{ color: "text.secondary", mb: 3 }}>Asking hz which environments it declares…</Typography>
      ) : rungs.length === 0 ? (
        <Box sx={{ mb: 3 }}>
          <EmptyRow text={`No environments in ${route.name}.`} action={addEnv} />
        </Box>
      ) : (
        <TableContainer component={Paper} sx={{ mb: 3 }}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Environment</TableCell>
                <TableCell>Posture</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                <TableCell>From</TableCell>
                <TableCell>Version</TableCell>
                <TableCell>Placement</TableCell>
                <TableCell />
              </TableRow>
            </TableHead>
            <TableBody>
              {[...rungs]
                .sort((a, b) => a.project.localeCompare(b.project) || a.name.localeCompare(b.name))
                .map((e) => (
                  <RungRow
                    key={`${e.project}/${e.name}`}
                    env={e}
                    instances={instances}
                    index={index}
                    onEdit={setEditing}
                    onRemove={setRemoving}
                  />
                ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <TabHeader title="Services" action={addService} />
      {services.error ? (
        <Alert severity="error">hz could not be asked which services it serves: {services.error.message}</Alert>
      ) : services.isLoading ? (
        <Typography sx={{ color: "text.secondary" }}>Asking hz which services it serves…</Typography>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No services in ${route.name}.`} action={addService} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Service</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                <TableCell>Domains</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((s) => (
                <TableRow key={s.name} hover>
                  <TableCell sx={{ fontFamily: "monospace", fontWeight: 600 }}>{s.name}</TableCell>
                  <TableCell>
                    <LocationCell index={index} project={s.project} />
                  </TableCell>
                  <TableCell sx={{ color: "text.secondary" }}>{s.domains.join(" · ")}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <AddEnvironmentDialog
        open={adding}
        project={route.name}
        siblings={ownRungs}
        onClose={() => setAdding(false)}
      />
      <EditEnvironmentDialog env={editing} siblings={editingSiblings} onClose={() => setEditing(null)} />
      <RemoveEnvironmentDialog target={removing} onClose={() => setRemoving(null)} />
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/")({
  component: ProjectOverview,
});

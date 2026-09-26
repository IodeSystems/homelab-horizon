/**
 * The pieces the four project-scoped screens share.
 *
 * `projectRoutes.ts` decides; this draws. Same split, same reason, as
 * `model.ts`/`ModelBits.tsx`: deleting the Location cell's project name, or
 * turning the scope link into a button that flips component state, passes tsc
 * and vite and every decision check. `projectRoutes.render.selftest.tsx`
 * renders these through the REAL router and counts what must be there.
 *
 * ONE RULE ABOVE THE OTHERS: every control that changes what the page shows is
 * a `<Link>`. A `<Button onClick={setState}>` here would render the same pixels
 * and lose the only property `/$project` exists to buy — that the screen you
 * are looking at has an address you can send to somebody else.
 */
import { useMemo } from "react";
import {
  Alert,
  Box,
  Chip,
  List,
  ListItemButton,
  ListItemText,
  Paper,
  Typography,
} from "@mui/material";
import { Link, useParams, useSearch } from "@tanstack/react-router";
import { useProjects } from "../../api/hooks";
import type { UseQueryResult } from "@tanstack/react-query";
import type { ProjectResp } from "../../api/generated-types";
import {
  buildProjectIndex,
  parseScope,
  projectParam,
  projectReachable,
  readLocation,
  readScope,
  resolveProjectName,
  type ProjectIndex,
  type ProjectNavTo,
  type ProjectResolution,
  type ProjectRoute,
  type ProjectScope,
  type ScopeReading,
} from "./projectRoutes.ts";

/** The shape every `/$project/…` screen reads: who we are, and how wide. */
export interface ProjectContext {
  projects: UseQueryResult<ProjectResp[]>;
  /** The raw `$project` parameter, exactly as it was typed. */
  param: string;
  index: ProjectIndex;
  resolution: ProjectResolution;
  scope: ProjectScope;
}

/**
 * Resolve the URL into a project, for any screen under `/$project`.
 *
 * Every child re-resolves rather than being handed the answer through a
 * context: the resolution is a pure function of two queries the child already
 * has, and a context would make each child unrenderable on its own.
 */
export function useProjectContext(): ProjectContext {
  const projects = useProjects();
  const { project: param } = useParams({ from: "/p/$project" });
  const search = useSearch({ from: "/p/$project" });
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const resolution = useMemo(() => resolveProjectName(index, param), [index, param]);
  return { projects, param, index, resolution, scope: parseScope(search.scope) };
}

/** The scope reading for a resolved project, ready to render. */
export function scopeOf(route: ProjectRoute, scope: ProjectScope): ScopeReading {
  return readScope(route, scope);
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

/** A link to a project's own screen, addressed the way that project addresses. */
export function ProjectLink({
  index,
  route,
  children,
}: {
  index: ProjectIndex;
  route: ProjectRoute;
  children?: React.ReactNode;
}) {
  return (
    <Link
      to="/p/$project"
      params={{ project: projectParam(index, route) }}
      search={{}}
      style={{ color: "inherit" }}
    >
      {children ?? route.name}
    </Link>
  );
}

// ---------------------------------------------------------------------------
// The two ways the parameter can fail to name one project
// ---------------------------------------------------------------------------

/**
 * A path that names no project.
 *
 * NOT A 404, deliberately. `/p/nosuch` — or any path no route claims, via the
 * splat route — arrives here, and the useful answer is "that is not a project,
 * here are the ones that are", with every one of them a link. The router's
 * generic not-found would say less and offer nothing.
 */
export function NoSuchProject({
  index,
  headline,
  meaning,
}: {
  index: ProjectIndex;
  headline: string;
  meaning: string;
}) {
  return (
    <Box>
      <Alert severity="warning" sx={{ mb: 2 }}>
        <Typography sx={{ fontWeight: 700 }}>{headline}</Typography>
        <Typography variant="body2">{meaning}</Typography>
      </Alert>
      <ProjectPickList index={index} title="The projects hz declares" />
      <Box sx={{ mt: 2 }}>
        <Link to="/">Back to the overview</Link>
      </Box>
    </Box>
  );
}

/**
 * A path that names MORE than one project.
 *
 * Reachable because a project name may itself contain a dot, which nothing in
 * `config` refuses today — so `c` under `b` under `a` and `b.c` under `a` both
 * address as `a.b.c`. hz refuses to pick: showing one project's rows under a
 * name that also means another is worse than asking.
 */
export function AmbiguousProject({
  index,
  headline,
  meaning,
  candidates,
}: {
  index: ProjectIndex;
  headline: string;
  meaning: string;
  candidates: ProjectRoute[];
}) {
  return (
    <Box>
      <Alert severity="warning" sx={{ mb: 2 }}>
        <Typography sx={{ fontWeight: 700 }}>{headline}</Typography>
        <Typography variant="body2">{meaning}</Typography>
      </Alert>
      <Paper sx={{ p: 2 }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 1 }}>
          Which did you mean?
        </Typography>
        <List dense disablePadding>
          {candidates.map((c) => (
            <Link
              key={c.name}
              to="/p/$project"
              params={{ project: projectParam(index, c) }}
              search={{}}
              style={{ textDecoration: "none", color: "inherit" }}
            >
              <ListItemButton>
                <ListItemText
                  primary={c.name}
                  secondary={
                    projectReachable(index, c)
                      ? c.parent
                        ? `under ${c.parent}`
                        : "a root project"
                      : "This project has no address of its own — every form of its name is claimed by another project. Rename one of them with hz to reach it."
                  }
                  slotProps={{ primary: { sx: { fontFamily: "monospace" } } }}
                />
              </ListItemButton>
            </Link>
          ))}
        </List>
      </Paper>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// The tree, as a list of links
// ---------------------------------------------------------------------------

/**
 * Every project, depth-ordered, each row a link.
 *
 * THE SELECTION IS THE URL. The screen this replaces held it in
 * `useState<string>("")` and fell back to the first project when the selected
 * one disappeared, so there was no address for "the redline project" at all.
 * Clicking here navigates; nothing here sets state.
 */
export function ProjectPickList({
  index,
  title,
  current,
}: {
  index: ProjectIndex;
  title: string;
  /** The project being shown, so its row reads as the one you are on. */
  current?: string;
}) {
  if (index.routes.length === 0) {
    return (
      <Alert severity="info">
        hz declares no project. This is an empty tree, not a failed read — nothing has been declared
        yet, and until a project exists there is no rung for an instance to name.
      </Alert>
    );
  }
  return (
    <Paper sx={{ p: 1 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700, px: 1, py: 1 }}>
        {title}
      </Typography>
      <List dense disablePadding>
        {index.routes.map((r) => (
          <Link
            key={r.name}
            to="/p/$project"
            params={{ project: projectParam(index, r) }}
            search={{}}
            style={{ textDecoration: "none", color: "inherit" }}
          >
            <ListItemButton
              selected={r.name === current}
              sx={{ pl: 1 + r.depth * 2 }}
            >
              <ListItemText
                primary={r.name}
                secondary={
                  r.descendants.length > 0
                    ? `${r.dotted} · ${r.descendants.length} below`
                    : r.parent
                      ? `${r.dotted}`
                      : "root"
                }
                slotProps={{
                  primary: { sx: { fontFamily: "monospace" } },
                  secondary: { sx: { fontFamily: "monospace", fontSize: "0.7rem" } },
                }}
              />
            </ListItemButton>
          </Link>
        ))}
      </List>
    </Paper>
  );
}

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

/**
 * The own/own-plus-descendants control, as a link.
 *
 * A SEARCH PARAM AND NOT `useState`. The narrowed view has its own URL, so it
 * can be linked, bookmarked and pasted into a ticket — which is the argument
 * the whole amendment rests on, applied to the one control that would most
 * obviously have been a toggle.
 *
 * It renders even for a project with no descendants, saying so, rather than
 * disappearing: a control that is sometimes absent teaches the operator that
 * the screen is unpredictable.
 */
export function ScopeControl({
  reading,
  to,
  param,
}: {
  reading: ScopeReading;
  /** The route this control switches within. */
  to: ProjectNavTo;
  param: string;
}) {
  return (
    // data-scope-control marks this region so the render check can assert the
    // control ITSELF is a link, rather than being satisfied by some other link
    // to the same URL elsewhere on the page.
    <Paper data-scope-control variant="outlined" sx={{ p: 1.5, mb: 2, bgcolor: "transparent" }}>
      <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap" }}>
        <Typography variant="caption" sx={{ textTransform: "uppercase", letterSpacing: 0.5, color: "text.secondary" }}>
          showing
        </Typography>
        <Chip size="small" label={reading.headline} sx={{ fontWeight: 700 }} />
        <Link
          to={to}
          params={{ project: param }}
          search={reading.other === "own" ? { scope: "own" as const } : {}}
          style={{ color: "inherit" }}
        >
          {reading.otherLabel}
        </Link>
      </Box>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mt: 0.5 }}>
        {reading.meaning}
      </Typography>
    </Paper>
  );
}

// ---------------------------------------------------------------------------
// Location
// ---------------------------------------------------------------------------

/**
 * Which project a row lives in.
 *
 * ALWAYS THE ROW'S OWN PROJECT NAME — on its own project's page too. The cell
 * is never blank, never a dash, and never says "this project": the row is going
 * to be screenshotted into a ticket, and the reader of that ticket cannot see
 * which page it came from.
 */
export function LocationCell({
  index,
  project,
}: {
  index: ProjectIndex;
  project: string | undefined | null;
}) {
  const reading = readLocation(project, index);
  const route = reading.linkTo ? index.byName.get(reading.label) : undefined;
  return (
    <Box>
      {route ? (
        <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
          <ProjectLink index={index} route={route} />
        </Typography>
      ) : (
        <Typography
          variant="body2"
          sx={{
            fontFamily: "monospace",
            color: reading.assigned ? "warning.main" : "text.secondary",
            fontStyle: reading.assigned ? "normal" : "italic",
          }}
        >
          {reading.label}
        </Typography>
      )}
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
        {reading.assigned ? "" : "not assigned to any project"}
      </Typography>
    </Box>
  );
}

/** The header every table with a Location column uses, so the label cannot drift. */
export const LOCATION_COLUMN_LABEL = "Location";

/** The header the two FLAT lists use — they name a project, not a location. */
export const PROJECT_COLUMN_LABEL = "Project";

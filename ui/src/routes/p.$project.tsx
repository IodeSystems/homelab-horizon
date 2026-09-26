/**
 * `/$project` — the layout every project-scoped screen sits inside.
 *
 * The job (plan/design/ui.md, Decision 1 amended): a project scopes a URL.
 * Four screens carry one because their records carry one — `Project` on the
 * project, on the environment, on the service, on the config registration — and
 * seventeen do not, because `config.Machine` is `{Name, Segments, Note}` and a
 * Project field on it "would be false for that row the day it was added".
 *
 * ## THIS FILE MUST RENDER AN `<Outlet/>` AND HAS A TEST THAT SAYS SO
 *
 * TanStack nests file routes by name prefix, so `$project.services.tsx` is a
 * CHILD of this file. A layout whose component renders no `<Outlet/>` swallows
 * every child silently: `/acme-co/services` would render THIS screen, with no
 * error, no warning and no type error. The sibling `redline` repo shipped that
 * exact bug — `code.$code.tsx` swallowed by `code.tsx`, so every invite link
 * showed the manual-entry form instead.
 *
 * `projectRoutes.render.selftest.tsx` drives the real router at
 * `/<project>/domains` and asserts the DOMAINS screen is what comes back. If
 * you remove the Outlet below, that check goes red and nothing else does.
 *
 * ## The parameter is resolved by lookup
 *
 * `/iodesystems.redline/domains` carries the whole dotted path in one segment,
 * because a file route needs a fixed nesting depth and a project tree does not
 * have one. Nothing splits it on "." — see `projectRoutes.ts` for why that
 * cannot be made correct, and for the collision the amendment believed was
 * impossible.
 */
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { Alert, Box, Chip, CircularProgress, Paper, Typography } from "@mui/material";
import {
  AmbiguousProject,
  NoSuchProject,
  useProjectContext,
} from "../components/model/ProjectBits";
import {
  parseScope,
  projectBack,
  projectParam,
  type ProjectIndex,
  type ProjectRoute,
  type ProjectScope,
} from "../components/model/projectRoutes.ts";

/**
 * The scope, carried in the URL.
 *
 * `all` is the default and is rendered as NO search param at all, so the plain
 * `/acme-co/services` is the canonical address rather than
 * `/acme-co/services?scope=all`. Anything unrecognised reads as the default: a
 * truncated link should show the whole project, not an error.
 */
interface ProjectSearch {
  scope?: ProjectScope;
}

/**
 * Who you are looking at.
 *
 * IDENTITY ONLY — THE PICKER IS GONE. The sidebar's project zone IS the
 * switcher now: it holds the tree at the top level, this project's nav once you
 * are inside one, and the link back up. A second control on the page doing the
 * same job is two controls for one job, and the one on the page was the third
 * column this amendment removes.
 *
 * What stays is the header's own sentence: what this project is, what it
 * inherits, and what sits below it. The "← parent" link is repeated here as an
 * in-page affordance so the way up is visible without going to the sidebar,
 * which on a phone is behind the hamburger.
 */
function ProjectHeader({ route, index }: { route: ProjectRoute; index: ProjectIndex }) {
  const back = projectBack(index, route);
  const children = route.children
    .map((name) => index.byName.get(name))
    .filter((r): r is ProjectRoute => r !== undefined);
  return (
    <Paper sx={{ p: 2, mb: 2 }}>
      <Box sx={{ mb: 0.5 }}>
        {back.toEstate ? (
          <Link to="/projects" style={{ color: "inherit", fontSize: "0.85rem" }}>
            {back.label}
          </Link>
        ) : (
          <Link
            to="/$project"
            params={{ project: back.param }}
            search={{}}
            style={{ color: "inherit", fontSize: "0.85rem" }}
          >
            {back.label}
          </Link>
        )}
      </Box>
      <Box sx={{ display: "flex", gap: 1.5, alignItems: "baseline", flexWrap: "wrap" }}>
        <Typography variant="h4" sx={{ fontWeight: 700, fontFamily: "monospace" }}>
          {route.name}
        </Typography>
        <Typography variant="body2" sx={{ fontFamily: "monospace", color: "text.secondary" }}>
          {route.dotted}
        </Typography>
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary", mt: 0.5 }}>
        {route.parent ? (
          <>
            A child of{" "}
            <Link
              to="/$project"
              params={{ project: projectParam(index, index.byName.get(route.parent)!) }}
              search={{}}
              style={{ color: "inherit" }}
            >
              {route.parent}
            </Link>
            . It inherits the package feed and nothing else — not a version, not a machine, not a
            service.
          </>
        ) : (
          "A root project. It inherits nothing; what it declares cascades down."
        )}
        {route.descendants.length > 0
          ? ` ${route.descendants.length} project${route.descendants.length === 1 ? "" : "s"} sit${route.descendants.length === 1 ? "s" : ""} below it.`
          : " Nothing sits below it."}
      </Typography>

      {/* The way DOWN, on the page as well as in the sidebar. Not a picker — it
          reaches this project's own children and nothing else. It is here
          because below `md` the sidebar is a drawer behind the hamburger, and a
          header that says "3 projects sit below it" with no way to reach any of
          them is the unaskable state this whole surface is organised against. */}
      {children.length > 0 ? (
        <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap", mt: 1, alignItems: "center" }}>
          <Typography variant="caption" sx={{ color: "text.secondary" }}>
            enter a subproject:
          </Typography>
          {children.map((c) => (
            <Link
              key={c.name}
              to="/$project"
              params={{ project: projectParam(index, c) }}
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
        </Box>
      ) : null}
    </Paper>
  );
}

function ProjectLayout() {
  const { projects, index, resolution } = useProjectContext();

  if (projects.isLoading) {
    return (
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 4 }}>
        <CircularProgress size={24} />
        <Typography>Asking hz which projects it declares…</Typography>
      </Box>
    );
  }

  if (projects.error) {
    const err = projects.error;
    return (
      <Alert severity="error">
        hz could not be asked which projects it declares:{" "}
        {err instanceof Error ? err.message : String(err)}. Nothing on this screen is a fact about a
        project — the failure is between this browser and hz.
      </Alert>
    );
  }

  if (!resolution.found) {
    // No <Outlet/> here, and that is deliberate: there is no project to scope a
    // child screen to, so rendering one would list nothing and imply the
    // project exists but is empty.
    return resolution.ambiguous ? (
      <AmbiguousProject
        index={index}
        headline={resolution.headline}
        meaning={resolution.meaning}
        candidates={resolution.candidates}
      />
    ) : (
      <NoSuchProject index={index} headline={resolution.headline} meaning={resolution.meaning} />
    );
  }

  return (
    <Box>
      <ProjectHeader route={resolution.route} index={index} />
      {/* NO TREE COLUMN AND NO TAB STRIP. Both moved into the sidebar's project
          zone — the column was a third column beside the sidebar and the page,
          and the tabs were a second copy of the nav the sidebar now holds.
          There is no `isMobile` branch left here either: the sidebar is already
          a Drawer below `md`, so the project nav gets the responsive behaviour
          the shell already has, at both widths, with no per-page branch. */}
      {/* THE OUTLET. Remove it and every child screen silently renders this
          page instead. projectRoutes.render.selftest.tsx is the check. */}
      <Outlet />
    </Box>
  );
}

export const Route = createFileRoute("/$project")({
  component: ProjectLayout,
  validateSearch: (search: Record<string, unknown>): ProjectSearch => {
    const scope = parseScope(search.scope);
    return scope === "all" ? {} : { scope };
  },
});

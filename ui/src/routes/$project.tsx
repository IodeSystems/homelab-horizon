/**
 * `/$project` — the layout every project-scoped screen sits inside.
 *
 * The job (plan/ui-redesign.md, Decision 1 amended): a project scopes a URL.
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
import {
  createFileRoute,
  Link,
  Outlet,
  useRouterState,
} from "@tanstack/react-router";
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Divider,
  Paper,
  Typography,
  useMediaQuery,
  useTheme,
} from "@mui/material";
import {
  AmbiguousProject,
  NoSuchProject,
  ProjectPickList,
  useProjectContext,
} from "../components/model/ProjectBits";
import {
  parseScope,
  projectParam,
  PROJECT_TABS,
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

/** The four screens, as tabs that are links. Never a `useState` tab index. */
function ProjectTabs({ param, current }: { param: string; current: string }) {
  return (
    <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap", mb: 2 }}>
      {PROJECT_TABS.map((t) => {
        const active = t.to === current;
        return (
          <Link
            key={t.to}
            to={t.to}
            params={{ project: param }}
            search={{}}
            style={{ textDecoration: "none" }}
          >
            <Chip
              label={t.label}
              variant={active ? "filled" : "outlined"}
              color={active ? "primary" : "default"}
              sx={{ fontWeight: active ? 700 : 400, cursor: "pointer" }}
            />
          </Link>
        );
      })}
    </Box>
  );
}

/**
 * Who you are looking at, and how to get to another one.
 *
 * THE SWITCHER IS ON THE PAGE, NOT IN THE NAV. The nav is job-shaped and
 * gateway-wide; a project switcher sitting in it would imply the other fifteen
 * entries follow it, and they do not. On a phone the nav is behind a hamburger,
 * which is the one width where switching matters most.
 */
function ProjectHeader({
  route,
  index,
  compactSwitcher,
}: {
  route: ProjectRoute;
  index: ProjectIndex;
  /** True when no tree column is rendered beside this, so this must carry the switch. */
  compactSwitcher: boolean;
}) {
  return (
    <Paper sx={{ p: 2, mb: 2 }}>
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

      {compactSwitcher ? (
        <>
          <Divider sx={{ my: 1.5 }} />
          <Typography
            variant="caption"
            sx={{ textTransform: "uppercase", letterSpacing: 0.5, color: "text.secondary" }}
          >
            switch project
          </Typography>
          <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap", mt: 0.5 }}>
            {index.routes.map((r) => (
              <Link
                key={r.name}
                to="/$project"
                params={{ project: projectParam(index, r) }}
                search={{}}
                style={{ textDecoration: "none" }}
              >
                <Chip
                  size="small"
                  label={r.name}
                  variant={r.name === route.name ? "filled" : "outlined"}
                  color={r.name === route.name ? "primary" : "default"}
                  sx={{ fontFamily: "monospace", cursor: "pointer" }}
                />
              </Link>
            ))}
          </Box>
        </>
      ) : null}
    </Paper>
  );
}

function ProjectLayout() {
  const { projects, index, resolution, param } = useProjectContext();
  const theme = useTheme();
  // A JS breakpoint, not a CSS one: the tree column must be ABSENT at phone
  // width, not merely hidden, or "the switcher still works on a phone" is a
  // claim nothing can check.
  const isMobile = useMediaQuery(theme.breakpoints.down("md"));
  const pathname = useRouterState({ select: (s) => s.location.pathname });

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

  const route = resolution.route;
  // Which of the four tabs is active, by the path's tail rather than by state.
  const tail = pathname.replace(/\/+$/, "").split("/").pop() ?? "";
  const current =
    tail === "services" || tail === "domains" || tail === "config"
      ? (`/$project/${tail}` as const)
      : ("/$project" as const);

  return (
    <Box>
      <ProjectHeader route={route} index={index} compactSwitcher={isMobile} />
      <ProjectTabs param={param} current={current} />
      <Box sx={{ display: "flex", gap: 2, alignItems: "flex-start" }}>
        {isMobile ? null : (
          <Box sx={{ width: 260, flexShrink: 0 }}>
            <ProjectPickList index={index} title="The tree" current={route.name} />
          </Box>
        )}
        <Box sx={{ flex: 1, minWidth: 0 }}>
          {/* THE OUTLET. Remove it and every child screen silently renders this
              page instead. projectRoutes.render.selftest.tsx is the check. */}
          <Outlet />
        </Box>
      </Box>
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

/**
 * `/p/$project` — the layout every project-scoped screen sits inside.
 *
 * The job (plan/design/ui.md, Decision 1, amendment 5): a project scopes a URL.
 * `/p/<name>/<tab>`, where the name is the project's bare name — unique across
 * the config, so no dotted path and no ambiguity. The breadcrumb and the six
 * tabs are drawn by the shell (`ScopeBar`), not here, because the unscoped
 * screens carry the same tabs and have no layout of their own.
 *
 * ## THIS FILE MUST RENDER AN `<Outlet/>` AND HAS A TEST THAT SAYS SO
 *
 * TanStack nests file routes by name prefix, so `p.$project.services.tsx` is a
 * CHILD of this file. A layout whose component renders no `<Outlet/>` swallows
 * every child silently: `/p/acme-co/services` would render THIS screen, with no
 * error, no warning and no type error. The sibling `redline` repo shipped that
 * exact bug — `code.$code.tsx` swallowed by `code.tsx`, so every invite link
 * showed the manual-entry form instead.
 *
 * `projectRoutes.render.selftest.tsx` drives the real router at
 * `/p/<project>/domains` and asserts the DOMAINS screen is what comes back. If
 * you remove the Outlet below, that check goes red and nothing else does.
 */
import { createFileRoute, Outlet } from "@tanstack/react-router";
import { Alert, Box, CircularProgress, Typography } from "@mui/material";
import { NoSuchProject, useProjectContext } from "../components/model/ProjectBits";
import { parseScope, type ProjectScope } from "../components/model/projectRoutes.ts";

/**
 * The scope, carried in the URL.
 *
 * `all` is the default and is rendered as NO search param at all, so the plain
 * `/p/acme-co/services` is the canonical address rather than
 * `/p/acme-co/services?scope=all`. Anything unrecognised reads as the default: a
 * truncated link should show the whole project, not an error.
 */
interface ProjectSearch {
  scope?: ProjectScope;
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
    return (
      <NoSuchProject index={index} headline={resolution.headline} meaning={resolution.meaning} />
    );
  }

  // THE OUTLET. Remove it and every child screen silently renders this
  // layout instead. projectRoutes.render.selftest.tsx is the check.
  return <Outlet />;
}

export const Route = createFileRoute("/p/$project")({
  component: ProjectLayout,
  validateSearch: (search: Record<string, unknown>): ProjectSearch => {
    const scope = parseScope(search.scope);
    return scope === "all" ? {} : { scope };
  },
});

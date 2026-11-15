/**
 * THE SCOPE BAR — WHAT YOU ARE LOOKING AT, AND WHERE THAT IS.
 *
 * (plan/design/ui.md, Decision 1, amendment 5.) A breadcrumb over six tabs,
 * drawn by the shell above every scoped screen — the six unscoped ones and
 * everything under `/p/<name>`. A gateway screen has no tab and gets no bar.
 *
 *   acme-co › storefront                        ← the scope, each crumb a link
 *   Overview  Services  Domains  Machines  Network  Config
 *
 * Every crumb and every tab is a `<Link>`, and every crumb KEEPS THE TAB: on
 * `/p/storefront/domains`, the `acme-co` crumb goes to `/p/acme-co/domains`.
 * The clear control (no label; its title says what it does) goes to the same
 * tab with no project. Nothing here is state — the bar is a pure function of
 * the pathname, so a pasted link renders the same bar for everybody.
 */
import { useMemo } from "react";
import { Box, Breadcrumbs, Tab, Tabs, Typography } from "@mui/material";
import FilterListOffIcon from "@mui/icons-material/FilterListOff";
import { Link } from "@tanstack/react-router";
import { useProjects } from "../../api/hooks";
import {
  ancestry,
  buildProjectIndex,
  SCOPE_TABS,
  tabTarget,
  type ScopeTab,
} from "./projectRoutes.ts";
import { useScopeLocation } from "./SidebarMenu";

function TabLink({ tab, project }: { tab: ScopeTab; project: string | null }) {
  const target = tabTarget(tab, project);
  return (
    <Link
      to={target.to as "/p/$project"}
      params={target.params as never}
      search={{} as never}
      title={tab.blurb}
      // The whole tab is the link, not just its word: padding lives here, not
      // on the Tab, so a click anywhere on the tab navigates.
      style={{ textDecoration: "none", color: "inherit", display: "block", padding: "10px 16px" }}
    >
      {tab.label}
    </Link>
  );
}

export function ScopeBar() {
  const { project, tab } = useScopeLocation();
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);

  if (!tab) return null;
  const route = project ? index.byName.get(project) : undefined;
  const crumbs = route ? ancestry(index, route) : [];
  const unscoped = tabTarget(tab, null);

  return (
    <Box data-scope-bar sx={{ mb: 2 }}>
      {project ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 0.5, mb: 0.5 }}>
          <Link
            to={unscoped.to as "/"}
            search={{} as never}
            aria-label={`${tab.label} across every project`}
            title={`${tab.label} across every project`}
            style={{ color: "inherit", display: "flex", padding: 4, opacity: 0.7 }}
          >
            <FilterListOffIcon fontSize="small" />
          </Link>
          <Breadcrumbs data-breadcrumb sx={{ fontFamily: "monospace" }}>
            {crumbs.length > 0 ? (
              crumbs.map((c, i) => {
                const t = tabTarget(tab, c.name);
                return i === crumbs.length - 1 ? (
                  <Typography key={c.name} sx={{ fontFamily: "monospace", fontWeight: 700 }}>
                    {c.name}
                  </Typography>
                ) : (
                  <Link
                    key={c.name}
                    to={t.to as "/p/$project"}
                    params={t.params as never}
                    search={{} as never}
                    style={{ color: "inherit", fontFamily: "monospace" }}
                  >
                    {c.name}
                  </Link>
                );
              })
            ) : (
              // Not (yet) a known project: the layout below explains; the
              // crumb still says what the address asked for.
              <Typography sx={{ fontFamily: "monospace", fontWeight: 700 }}>{project}</Typography>
            )}
          </Breadcrumbs>
        </Box>
      ) : null}
      <Tabs
        value={tab.key}
        variant="scrollable"
        scrollButtons="auto"
        allowScrollButtonsMobile
        sx={{ borderBottom: 1, borderColor: "divider", minHeight: 40 }}
      >
        {SCOPE_TABS.map((t) => (
          <Tab
            key={t.key}
            value={t.key}
            label={<TabLink tab={t} project={project} />}
            sx={{ textTransform: "none", minHeight: 40, p: 0 }}
          />
        ))}
      </Tabs>
    </Box>
  );
}

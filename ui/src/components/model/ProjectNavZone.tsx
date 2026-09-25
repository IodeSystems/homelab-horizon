/**
 * The sidebar's PROJECT ZONE — the tree, or the project you are inside.
 *
 * The first of the sidebar's two zones. The second one — the fourteen gateway
 * entries in `AppLayout` — never changes, at any depth; this one is replaced
 * the moment you enter a project.
 *
 * # IT IS A FUNCTION OF THE URL, AND THAT IS THE WHOLE POINT
 *
 * `readProjectZone` (`projectRoutes.ts`) decides from the `$project` parameter
 * alone; this file draws what it decided. Nothing here is `useState`: which
 * project you are in, how deep you are, and which nav is showing are facts
 * about the ADDRESS. A sidebar that morphs on component state cannot be linked,
 * shared or bookmarked, and "open the redline nav" would mean two different
 * screens to two people — which is the argument all three amendments rest on,
 * applied to the navigation itself.
 *
 * It lives beside `ProjectBits.tsx` rather than in it because it renders in the
 * SHELL, on every screen, and must not depend on `useProjectContext` — that
 * hook reads `useParams({ from: "/$project" })` and throws anywhere else.
 */
import { useMemo } from "react";
import { Box, Divider, List, ListItemButton, ListItemText, Tooltip, Typography } from "@mui/material";
import { Link, useRouterState } from "@tanstack/react-router";
import { useProjects } from "../../api/hooks";
import {
  buildProjectIndex,
  projectParam,
  readProjectZone,
  PROJECT_NAV,
  PROJECT_NAV_GAPS,
  type ProjectNavTo,
} from "./projectRoutes.ts";

/**
 * The `$project` parameter the ADDRESS carries, from anywhere in the app.
 *
 * `useProjectContext` cannot be used outside `/$project` — `useParams({from})`
 * throws there — and the sidebar renders on every screen, so it reads the match
 * list instead. THE ROUTE IS THE ONLY SOURCE. Nothing about which project the
 * sidebar is showing may come from `useState`: a nav that morphs on component
 * state cannot be linked, and "open the redline nav" would mean two different
 * screens to two people.
 */
export function useProjectParam(): string | undefined {
  return useRouterState({
    select: (s) => {
      const match = s.matches.find((m) => m.routeId === "/$project");
      return (match?.params as { project?: string } | undefined)?.project;
    },
  });
}

/** Which project-nav entries the current address is inside. */
function useMatchedRouteIds(): string[] {
  return useRouterState({ select: (s) => s.matches.map((m) => m.routeId as string) });
}

const ZONE_CAPTION = {
  textTransform: "uppercase" as const,
  letterSpacing: 0.6,
  fontSize: "0.65rem",
  color: "text.secondary",
  px: 2,
  pt: 1.5,
  pb: 0.5,
  display: "block",
};

/** One row of the sidebar, always a real link. */
function ZoneLink({
  to,
  params,
  label,
  secondary,
  indent = 0,
  selected = false,
  bold = false,
  onNavigate,
}: {
  to: "/projects" | ProjectNavTo;
  params?: { project: string };
  label: string;
  secondary?: string;
  indent?: number;
  selected?: boolean;
  bold?: boolean;
  onNavigate?: () => void;
}) {
  return (
    <Link
      to={to as "/projects"}
      params={params as never}
      search={{} as never}
      style={{ textDecoration: "none", color: "inherit" }}
      onClick={onNavigate}
    >
      <ListItemButton
        selected={selected}
        sx={{
          borderRadius: 1,
          mx: 1,
          pl: 1 + indent * 1.5,
          py: 0.4,
          "&.Mui-selected": {
            bgcolor: "rgba(233, 69, 96, 0.15)",
            "&:hover": { bgcolor: "rgba(233, 69, 96, 0.25)" },
          },
        }}
      >
        <ListItemText
          primary={label}
          secondary={secondary}
          slotProps={{
            primary: { sx: { fontWeight: bold ? 700 : 400, fontSize: "0.88rem" } },
            secondary: { sx: { fontSize: "0.65rem", fontFamily: "monospace" } },
          }}
        />
      </ListItemButton>
    </Link>
  );
}

/**
 * The first of the sidebar's two zones: the project tree, or the project you
 * are inside.
 *
 * THE WHOLE ZONE IS A FUNCTION OF THE URL. `readProjectZone` decides from the
 * `$project` parameter alone; this draws what it decided. There is no open/
 * closed state, no selected project held in a component, and no branch on
 * viewport width — the shell is already a `Drawer` below `md`, so the project
 * nav inherits the responsive behaviour the sidebar already has.
 *
 * The gateway zone below this one does not change, at any depth. Three projects
 * deep and needing Settings costs one click, in the same place on screen every
 * time.
 */
export function SidebarProjectZone({ onNavigate }: { onNavigate?: () => void }) {
  const projects = useProjects();
  const param = useProjectParam();
  const matched = useMatchedRouteIds();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const zone = readProjectZone(index, param);

  const tree = (title: string) => (
    <>
      <Typography variant="caption" sx={ZONE_CAPTION}>
        {title}
      </Typography>
      {projects.isLoading ? (
        <Typography variant="caption" sx={{ px: 2, color: "text.secondary" }}>
          Asking hz which projects it declares…
        </Typography>
      ) : projects.error ? (
        <Typography variant="caption" sx={{ px: 2, color: "warning.main", display: "block" }}>
          hz could not be asked which projects it declares. This list is empty because the read
          failed, not because there are none.
        </Typography>
      ) : index.routes.length === 0 ? (
        <Typography variant="caption" sx={{ px: 2, color: "text.secondary", display: "block" }}>
          hz declares no project yet. This is an empty tree, not a failed read.
        </Typography>
      ) : (
        <List dense disablePadding>
          {index.routes.map((r) => (
            <ZoneLink
              key={r.name}
              to="/$project"
              params={{ project: projectParam(index, r) }}
              label={r.name}
              secondary={r.descendants.length > 0 ? `${r.descendants.length} below` : undefined}
              indent={r.depth}
              onNavigate={onNavigate}
            />
          ))}
        </List>
      )}
    </>
  );

  if (zone.kind === "estate") {
    return <Box data-project-zone="estate">{tree("Projects")}</Box>;
  }

  if (zone.kind === "unresolved") {
    return (
      <Box data-project-zone="unresolved">
        <List dense disablePadding>
          <ZoneLink to="/projects" label="← All projects" bold onNavigate={onNavigate} />
        </List>
        <Typography
          variant="caption"
          sx={{ px: 2, py: 1, color: "warning.main", display: "block" }}
        >
          {zone.meaning} Pick one below.
        </Typography>
        {tree("Projects")}
      </Box>
    );
  }

  const { route, back, children } = zone;
  return (
    <Box data-project-zone="project">
      <List dense disablePadding>
        {/* BACK IS A LINK AND IT NAMES ITS DESTINATION. A bare "Back" does not
            say where it goes, and this one goes UP while the browser's goes
            whence you came — the two are allowed to differ only because the
            label says which is which. */}
        <ZoneLink
          to={back.toEstate ? "/projects" : "/$project"}
          params={back.toEstate ? undefined : { project: back.param }}
          label={back.label}
          bold
          onNavigate={onNavigate}
        />
      </List>

      <Typography variant="caption" sx={ZONE_CAPTION}>
        in this project
      </Typography>
      <Box sx={{ px: 2, pb: 0.5 }}>
        <Typography sx={{ fontFamily: "monospace", fontWeight: 700 }}>{route.name}</Typography>
        <Typography variant="caption" sx={{ fontFamily: "monospace", color: "text.secondary" }}>
          {route.dotted}
        </Typography>
      </Box>

      <List dense disablePadding>
        {PROJECT_NAV.map((entry) => {
          const id = entry.to === "/$project" ? "/$project/" : entry.to;
          return (
            <ZoneLink
              key={entry.to}
              to={entry.to}
              params={{ project: projectParam(index, route) }}
              label={entry.label}
              indent={1}
              selected={matched.includes(id)}
              onNavigate={onNavigate}
            />
          );
        })}
      </List>

      {/* GREYED WITH A REASON, NEVER REMOVED. A nav entry that is absent is
          unaskable: the operator cannot tell "this project has no bans" from
          "hz cannot say", and the support conversation has nowhere to start.
          Each names the gateway screen that does hold the rows. */}
      {/* The heading is conditional on there being entries under it. A caption
          over nothing is the blank area the operator has to interpret — and it
          is what a positive control found: deleting the entries left the
          heading standing, which reads as "there are none" rather than "these
          were removed". */}
      {PROJECT_NAV_GAPS.length > 0 ? (
        <Typography variant="caption" sx={ZONE_CAPTION}>
          cannot be scoped to a project
        </Typography>
      ) : null}
      <List dense disablePadding>
        {PROJECT_NAV_GAPS.map((gap) => (
          <Tooltip key={gap.label} title={gap.why} placement="right">
            <Box>
              <ListItemButton
                disabled
                sx={{ borderRadius: 1, mx: 1, pl: 2.5, py: 0.4, opacity: 0.55 }}
              >
                <ListItemText
                  primary={gap.label}
                  secondary="not scopable — see below"
                  slotProps={{
                    primary: { sx: { fontSize: "0.88rem" } },
                    secondary: { sx: { fontSize: "0.65rem" } },
                  }}
                />
              </ListItemButton>
              <Typography
                variant="caption"
                sx={{ px: 2.5, pb: 0.5, color: "text.secondary", display: "block", fontSize: "0.65rem" }}
              >
                hz cannot select these by project.{" "}
                <Link to={gap.gatewayAt} style={{ color: "inherit" }} onClick={onNavigate}>
                  {gap.gatewayLabel}
                </Link>{" "}
                lists every one, gateway-wide.
              </Typography>
            </Box>
          </Tooltip>
        ))}
      </List>

      <Divider sx={{ my: 1, borderColor: "rgba(255,255,255,0.06)" }} />
      <Typography variant="caption" sx={ZONE_CAPTION}>
        subprojects
      </Typography>
      {children.length === 0 ? (
        <Typography
          variant="caption"
          sx={{ px: 2, pb: 1, color: "text.secondary", display: "block", fontSize: "0.68rem" }}
        >
          {route.name} has none. These screens show its own rows and nothing below, because there is
          nothing below.
        </Typography>
      ) : (
        <List dense disablePadding>
          {children.map((c) => (
            <ZoneLink
              key={c.name}
              to="/$project"
              params={{ project: projectParam(index, c) }}
              label={c.name}
              secondary={c.descendants.length > 0 ? `${c.descendants.length} below` : undefined}
              indent={1}
              onNavigate={onNavigate}
            />
          ))}
        </List>
      )}
    </Box>
  );
}

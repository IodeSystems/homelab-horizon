/**
 * THE SIDEBAR — ONE RECURSIVE MENU, WHOSE ROOT IS THE ESTATE.
 *
 * (plan/design/ui.md, Decision 1, amended a fourth time.) What shipped before
 * this was TWO ZONES: a project tree over fourteen fixed gateway entries, with
 * `Machines`, `Services` and `Domains` appearing in both halves — the same words
 * twice, two meanings, both on screen — plus six lines apologising for two
 * surfaces a project cannot scope. Twenty-four rows at the top level and thirty-
 * eight inside a project. It was rendered and rejected as cluttered.
 *
 * The shape now:
 *
 *   LEVEL 0 — the estate            LEVEL 1 — acme-co
 *     Overview                        ← Estate
 *     Services                        Overview
 *     Domains                         Services
 *     Machines                        Domains
 *     Network                         Machines
 *     ── projects ──                  Network
 *     acme-co              ▸          ── subprojects ──
 *     ── the gateway ──               intern            ▸
 *     Drift · DNS · Hosts · …         storefront        ▸
 *
 * Five scopable entries, the same five words in the same order at every level,
 * narrowing as you descend. One subtree block, ONE LEVEL deep by default, every
 * node with children expandable in place. The way up labelled with where it
 * goes. The ten gateway surfaces at level 0 and nowhere else.
 *
 * # THE TRADE, WRITTEN DOWN
 *
 * The previous amendment put the gateway entries at every depth so that "three
 * projects deep and needing Settings costs one click". This reverses it:
 * Settings from inside a project is two clicks (`← Estate`, then Settings) and
 * ten permanent rows leave the sidebar. Settings is rare; the clutter was
 * constant; `← Estate` is a labelled affordance in a fixed place.
 *
 * # WHERE EACH LINE COMES FROM, AND WHAT MAY BE STATE
 *
 * `readMenu` (`projectRoutes.ts`) decides the whole menu from the `$project`
 * parameter alone; this file draws what it decided. Which project you are in and
 * how deep you are are facts about the ADDRESS — a nav that morphs on component
 * state cannot be linked, and "open the redline nav" would mean two different
 * screens to two people.
 *
 * WHICH NODES ARE EXPANDED IS THE ONE EXCEPTION, AND IT IS DELIBERATE. A
 * disclosure triangle is not a location: the page does not change, the rows in
 * scope do not change, and nothing about what the reader is looking at is
 * different — they are peeking at what is under a sibling before deciding
 * whether to go there. It is `useState` here rather than a search param for two
 * reasons beyond that: the menu renders on EVERY route, so a search param would
 * have to be declared on the root route and preserved by every `<Link>` in the
 * app — one link that dropped it would silently collapse the tree — and it would
 * put one reader's fiddling into the URL they send somebody else. The default is
 * closed, which is a pure function of nothing at all, so a fresh load of any URL
 * renders exactly one level for everybody.
 *
 * It lives beside `ProjectBits.tsx` rather than in it because it renders in the
 * SHELL, on every screen, and must not depend on `useProjectContext` — that hook
 * reads `useParams({ from: "/$project" })` and throws anywhere else.
 */
import { useMemo, useState } from "react";
import {
  Box,
  IconButton,
  List,
  ListItemButton,
  ListItemText,
  Typography,
} from "@mui/material";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import { Link, useRouterState } from "@tanstack/react-router";
import { useProjects } from "../../api/hooks";
import {
  buildProjectIndex,
  readMenu,
  GATEWAY_NAV,
  type EstateNavTo,
  type MenuEntry,
  type MenuNode,
  type ProjectNavTo,
} from "./projectRoutes.ts";

/**
 * The `$project` parameter the ADDRESS carries, from anywhere in the app.
 *
 * `useProjectContext` cannot be used outside `/$project` — `useParams({from})`
 * throws there — and the sidebar renders on every screen, so it reads the match
 * list instead. THE ROUTE IS THE ONLY SOURCE. Nothing about which project the
 * menu is showing may come from `useState`: a nav that morphs on component
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

/** Which entries the current address is inside, as the router sees it. */
function useMatchedRouteIds(): string[] {
  return useRouterState({ select: (s) => s.matches.map((m) => m.routeId as string) });
}

const CAPTION = {
  textTransform: "uppercase" as const,
  letterSpacing: 0.6,
  fontSize: "0.65rem",
  color: "text.secondary",
  px: 2,
  pt: 1.5,
  pb: 0.5,
  display: "block",
};

const SELECTED = {
  "&.Mui-selected": {
    bgcolor: "rgba(233, 69, 96, 0.15)",
    "&:hover": { bgcolor: "rgba(233, 69, 96, 0.25)" },
  },
};

/** One row of the menu, always a real link so it can be middle-clicked. */
function MenuRow({
  to,
  param,
  label,
  title,
  indent = 0,
  selected = false,
  bold = false,
  trailing,
  onNavigate,
}: {
  to: EstateNavTo | ProjectNavTo | "/projects";
  param?: string;
  label: string;
  title?: string;
  indent?: number;
  selected?: boolean;
  bold?: boolean;
  /** The disclosure control, when this row has children to peek at. */
  trailing?: React.ReactNode;
  onNavigate?: () => void;
}) {
  return (
    <Box sx={{ display: "flex", alignItems: "center", pr: trailing ? 0.5 : 0 }}>
      <Link
        to={to as "/projects"}
        params={(param ? { project: param } : undefined) as never}
        search={{} as never}
        style={{ textDecoration: "none", color: "inherit", flex: 1, minWidth: 0 }}
        onClick={onNavigate}
        title={title}
      >
        <ListItemButton
          selected={selected}
          sx={{ borderRadius: 1, mx: 1, pl: 1 + indent * 1.5, py: 0.4, ...SELECTED }}
        >
          <ListItemText
            primary={label}
            slotProps={{ primary: { sx: { fontWeight: bold ? 700 : 400, fontSize: "0.88rem" } } }}
          />
        </ListItemButton>
      </Link>
      {trailing}
    </Box>
  );
}

/**
 * The subtree block: one level, and a control on every node that has more.
 *
 * The label is a link (going there is a navigation) and the triangle is a button
 * (peeking is not). Two jobs, two controls, both visible — a row that did both
 * on one click would make "look inside" and "go inside" the same gesture, and
 * the operator cannot undo the one they did not mean.
 */
function SubtreeBlock({
  nodes,
  open,
  toggle,
  onNavigate,
}: {
  nodes: MenuNode[];
  open: ReadonlySet<string>;
  toggle: (name: string) => void;
  onNavigate?: () => void;
}) {
  return (
    <List dense disablePadding>
      {nodes.map((n) => (
        <MenuRow
          key={n.route.name}
          to="/$project"
          param={n.param}
          label={n.route.name}
          indent={n.indent}
          onNavigate={onNavigate}
          trailing={
            n.hasChildren ? (
              <IconButton
                size="small"
                aria-label={n.toggleLabel}
                title={n.toggleLabel}
                aria-expanded={open.has(n.route.name)}
                onClick={() => toggle(n.route.name)}
                sx={{ color: "text.secondary" }}
              >
                {open.has(n.route.name) ? (
                  <ExpandMoreIcon fontSize="small" />
                ) : (
                  <ChevronRightIcon fontSize="small" />
                )}
              </IconButton>
            ) : undefined
          }
        />
      ))}
    </List>
  );
}

/**
 * The gateway block — LEVEL 0 ONLY.
 *
 * Ten links in a wrapped flow rather than ten rows, which is what makes level 0
 * fit on a screen with the five entries and the project tree. They are still
 * real links with real hrefs: middle-clickable, copyable, and readable out of
 * the markup.
 */
function GatewayBlock({
  currentPath,
  onNavigate,
}: {
  currentPath: string;
  onNavigate?: () => void;
}) {
  // ONE BLOCK, INLINE — not ten rows and not ten wrapper divs. Ten rows is what
  // made the old sidebar twenty-four lines long; the flow wraps to about three
  // lines in a 260px column and is one block in the markup, which is also what
  // lets the line-count check in the render selftest measure what a reader sees.
  return (
    <Box data-gateway-zone sx={{ px: 2, pb: 2, fontSize: "0.8rem", lineHeight: 1.9 }}>
      {GATEWAY_NAV.map((g, i) => (
        <span key={g.to}>
          {i > 0 ? <span style={{ color: "#5a6478" }}> · </span> : null}
          <Link
            to={g.to}
            title={g.why}
            onClick={onNavigate}
            style={{
              color: currentPath === g.to ? "#e94560" : "#b9c2d0",
              fontWeight: currentPath === g.to ? 700 : 400,
              whiteSpace: "nowrap",
            }}
          >
            {g.label}
          </Link>
        </span>
      ))}
    </Box>
  );
}

/**
 * The whole menu. One reading, one renderer, every level.
 */
export function SidebarMenu({ onNavigate }: { onNavigate?: () => void }) {
  const projects = useProjects();
  const param = useProjectParam();
  const matched = useMatchedRouteIds();
  const currentPath = useRouterState({ select: (s) => s.location.pathname });
  // The ONE piece of component state in the nav, and it is a disclosure control:
  // see the file header for why an expanded node is not a location.
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set<string>());
  const toggle = (name: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });

  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const menu = readMenu(index, param, open);

  const isSelected = (entry: MenuEntry) =>
    menu.kind === "project"
      ? matched.includes(entry.to === "/$project" ? "/$project/" : entry.to)
      : currentPath === entry.to;

  return (
    <Box data-menu-level={menu.level} data-menu-kind={menu.kind}>
      {/* THE WAY UP, LABELLED WITH ITS DESTINATION. Absent at level 0, because
          there is nothing above the estate. */}
      {menu.up ? (
        <List dense disablePadding>
          <MenuRow
            to={menu.up.toEstate ? "/projects" : "/$project"}
            param={menu.up.toEstate ? undefined : menu.up.param}
            label={menu.up.label}
            title={menu.up.meaning}
            bold
            onNavigate={onNavigate}
          />
        </List>
      ) : null}

      {/* THE SCOPE THE FIVE ENTRIES BELOW APPLY TO — "Estate", or this project's
          name. One line, not three: the rejected version printed "in this
          project", the name, and the dotted path, which is the name twice. The
          dotted path is on the page's own header, where it can be read and
          copied. */}
      <Typography
        variant="caption"
        sx={{ ...CAPTION, fontFamily: menu.here ? "monospace" : undefined }}
      >
        {menu.scopeLabel}
      </Typography>

      {menu.kind === "unresolved" ? (
        <Typography
          variant="caption"
          sx={{ px: 2, pb: 1, color: "warning.main", display: "block" }}
        >
          {menu.meaning} These five screens show the whole estate; pick a project below.
        </Typography>
      ) : null}

      <List dense disablePadding data-menu-entries>
        {menu.entries.map((entry) => (
          <MenuRow
            key={entry.label}
            to={entry.to}
            param={entry.param || undefined}
            label={entry.label}
            title={entry.blurb}
            selected={isSelected(entry)}
            onNavigate={onNavigate}
          />
        ))}
      </List>

      <Typography variant="caption" sx={CAPTION}>
        {menu.childCaption}
      </Typography>
      {projects.isLoading ? (
        <Typography variant="caption" sx={{ px: 2, color: "text.secondary", display: "block" }}>
          Asking hz which projects it declares…
        </Typography>
      ) : projects.error ? (
        <Typography variant="caption" sx={{ px: 2, color: "warning.main", display: "block" }}>
          hz could not be asked which projects it declares. This list is empty because the read
          failed, not because there are none.
        </Typography>
      ) : menu.nodes.length === 0 ? (
        <Typography variant="caption" sx={{ px: 2, color: "text.secondary", display: "block" }}>
          {menu.emptyNote}
        </Typography>
      ) : (
        <SubtreeBlock nodes={menu.nodes} open={open} toggle={toggle} onNavigate={onNavigate} />
      )}

      {/* THE GATEWAY BLOCK, AT LEVEL 0 AND NOWHERE ELSE. Not duplicated into a
          project, not greyed there, not explained there: a nav entry to a
          surface that does not exist at this scope is a door to a room that is
          not there, and `← Estate` above is the way to the rooms that do. */}
      {menu.showGateway ? (
        <>
          <Typography variant="caption" sx={CAPTION}>
            the gateway
          </Typography>
          <GatewayBlock currentPath={currentPath} onNavigate={onNavigate} />
        </>
      ) : null}
    </Box>
  );
}

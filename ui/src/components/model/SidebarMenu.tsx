/**
 * THE SIDEBAR — WHERE YOU ARE. The project tree, then the gateway group.
 *
 * (plan/design/ui.md, Decision 1, amendment 5.) The sidebar is the SAME at
 * every scope. What changes when you move is which tree node is highlighted
 * and which nodes are open; nothing appears, disappears or is renamed. The six
 * tabs over the page (`ScopeBar`) are WHAT you are looking at.
 *
 *   [+] projects
 *   ▾ acme-co
 *       intern
 *     ▸ storefront        ← highlighted when the address is /p/storefront/…
 *   ▸ redline
 *   gateway
 *   Drift
 *   DNS
 *   …
 *
 * The tree has NO named root. With no project selected, no node is
 * highlighted; the wordmark and the breadcrumb's clear control lead there.
 *
 * # A NODE KEEPS THE TAB
 *
 * On `/p/storefront/domains`, the `redline` node links to
 * `/p/redline/domains`. The tree changes WHERE; the tab is WHAT, and moving
 * sideways should not throw away what you were looking at. On a gateway
 * screen there is no tab, and a node links to the project's Overview.
 *
 * # WHAT MAY BE STATE
 *
 * The highlighted node and the default expansion — open along the path to it —
 * come from the ADDRESS, so two people opening one link see one sidebar. Which
 * nodes the reader has flipped away from that default is `useState`: a
 * disclosure triangle is not a location (Decision M), and the sidebar renders
 * on every route, so a search param would have to survive every `<Link>` in
 * the app.
 *
 * It renders in the SHELL, on every screen, so it must not depend on
 * `useProjectContext` — that hook reads `useParams({ from: "/p/$project" })`
 * and throws anywhere else. It reads the pathname instead.
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
import AddIcon from "@mui/icons-material/Add";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import DeleteOutlineIcon from "@mui/icons-material/DeleteOutlined";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import { Link, useRouterState } from "@tanstack/react-router";
import { useProjects } from "../../api/hooks";
import {
  buildProjectIndex,
  projectOfPath,
  SCOPE_TABS,
  tabOfPath,
  tabTarget,
  treeRows,
  GATEWAY_NAV,
  type ScopeTab,
  type TreeRow,
} from "./projectRoutes.ts";
import { AddProjectDialog, RemoveProjectDialog } from "./ProjectDialogs";

/** Where the address says you are: which project (or none), which tab (or none). */
export function useScopeLocation(): { pathname: string; project: string | null; tab: ScopeTab | null } {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  return { pathname, project: projectOfPath(pathname), tab: tabOfPath(pathname) };
}

const CAPTION = {
  textTransform: "uppercase" as const,
  letterSpacing: 0.6,
  fontSize: "0.65rem",
  color: "text.secondary",
  display: "block",
};

const SELECTED = {
  "&.Mui-selected": {
    bgcolor: "rgba(233, 69, 96, 0.15)",
    "&:hover": { bgcolor: "rgba(233, 69, 96, 0.25)" },
  },
};

/** A caption with an optional control at its right edge. */
function GroupHeading({ label, action }: { label: string; action?: React.ReactNode }) {
  return (
    <Box sx={{ display: "flex", alignItems: "center", px: 2, pt: 1.5, pb: 0.5, minHeight: 32 }}>
      <Typography variant="caption" sx={{ ...CAPTION, flex: 1 }}>
        {label}
      </Typography>
      {action}
    </Box>
  );
}

/**
 * One tree node. The label is a link (going there) and the triangle is a
 * button (peeking): two jobs, two controls, so neither can be done by accident.
 */
function TreeNode({
  row,
  tab,
  toggle,
  onRemove,
  onNavigate,
}: {
  row: TreeRow;
  tab: ScopeTab;
  toggle: (name: string) => void;
  onRemove: (name: string) => void;
  onNavigate?: () => void;
}) {
  const target = tabTarget(tab, row.route.name);
  return (
    <Box sx={{ display: "flex", alignItems: "center", pr: 0.5 }} data-tree-node={row.route.name}>
      <Box sx={{ width: 28, flexShrink: 0, pl: 1 + row.depth * 1.5, boxSizing: "content-box" }}>
        {row.hasChildren ? (
          <IconButton
            size="small"
            aria-label={row.toggleLabel}
            title={row.toggleLabel}
            aria-expanded={row.expanded}
            onClick={() => toggle(row.route.name)}
            sx={{ color: "text.secondary", p: 0.25 }}
          >
            {row.expanded ? <ExpandMoreIcon fontSize="small" /> : <ChevronRightIcon fontSize="small" />}
          </IconButton>
        ) : null}
      </Box>
      <Link
        to={target.to as "/p/$project"}
        params={target.params as never}
        search={{} as never}
        style={{ textDecoration: "none", color: "inherit", flex: 1, minWidth: 0 }}
        onClick={onNavigate}
        title={`${row.route.name} — ${tab.label}`}
      >
        <ListItemButton selected={row.current} sx={{ borderRadius: 1, py: 0.3, pl: 0.5, ...SELECTED }}>
          <ListItemText
            primary={row.route.name}
            slotProps={{
              primary: {
                sx: {
                  fontFamily: "monospace",
                  fontSize: "0.85rem",
                  fontWeight: row.current ? 700 : 400,
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  whiteSpace: "nowrap",
                },
              },
            }}
          />
        </ListItemButton>
      </Link>
      {/* Remove is offered on the node you are ON, and nowhere else: a
          destructive control on every row is one mis-click from the wrong
          project. The dialog is a dry run until confirmed. */}
      {row.current ? (
        <IconButton
          size="small"
          aria-label={`Remove ${row.route.name}…`}
          title={`Remove ${row.route.name}… (shows what it would take first)`}
          onClick={() => onRemove(row.route.name)}
          sx={{ color: "text.secondary", p: 0.25 }}
        >
          <DeleteOutlineIcon fontSize="small" />
        </IconButton>
      ) : null}
    </Box>
  );
}

/** The gateway group: one real row per surface, the same at every depth. */
function GatewayGroup({ pathname, onNavigate }: { pathname: string; onNavigate?: () => void }) {
  return (
    <List dense disablePadding data-gateway-zone>
      {GATEWAY_NAV.map((g) => {
        const selected = pathname === g.to || pathname.startsWith(`${g.to}/`);
        return (
          <Link
            key={g.to}
            to={g.to}
            title={g.why}
            onClick={onNavigate}
            style={{ textDecoration: "none", color: "inherit", display: "block" }}
          >
            <ListItemButton selected={selected} sx={{ borderRadius: 1, mx: 1, py: 0.3, ...SELECTED }}>
              <ListItemText
                primary={g.label}
                slotProps={{ primary: { sx: { fontSize: "0.85rem", fontWeight: selected ? 700 : 400 } } }}
              />
            </ListItemButton>
          </Link>
        );
      })}
    </List>
  );
}

export function SidebarMenu({ onNavigate }: { onNavigate?: () => void }) {
  const projects = useProjects();
  const { pathname, project, tab } = useScopeLocation();
  // Nodes flipped away from the default (open along the path to `project`).
  const [toggled, setToggled] = useState<ReadonlySet<string>>(() => new Set<string>());
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const toggle = (name: string) =>
    setToggled((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });

  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const rows = treeRows(index, project, toggled);
  // On a gateway screen there is no tab to keep, so a node goes to Overview.
  const nodeTab = tab ?? SCOPE_TABS[0]!;

  return (
    <Box data-sidebar>
      <GroupHeading
        label="projects"
        action={
          <IconButton
            size="small"
            aria-label="Add a project"
            title={project ? `Add a project (under ${project} by default)` : "Add a project"}
            onClick={() => setAdding(true)}
            sx={{ color: "text.secondary", p: 0.25 }}
          >
            <AddIcon fontSize="small" />
          </IconButton>
        }
      />
      {projects.isLoading ? (
        <Typography variant="caption" sx={{ px: 2, color: "text.secondary", display: "block" }}>
          Asking hz which projects it declares…
        </Typography>
      ) : projects.error ? (
        <Typography variant="caption" sx={{ px: 2, color: "warning.main", display: "block" }}>
          hz could not be asked which projects it declares. This list is empty because the read
          failed, not because there are none.
        </Typography>
      ) : rows.length === 0 ? (
        <Typography variant="caption" sx={{ px: 2, color: "text.secondary", display: "block" }}>
          hz declares no project yet. This is an empty tree, not a failed read.
        </Typography>
      ) : (
        <List dense disablePadding data-tree>
          {rows.map((row) => (
            <TreeNode
              key={row.route.name}
              row={row}
              tab={nodeTab}
              toggle={toggle}
              onRemove={setRemoving}
              onNavigate={onNavigate}
            />
          ))}
        </List>
      )}

      <GroupHeading label="gateway" />
      <GatewayGroup pathname={pathname} onNavigate={onNavigate} />
      <Box sx={{ pb: 2 }} />

      <AddProjectDialog
        open={adding}
        index={index}
        defaultParent={project ?? ""}
        onClose={() => setAdding(false)}
      />
      <RemoveProjectDialog name={removing} onClose={() => setRemoving(null)} />
    </Box>
  );
}

import { type ReactNode, useState } from "react";
import {
  Alert,
  Box,
  Drawer,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Avatar,
  Button,
  Divider,
  Menu,
  MenuItem,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme,
} from "@mui/material";
import MenuIcon from "@mui/icons-material/Menu";
import AccountCircleIcon from "@mui/icons-material/AccountCircle";
import PersonIcon from "@mui/icons-material/PersonOutlined";
import DashboardIcon from "@mui/icons-material/Dashboard";
import CompareArrowsIcon from "@mui/icons-material/CompareArrows";
import AccountTreeIcon from "@mui/icons-material/AccountTree";
import ComputerIcon from "@mui/icons-material/Computer";
import LanIcon from "@mui/icons-material/Lan";
import DnsIcon from "@mui/icons-material/Dns";
import StorageIcon from "@mui/icons-material/Storage";
import LanguageIcon from "@mui/icons-material/Language";
import VpnKeyIcon from "@mui/icons-material/VpnKey";
import BlockIcon from "@mui/icons-material/Block";
import MonitorHeartIcon from "@mui/icons-material/MonitorHeart";
import HubIcon from "@mui/icons-material/Hub";
import RouterIcon from "@mui/icons-material/Router";
import SettingsIcon from "@mui/icons-material/Settings";
import LogoutIcon from "@mui/icons-material/Logout";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useAuthStatus, useLogout } from "../api/auth";
import { SidebarProjectZone } from "./model/ProjectNavZone";

const SIDEBAR_WIDTH = 260;

/**
 * Every gateway route the sidebar may point at, spelled out — so a `<Link>`
 * built from this list is checked by tsc against the real route tree rather
 * than accepting any string.
 */
type GatewayPath =
  | "/dashboard"
  | "/drift"
  | "/projects"
  | "/machines"
  | "/hosts"
  | "/services"
  | "/domains"
  | "/dns"
  | "/vpn"
  | "/bans"
  | "/checks"
  | "/observability"
  | "/ports"
  | "/settings";

interface NavItem {
  icon: ReactNode;
  label: string;
  path: GatewayPath;
  disabled?: boolean;
}

const navItems: NavItem[] = [
  { icon: <DashboardIcon />, label: "Dashboard", path: "/dashboard" },
  // Sits next to Dashboard rather than under Config: the drift screen answers
  // "what would change on which box", which is a job, not a data type. The
  // navigation redesign (plan/design/ui.md) reshapes this list properly;
  // this is one entry in the existing shell, not that change.
  { icon: <CompareArrowsIcon />, label: "Drift", path: "/drift" },
  // The model's two read-only surfaces, beside Drift for the same reason. They
  // are NOT one entry each per data type: Projects is "what exists and what
  // does each rung declare", Machines is "what boxes are there and what runs
  // on them". Instances get no entry of their own — an instance is a config
  // address, not a thing with an identity, so a global instance list would
  // answer no question anyone asks. A machine's projection hangs off its
  // machine at /machines/$machine, which is where you are when you ask.
  { icon: <AccountTreeIcon />, label: "Projects", path: "/projects" },
  { icon: <ComputerIcon />, label: "Machines", path: "/machines" },
  // Beside Machines and deliberately NOT merged into it: a machine is a box hz
  // manages with an agent, a host is an address other records resolve through,
  // and the two lists do not have to match. Hosts answers one job — what points
  // at this box, and what breaks if I move it — which nothing rendered before.
  // The /observability host table is a third thing again (names and labels for
  // scrape targets), so it stays where it is.
  { icon: <LanIcon />, label: "Hosts", path: "/hosts" },
  { icon: <DnsIcon />, label: "Services", path: "/services" },
  { icon: <LanguageIcon />, label: "Domains", path: "/domains" },
  { icon: <StorageIcon />, label: "DNS", path: "/dns" },
  { icon: <VpnKeyIcon />, label: "VPN Clients", path: "/vpn" },
  { icon: <BlockIcon />, label: "IP Bans", path: "/bans" },
  { icon: <MonitorHeartIcon />, label: "Checks", path: "/checks" },
  { icon: <HubIcon />, label: "Observability", path: "/observability" },
  { icon: <RouterIcon />, label: "Ports", path: "/ports" },
  { icon: <SettingsIcon />, label: "Settings", path: "/settings" },
];

// WHAT IS NOT IN THE LIST, and why (plan/design/ui.md, Decision 1, amended twice):
//
//   Config. Everything on that surface is addressed as
//   `(project, environment, app, role)` and CMRegistrationResp carries
//   `Project`, so it lives at /$project/config now. A nav entry pointing at a
//   URL that redirects to Projects is a label that does not describe its
//   destination, which is a support ticket; /config is kept as a REDIRECT so
//   bookmarks land somewhere true, and that is all it is.
//
//   Anything project-scoped. Those live in the PROJECT ZONE above this list,
//   which is replaced when you enter a project. This list is not.
//
// Everything that IS here is here because its record carries no project: a
// machine is `{Name, Segments, Note}` and the gateway box hosts instances from
// several projects at once, so nesting Machines under one of them would leave
// the most important machine in the estate with no URL. /services is still the
// only screen an unassigned service can appear on, /domains the only place a
// gateway-wide domain collision is visible, /machines the only page gw-1 has.

/**
 * THE SIDEBAR HAS TWO ZONES AND ONLY THE FIRST ONE CHANGES.
 *
 * Zone 1 — the project tree, replaced on entry by the project's own nav and a
 * link back to its parent. Zone 2 — these fourteen gateway entries, identical
 * at every depth, in the same order and the same place on screen.
 *
 * Three projects deep and you need Settings: you click Settings. The two
 * alternatives both lose. Settings at the root only costs N clicks up and N
 * back down and makes a gateway-wide fact feel like one project's property;
 * Settings appearing only sometimes is a sidebar that morphs, which is the
 * thing this whole navigation is organised against.
 */
function SidebarContent({ onNavigate }: { onNavigate?: () => void }) {
  const routerState = useRouterState();
  const currentPath = routerState.location.pathname;

  return (
    <Box
      sx={{
        display: "flex",
        flexDirection: "column",
        minHeight: "100%",
        bgcolor: "#16213e",
      }}
    >
      <Box sx={{ p: 2, borderBottom: "1px solid rgba(255,255,255,0.06)" }}>
        <Link to="/dashboard" style={{ textDecoration: "none" }} onClick={onNavigate}>
          <Typography variant="h6" sx={{ fontWeight: 700, color: "#fff", cursor: "pointer" }}>
            Homelab Horizon
          </Typography>
        </Link>
      </Box>

      {/* ZONE 1 — where you are. A pure function of the URL; see
          readProjectZone. Nothing here is component state. */}
      <Box sx={{ pb: 1, borderBottom: "1px solid rgba(255,255,255,0.06)" }}>
        <SidebarProjectZone onNavigate={onNavigate} />
      </Box>

      {/* ZONE 2 — the gateway. Never changes, at any depth. */}
      <Typography
        variant="caption"
        sx={{
          textTransform: "uppercase",
          letterSpacing: 0.6,
          fontSize: "0.65rem",
          color: "text.secondary",
          px: 2,
          pt: 1.5,
          pb: 0.5,
          display: "block",
        }}
      >
        the gateway
      </Typography>
      <List data-gateway-zone sx={{ flex: 1, pt: 0 }}>
        {navItems.map((item) => {
          const isActive = currentPath === item.path;
          const button = (
            <ListItemButton
              key={item.path}
              disabled={item.disabled}
              selected={isActive}
              sx={{
                borderRadius: 1,
                mx: 1,
                mb: 0.25,
                py: 0.4,
                "&.Mui-selected": {
                  bgcolor: "rgba(233, 69, 96, 0.15)",
                  "&:hover": { bgcolor: "rgba(233, 69, 96, 0.25)" },
                },
              }}
            >
              <ListItemIcon sx={{ minWidth: 36, color: isActive ? "primary.main" : "text.secondary" }}>
                {item.icon}
              </ListItemIcon>
              <ListItemText primary={item.label} slotProps={{ primary: { sx: { fontSize: "0.88rem" } } }} />
            </ListItemButton>
          );

          if (item.disabled) {
            return (
              <Tooltip key={item.path} title="Coming soon" placement="right">
                <span>{button}</span>
              </Tooltip>
            );
          }
          // A real link, so the entry can be middle-clicked, copied and read
          // out of the markup — and so "Settings is one click from three deep"
          // is a claim a check can make against an href.
          return (
            <Link
              key={item.path}
              to={item.path}
              style={{ textDecoration: "none", color: "inherit" }}
              onClick={onNavigate}
            >
              {button}
            </Link>
          );
        })}
      </List>

      <List sx={{ borderTop: "1px solid rgba(255,255,255,0.06)" }}>
        <Link to="/account" style={{ textDecoration: "none", color: "inherit" }} onClick={onNavigate}>
          <ListItemButton
            selected={currentPath === "/account"}
            sx={{ borderRadius: 1, mx: 1, mb: 1 }}
          >
            <ListItemIcon sx={{ minWidth: 36, color: "text.secondary" }}>
              <PersonIcon />
            </ListItemIcon>
            <ListItemText primary="Account" />
          </ListItemButton>
        </Link>
      </List>
    </Box>
  );
}

// Who is signed in, top right.
//
// The sidebar had a Logout button and nothing else — no indication of *whose*
// session was being ended, which with a shared admin token, a VPN admin peer and
// real accounts all able to authenticate was the one thing worth showing.
function UserMenu() {
  const { data } = useAuthStatus();
  const logout = useLogout();
  const navigate = useNavigate();
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);

  if (!data?.authenticated) return null;

  // Each way in gets its own label: "admin token" is not a person, and showing
  // a name for it would be a lie.
  const name = data.username ?? (data.method === "vpn" ? "VPN admin peer" : "admin token");
  const isAccount = Boolean(data.username);

  return (
    <>
      <Button
        onClick={(e) => setAnchor(e.currentTarget)}
        startIcon={
          isAccount ? (
            <Avatar sx={{ width: 24, height: 24, fontSize: 13 }}>
              {name.charAt(0).toUpperCase()}
            </Avatar>
          ) : (
            <AccountCircleIcon />
          )
        }
        sx={{ textTransform: "none", color: "text.secondary" }}
      >
        {name}
      </Button>
      <Menu anchorEl={anchor} open={Boolean(anchor)} onClose={() => setAnchor(null)}>
        <MenuItem disabled sx={{ opacity: "1 !important" }}>
          <Box>
            <Typography variant="body2" sx={{ fontWeight: 600 }}>
              {name}
            </Typography>
            <Typography variant="caption" color="text.secondary">
              {isAccount ? data.role ?? "signed in" : "not tied to a person"}
            </Typography>
          </Box>
        </MenuItem>
        <Divider />
        <MenuItem
          onClick={() => {
            setAnchor(null);
            navigate({ to: "/account" });
          }}
        >
          <ListItemIcon>
            <PersonIcon fontSize="small" />
          </ListItemIcon>
          Account settings
        </MenuItem>
        <MenuItem
          onClick={() => {
            setAnchor(null);
            logout.mutate();
          }}
        >
          <ListItemIcon>
            <LogoutIcon fontSize="small" />
          </ListItemIcon>
          Sign out
        </MenuItem>
      </Menu>
    </>
  );
}

function ReadOnlyBanner() {
  const { data } = useAuthStatus();
  if (!data || data.configPrimary || !data.peerId) return null;
  const primary = data.primaryId || "the config primary";
  return (
    <Alert severity="warning" sx={{ mb: 2 }}>
      Read-only — this instance ({data.peerId}) is a non-primary fleet member.
      Edit configuration on <strong>{primary}</strong>.
    </Alert>
  );
}

export default function AppLayout({ children }: { children: ReactNode }) {
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down("md"));
  const [drawerOpen, setDrawerOpen] = useState(false);
  const navigate = useNavigate();

  return (
    <Box sx={{ display: "flex", minHeight: "100vh" }}>
      {isMobile ? (
        <>
          <Drawer
            open={drawerOpen}
            onClose={() => setDrawerOpen(false)}
            slotProps={{ paper: { sx: { width: SIDEBAR_WIDTH, bgcolor: "#16213e" } } }}
          >
            <SidebarContent onNavigate={() => setDrawerOpen(false)} />
          </Drawer>
        </>
      ) : (
        <Box
          component="nav"
          sx={{
            width: SIDEBAR_WIDTH,
            flexShrink: 0,
            borderRight: "1px solid rgba(255,255,255,0.06)",
          }}
        >
          <Box sx={{ position: "fixed", width: SIDEBAR_WIDTH, height: "100vh", overflow: "auto" }}>
            <SidebarContent />
          </Box>
        </Box>
      )}

      <Box
        component="main"
        sx={{
          flex: 1,
          p: 3,
          minWidth: 0,
        }}
      >
        <Box sx={{ display: "flex", justifyContent: "flex-end", mb: 1 }}>
          <UserMenu />
        </Box>
        {isMobile && (
          <Box sx={{ display: "flex", alignItems: "center", gap: 1, mb: 2 }}>
            {/* LABELLED, not an icon alone. Below `md` this button is the only
                way to the project tree and the gateway entries, so "Menu" is
                spelled out rather than left to a hamburger the operator has to
                recognise. */}
            <Button
              onClick={() => setDrawerOpen(true)}
              startIcon={<MenuIcon />}
              sx={{ textTransform: "none" }}
            >
              Menu
            </Button>
            <Typography
              variant="h6"
              onClick={() => navigate({ to: "/dashboard" })}
              sx={{ fontWeight: 700, color: "#fff", cursor: "pointer" }}
            >
              Homelab Horizon
            </Typography>
          </Box>
        )}
        <ReadOnlyBanner />
        {children}
      </Box>
    </Box>
  );
}

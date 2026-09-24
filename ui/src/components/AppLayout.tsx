import { type ReactNode, useState } from "react";
import {
  Alert,
  Box,
  Drawer,
  IconButton,
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
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useAuthStatus, useLogout } from "../api/auth";

const SIDEBAR_WIDTH = 240;

interface NavItem {
  icon: ReactNode;
  label: string;
  path: string;
  disabled?: boolean;
}

const navItems: NavItem[] = [
  { icon: <DashboardIcon />, label: "Dashboard", path: "/dashboard" },
  // Sits next to Dashboard rather than under Config: the drift screen answers
  // "what would change on which box", which is a job, not a data type. The
  // navigation redesign (plan/ui-redesign.md) reshapes this list properly;
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

// WHAT IS NOT IN THE LIST, and why (plan/ui-redesign.md, Decision 1 amended):
//
//   Config. Everything on that surface is addressed as
//   `(project, environment, app, role)` and CMRegistrationResp carries
//   `Project`, so it lives at /$project/config now. A nav entry pointing at a
//   URL that redirects to Projects is a label that does not describe its
//   destination, which is a support ticket; /config is kept as a REDIRECT so
//   bookmarks land somewhere true, and that is all it is.
//
//   A project switcher. The fifteen entries above are gateway-wide and do not
//   follow a project, so a switcher here would imply they do. It lives on the
//   project page, where the things it changes are.
//
// Everything that IS here is here because its record carries no project: a
// machine is `{Name, Segments, Note}` and the gateway box hosts instances from
// several projects at once, so nesting Machines under one of them would leave
// the most important machine in the estate with no URL.

function SidebarContent({ onNavigate }: { onNavigate?: () => void }) {
  const navigate = useNavigate();
  const routerState = useRouterState();
  const currentPath = routerState.location.pathname;

  const handleNav = (path: string) => {
    navigate({ to: path });
    onNavigate?.();
  };

  return (
    <Box
      sx={{
        display: "flex",
        flexDirection: "column",
        height: "100%",
        bgcolor: "#16213e",
      }}
    >
      <Box sx={{ p: 2, borderBottom: "1px solid rgba(255,255,255,0.06)" }}>
        <Typography
          variant="h6"
          onClick={() => handleNav("/dashboard")}
          sx={{ fontWeight: 700, color: "#fff", cursor: "pointer" }}
        >
          Homelab Horizon
        </Typography>
      </Box>

      <List sx={{ flex: 1, pt: 1 }}>
        {navItems.map((item) => {
          const isActive = currentPath === item.path;
          const button = (
            <ListItemButton
              key={item.path}
              disabled={item.disabled}
              selected={isActive}
              onClick={() => handleNav(item.path)}
              sx={{
                borderRadius: 1,
                mx: 1,
                mb: 0.5,
                "&.Mui-selected": {
                  bgcolor: "rgba(233, 69, 96, 0.15)",
                  "&:hover": { bgcolor: "rgba(233, 69, 96, 0.25)" },
                },
              }}
            >
              <ListItemIcon sx={{ minWidth: 40, color: isActive ? "primary.main" : "text.secondary" }}>
                {item.icon}
              </ListItemIcon>
              <ListItemText primary={item.label} />
            </ListItemButton>
          );

          if (item.disabled) {
            return (
              <Tooltip key={item.path} title="Coming soon" placement="right">
                <span>{button}</span>
              </Tooltip>
            );
          }
          return button;
        })}
      </List>

      <List sx={{ borderTop: "1px solid rgba(255,255,255,0.06)" }}>
        <ListItemButton
          onClick={() => handleNav("/account")}
          selected={currentPath === "/account"}
          sx={{ borderRadius: 1, mx: 1, mb: 1 }}
        >
          <ListItemIcon sx={{ minWidth: 40, color: "text.secondary" }}>
            <PersonIcon />
          </ListItemIcon>
          <ListItemText primary="Account" />
        </ListItemButton>
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
            <IconButton onClick={() => setDrawerOpen(true)} edge="start">
              <MenuIcon />
            </IconButton>
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

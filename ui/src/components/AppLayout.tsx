import { type ReactNode, useState } from "react";
import {
  Alert,
  Avatar,
  Box,
  Button,
  Divider,
  Drawer,
  ListItemIcon,
  Menu,
  MenuItem,
  Typography,
  useMediaQuery,
  useTheme,
} from "@mui/material";
import MenuIcon from "@mui/icons-material/Menu";
import AccountCircleIcon from "@mui/icons-material/AccountCircle";
import PersonIcon from "@mui/icons-material/PersonOutlined";
import LogoutIcon from "@mui/icons-material/Logout";
import { Link, useNavigate } from "@tanstack/react-router";
import { useAuthStatus, useLogout } from "../api/auth";
import { SidebarMenu } from "./model/SidebarMenu";
import { ScopeBar } from "./model/ScopeBar";

const SIDEBAR_WIDTH = 260;

/**
 * THE SHELL: wordmark, sidebar, who is signed in, the scope bar, the page.
 *
 * plan/design/ui.md, Decision 1, amendment 5. The sidebar (`SidebarMenu`) is
 * WHERE — the project tree and the gateway group, the same at every scope. The
 * scope bar (`ScopeBar`) is WHAT — a breadcrumb over the six tabs, drawn here
 * so the unscoped screens and the project screens carry the same bar. Account
 * is in the user menu at top right, and only there.
 */
function SidebarContent({ onNavigate }: { onNavigate?: () => void }) {
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
        <Link to="/" style={{ textDecoration: "none" }} onClick={onNavigate}>
          <Typography variant="h6" sx={{ fontWeight: 700, color: "#fff", cursor: "pointer" }}>
            Homelab Horizon
          </Typography>
        </Link>
      </Box>

      <SidebarMenu onNavigate={onNavigate} />
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
        // The name is the first thing to go on a narrow screen: the avatar is
        // still the control, and the name is in its title and in the menu it
        // opens. Without this the app bar wraps to two lines on a phone.
        title={name}
        aria-label={`Signed in as ${name}`}
        sx={{
          textTransform: "none",
          color: "text.secondary",
          minWidth: 0,
          flexShrink: 0,
          "& .MuiButton-startIcon": { mr: { xs: 0, sm: 1 } },
        }}
      >
        <Box
          component="span"
          sx={{
            display: { xs: "none", sm: "inline" },
            maxWidth: 220,
            overflow: "hidden",
            textOverflow: "ellipsis",
            whiteSpace: "nowrap",
          }}
        >
          {name}
        </Box>
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
        {/* THE APP BAR: ONE ROW, NEVER WRAPPED. It was two — the user menu on
            its own line, then Menu and the wordmark under it — and on a phone a
            long account name pushed it further. Now the wordmark gives way
            (ellipsis) and the user button drops to its avatar below `sm`. */}
        <Box sx={{ display: "flex", alignItems: "center", gap: 1, mb: 2, flexWrap: "nowrap", minWidth: 0 }}>
          {isMobile ? (
            <>
              {/* LABELLED, not an icon alone. Below `md` this button is the
                  only way to the project tree and the gateway entries, so
                  "Menu" is spelled out rather than left to a hamburger the
                  operator has to recognise. */}
              <Button
                onClick={() => setDrawerOpen(true)}
                startIcon={<MenuIcon />}
                sx={{ textTransform: "none", flexShrink: 0 }}
              >
                Menu
              </Button>
              <Typography
                variant="h6"
                noWrap
                onClick={() => navigate({ to: "/" })}
                sx={{ fontWeight: 700, color: "#fff", cursor: "pointer", minWidth: 0 }}
              >
                Homelab Horizon
              </Typography>
            </>
          ) : null}
          <Box sx={{ flex: 1 }} />
          <UserMenu />
        </Box>
        <ReadOnlyBanner />
        <ScopeBar />
        {children}
      </Box>
    </Box>
  );
}

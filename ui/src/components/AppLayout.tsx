import { type ReactNode, useState } from "react";
import {
  Box,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme,
} from "@mui/material";
import MenuIcon from "@mui/icons-material/Menu";
import DashboardIcon from "@mui/icons-material/Dashboard";
import DnsIcon from "@mui/icons-material/Dns";
import LanguageIcon from "@mui/icons-material/Language";
import VpnKeyIcon from "@mui/icons-material/VpnKey";
import BlockIcon from "@mui/icons-material/Block";
import MonitorHeartIcon from "@mui/icons-material/MonitorHeart";
import SettingsIcon from "@mui/icons-material/Settings";
import LogoutIcon from "@mui/icons-material/Logout";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useLogout } from "../api/auth";

const SIDEBAR_WIDTH = 240;

interface NavItem {
  icon: ReactNode;
  label: string;
  path: string;
  disabled?: boolean;
}

const navItems: NavItem[] = [
  { icon: <DashboardIcon />, label: "Dashboard", path: "/dashboard" },
  { icon: <DnsIcon />, label: "Services", path: "/services" },
  { icon: <LanguageIcon />, label: "Domains", path: "/domains" },
  { icon: <VpnKeyIcon />, label: "VPN Clients", path: "/vpn" },
  { icon: <BlockIcon />, label: "IP Bans", path: "/bans" },
  { icon: <MonitorHeartIcon />, label: "Checks", path: "/checks" },
  { icon: <SettingsIcon />, label: "Settings", path: "/settings" },
];

function SidebarContent({ onNavigate }: { onNavigate?: () => void }) {
  const navigate = useNavigate();
  const routerState = useRouterState();
  const currentPath = routerState.location.pathname;
  const logout = useLogout();

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
        <Typography variant="h6" sx={{ fontWeight: 700, color: "#fff" }}>
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
          onClick={() => logout.mutate()}
          sx={{ borderRadius: 1, mx: 1, mb: 1 }}
        >
          <ListItemIcon sx={{ minWidth: 40, color: "text.secondary" }}>
            <LogoutIcon />
          </ListItemIcon>
          <ListItemText primary="Logout" />
        </ListItemButton>
      </List>
    </Box>
  );
}

export default function AppLayout({ children }: { children: ReactNode }) {
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down("md"));
  const [drawerOpen, setDrawerOpen] = useState(false);

  return (
    <Box sx={{ display: "flex", minHeight: "100vh" }}>
      {isMobile ? (
        <>
          <IconButton
            onClick={() => setDrawerOpen(true)}
            sx={{
              position: "fixed",
              top: 12,
              left: 12,
              zIndex: 1300,
              bgcolor: "background.paper",
              "&:hover": { bgcolor: "secondary.main" },
            }}
          >
            <MenuIcon />
          </IconButton>
          <Drawer
            open={drawerOpen}
            onClose={() => setDrawerOpen(false)}
            PaperProps={{ sx: { width: SIDEBAR_WIDTH, bgcolor: "#16213e" } }}
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
          pt: isMobile ? 8 : 3,
          minWidth: 0,
        }}
      >
        {children}
      </Box>
    </Box>
  );
}

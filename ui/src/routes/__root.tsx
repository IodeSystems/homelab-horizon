import { createRootRoute, Outlet, useRouterState } from "@tanstack/react-router";
import { Box, CircularProgress } from "@mui/material";
import { useAuthStatus } from "../api/auth";
import AppLayout from "../components/AppLayout";
import LoginPage from "../components/LoginPage";
import SyncProvider from "../components/SyncProvider";

// Routes that must render without an admin session. The MFA portal is the
// whole point of the VPN jail: a jailed peer is redirected here to unlock its
// own access, and it is by definition not an admin. Gating it behind the admin
// login shows the one page such a peer needs as a login form it cannot pass.
//
// Safe to exempt because the page has no admin powers of its own — every
// endpoint it calls (/api/v1/mfa/{status,enroll,verify}) authenticates the
// caller by WireGuard source IP server-side, and refuses anyone who is not a
// known peer. It renders chrome-free for the same reason: a jailed peer can't
// reach anything the nav would link to.
const PUBLIC_ROUTES = ["/mfa"];

function RootComponent() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const isPublic = PUBLIC_ROUTES.includes(pathname);
  const { data, isLoading, isError } = useAuthStatus();

  if (isPublic) {
    return <Outlet />;
  }

  if (isLoading) {
    return (
      <Box
        sx={{
          minHeight: "100vh",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        <CircularProgress />
      </Box>
    );
  }

  if (isError || !data?.authenticated) {
    return <LoginPage />;
  }

  return (
    <SyncProvider>
      <AppLayout>
        <Outlet />
      </AppLayout>
    </SyncProvider>
  );
}

export const Route = createRootRoute({
  component: RootComponent,
});

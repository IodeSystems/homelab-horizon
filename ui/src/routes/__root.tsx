import { createRootRoute, Outlet } from "@tanstack/react-router";
import { Box, CircularProgress } from "@mui/material";
import { useAuthStatus } from "../api/auth";
import AppLayout from "../components/AppLayout";
import LoginPage from "../components/LoginPage";
import SyncProvider from "../components/SyncProvider";

function RootComponent() {
  const { data, isLoading, isError } = useAuthStatus();

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

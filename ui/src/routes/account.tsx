import { createFileRoute } from "@tanstack/react-router";
import { Alert, Box, Card, CardContent, Chip, Typography } from "@mui/material";
import { useAuthStatus } from "../api/auth";
import AccountSecurity from "../components/AccountSecurity";
import AccountTokens from "../components/AccountTokens";
import AccountPeers from "../components/AccountPeers";

// Everything about *you*, in one place.
//
// These cards previously lived under Settings > Users, mixed in with managing
// other people's accounts. They are a different job: an operator who wants to
// enrol a passkey or rotate their own token should not have to pass through a
// page about administering everyone else.
function AccountPage() {
  const { data } = useAuthStatus();

  // The shared admin token and a VPN admin peer authenticate but do not name a
  // person, so there is no account to show settings for. Say which one it is
  // rather than rendering empty cards.
  if (data && data.authenticated && !data.username) {
    return (
      <Box>
        <Typography variant="h5" sx={{ mb: 3, fontWeight: 600 }}>
          Account
        </Typography>
        <Alert severity="info">
          You are signed in with{" "}
          {data.method === "vpn" ? "an admin VPN peer" : "the shared admin token"},
          which is not tied to a person. Sign in with an account to manage a
          password, second factors, API tokens and devices.
        </Alert>
      </Box>
    );
  }

  return (
    <Box>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1.5, mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          Account
        </Typography>
        {data?.username && (
          <Chip size="small" label={data.username} />
        )}
        {data?.role && <Chip size="small" variant="outlined" label={data.role} />}
      </Box>

      <Card variant="outlined">
        <CardContent>
          <Typography variant="body2" color="text.secondary">
            Your sign-in, the credentials your scripts use, and the devices
            registered to you. Settings that apply to everyone — the account
            policy, other people's accounts — stay under Settings.
          </Typography>
        </CardContent>
      </Card>

      <AccountSecurity />
      <AccountTokens />
      <AccountPeers />
    </Box>
  );
}

export const Route = createFileRoute("/account")({
  component: AccountPage,
});

import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Alert,
  Typography,
  Stack,
} from "@mui/material";
import { useNavigate } from "@tanstack/react-router";
import {
  useAccountPeers,
  usePeerOwnership,
  type AccountPeer,
} from "../api/auth";

// The devices belonging to this account.
//
// The gateway still owns every peer — the VPN Clients page administers them all
// and is unchanged. This is a filter, so somebody can find their own laptop
// without reading a list of everybody's phones. Peers created before ownership
// existed are unowned and offered for claiming.
export default function AccountPeers() {
  const { data, isLoading, error } = useAccountPeers();
  const own = usePeerOwnership();
  const navigate = useNavigate();

  if (isLoading) return null;
  if (error) {
    return <Alert severity="error">Could not load devices: {error.message}</Alert>;
  }

  const mine = data?.peers ?? [];
  const unowned = data?.unowned ?? [];

  return (
    <Card variant="outlined" sx={{ mt: 3 }}>
      <CardContent>
        <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
          VPN devices
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          The peers registered to you. Ownership is for finding your own devices,
          not a permission — every account here administers the whole gateway.
        </Typography>

        {own.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {own.error.message}
          </Alert>
        )}

        {mine.length === 0 && (
          <Alert severity="info" sx={{ mb: 2 }}>
            No devices registered to you. Peers you add from the VPN Clients page
            are recorded as yours; older ones can be claimed below.
          </Alert>
        )}

        <Stack spacing={1} sx={{ mb: 2 }}>
          {mine.map((p) => (
            <PeerRow
              key={p.name}
              peer={p}
              action="release"
              busy={own.isPending}
              onAction={() => own.mutate({ name: p.name, action: "release" })}
            />
          ))}
        </Stack>

        <Button variant="outlined" onClick={() => navigate({ to: "/vpn" })}>
          Add or configure devices
        </Button>

        {unowned.length > 0 && (
          <Box sx={{ mt: 3 }}>
            <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 0.5 }}>
              Unclaimed devices ({unowned.length})
            </Typography>
            <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
              {/* Peers that predate ownership. Someone else's device is never
                  listed here — only genuinely unowned ones. */}
              These existed before devices were linked to accounts. Claim the
              ones that are yours.
            </Typography>
            <Stack spacing={1}>
              {unowned.map((p) => (
                <PeerRow
                  key={p.name}
                  peer={p}
                  action="claim"
                  busy={own.isPending}
                  onAction={() => own.mutate({ name: p.name, action: "claim" })}
                />
              ))}
            </Stack>
          </Box>
        )}
      </CardContent>
    </Card>
  );
}

function PeerRow({
  peer,
  action,
  busy,
  onAction,
}: {
  peer: AccountPeer;
  action: "claim" | "release";
  busy: boolean;
  onAction: () => void;
}) {
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        px: 2,
        py: 1,
      }}
    >
      <Box>
        <Typography variant="body2" sx={{ fontWeight: 500 }}>
          {peer.name}
          {peer.online && (
            <Chip size="small" color="success" variant="outlined" label="seen" sx={{ ml: 1 }} />
          )}
        </Typography>
        <Typography variant="caption" color="text.secondary" sx={{ fontFamily: "monospace" }}>
          {peer.address || peer.allowedIps}
          {peer.latestHandshake &&
            ` · last handshake ${new Date(peer.latestHandshake).toLocaleString()}`}
        </Typography>
      </Box>
      <Button size="small" disabled={busy} onClick={onAction}>
        {action === "claim" ? "This is mine" : "Not mine"}
      </Button>
    </Box>
  );
}

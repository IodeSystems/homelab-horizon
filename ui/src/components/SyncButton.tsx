import { Badge, Box, Button, CircularProgress, Tooltip } from "@mui/material";
import SyncIcon from "@mui/icons-material/Sync";
import { keyframes } from "@mui/system";
import { useSyncContext } from "./SyncProvider";
import { usePendingChanges } from "../api/hooks";

// Animated gradient that sweeps across the border ring when changes are pending.
const flow = keyframes`
  0% { background-position: 0% 50%; }
  100% { background-position: 200% 50%; }
`;

// SyncButton is the single Sync control shared by the Services and Domains
// pages. When the live config has diverged from the last sync it wraps the
// button in a flowing gradient ring and badges the change count, so either
// admin can see at a glance that a Sync is due.
export default function SyncButton() {
  const { startSync, isSyncing } = useSyncContext();
  const { data: pending } = usePendingChanges();

  const count = pending?.count ?? 0;
  const hasPending = !!pending?.hasPending && !isSyncing;

  const tip = hasPending
    ? `${count} unsynced change${count === 1 ? "" : "s"} since last sync — Sync to publish DNS & certs`
    : "Sync all services";

  const button = (
    <Button
      variant="outlined"
      startIcon={isSyncing ? <CircularProgress size={16} /> : <SyncIcon />}
      onClick={startSync}
      disabled={isSyncing}
      sx={
        hasPending
          ? {
              bgcolor: "background.paper",
              border: "none",
              "&:hover": { border: "none", bgcolor: "background.paper" },
            }
          : undefined
      }
    >
      {isSyncing ? "Syncing..." : "Sync"}
    </Button>
  );

  return (
    <Tooltip title={tip}>
      <Badge
        badgeContent={hasPending ? count : 0}
        color="warning"
        overlap="rectangular"
      >
        {hasPending ? (
          <Box
            sx={{
              borderRadius: 1,
              p: "2px",
              background:
                "linear-gradient(90deg,#f39c12,#e74c3c,#f1c40f,#f39c12)",
              backgroundSize: "200% 100%",
              animation: `${flow} 2.5s linear infinite`,
            }}
          >
            {button}
          </Box>
        ) : (
          button
        )}
      </Badge>
    </Tooltip>
  );
}

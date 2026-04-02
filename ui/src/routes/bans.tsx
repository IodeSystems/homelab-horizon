import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Paper,
  Snackbar,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import { Delete as DeleteIcon } from "@mui/icons-material";
import { useBanIP, useBans, useUnbanIP } from "../api/hooks";
import type { BanEntry } from "../api/types";

function relativeTime(epochSeconds: number): string {
  if (epochSeconds === 0) return "Never";
  const now = Date.now() / 1000;
  const diff = epochSeconds - now;
  const absDiff = Math.abs(diff);
  const isPast = diff < 0;

  if (absDiff < 60) return isPast ? "just now" : "in a few seconds";

  const minutes = Math.floor(absDiff / 60);
  if (minutes < 60) {
    const label = minutes === 1 ? "minute" : "minutes";
    return isPast ? `${minutes} ${label} ago` : `in ${minutes} ${label}`;
  }

  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    const label = hours === 1 ? "hour" : "hours";
    return isPast ? `${hours} ${label} ago` : `in ${hours} ${label}`;
  }

  const days = Math.floor(hours / 24);
  const label = days === 1 ? "day" : "days";
  return isPast ? `${days} ${label} ago` : `in ${days} ${label}`;
}

function isExpired(ban: BanEntry): boolean {
  if (!ban.expiresAt) return false;
  return ban.expiresAt < Date.now() / 1000;
}

function BansPage() {
  const { data, isLoading, error } = useBans();
  const banIP = useBanIP();
  const unbanIP = useUnbanIP();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [ip, setIp] = useState("");
  const [timeout, setTimeout] = useState("");
  const [reason, setReason] = useState("");

  const [snackbar, setSnackbar] = useState<{
    open: boolean;
    message: string;
    severity: "success" | "error";
  }>({ open: false, message: "", severity: "success" });

  const handleBan = () => {
    const input: { ip: string; timeout?: number; reason?: string } = { ip };
    if (timeout) input.timeout = Number(timeout);
    if (reason) input.reason = reason;

    banIP.mutate(input, {
      onSuccess: () => {
        setSnackbar({ open: true, message: `Banned ${ip}`, severity: "success" });
        setDialogOpen(false);
        setIp("");
        setTimeout("");
        setReason("");
      },
      onError: (err) => {
        setSnackbar({
          open: true,
          message: `Failed to ban: ${err instanceof Error ? err.message : "Unknown error"}`,
          severity: "error",
        });
      },
    });
  };

  const handleUnban = (banIp: string) => {
    unbanIP.mutate(banIp, {
      onSuccess: () => {
        setSnackbar({ open: true, message: `Unbanned ${banIp}`, severity: "success" });
      },
      onError: (err) => {
        setSnackbar({
          open: true,
          message: `Failed to unban: ${err instanceof Error ? err.message : "Unknown error"}`,
          severity: "error",
        });
      },
    });
  };

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", mt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return (
      <Alert severity="error" sx={{ mt: 2 }}>
        Failed to load bans: {error instanceof Error ? error.message : "Unknown error"}
      </Alert>
    );
  }

  const bans = data?.bans ?? [];

  return (
    <Box>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 700 }}>
          IP Bans
        </Typography>
        <Button
          variant="contained"
          color="error"
          onClick={() => setDialogOpen(true)}
        >
          Ban IP
        </Button>
      </Box>

      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>IP Address</TableCell>
              <TableCell>Reason</TableCell>
              <TableCell>Banned By</TableCell>
              <TableCell>Created</TableCell>
              <TableCell>Expires</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {bans.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} align="center" sx={{ py: 4 }}>
                  <Typography color="text.secondary">No active IP bans</Typography>
                </TableCell>
              </TableRow>
            ) : (
              bans.map((ban) => {
                const expired = isExpired(ban);
                return (
                  <TableRow
                    key={ban.ip}
                    sx={expired ? { opacity: 0.5 } : undefined}
                  >
                    <TableCell>
                      <Typography sx={{ fontFamily: "monospace", fontSize: "0.9rem" }}>
                        {ban.ip}
                      </Typography>
                    </TableCell>
                    <TableCell>{ban.reason || "\u2014"}</TableCell>
                    <TableCell>{ban.service || "admin"}</TableCell>
                    <TableCell>{relativeTime(ban.createdAt)}</TableCell>
                    <TableCell>
                      {!ban.expiresAt ? (
                        <Chip label="Never" size="small" color="error" variant="outlined" />
                      ) : expired ? (
                        <Chip label="Expired" size="small" color="default" variant="outlined" />
                      ) : (
                        relativeTime(ban.expiresAt)
                      )}
                    </TableCell>
                    <TableCell align="right">
                      <IconButton
                        size="small"
                        color="error"
                        onClick={() => handleUnban(ban.ip)}
                        disabled={unbanIP.isPending}
                      >
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </TableContainer>

      {/* Ban IP Dialog */}
      <Dialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Ban IP Address</DialogTitle>
        <DialogContent>
          <TextField
            autoFocus
            label="IP Address"
            fullWidth
            required
            value={ip}
            onChange={(e) => setIp(e.target.value)}
            sx={{ mt: 1, mb: 2 }}
          />
          <TextField
            label="Timeout (seconds)"
            type="number"
            fullWidth
            value={timeout}
            onChange={(e) => setTimeout(e.target.value)}
            helperText="Leave empty for permanent"
            sx={{ mb: 2 }}
          />
          <TextField
            label="Reason"
            fullWidth
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDialogOpen(false)}>Cancel</Button>
          <Button
            onClick={handleBan}
            variant="contained"
            color="error"
            disabled={!ip || banIP.isPending}
          >
            Ban
          </Button>
        </DialogActions>
      </Dialog>

      {/* Snackbar */}
      <Snackbar
        open={snackbar.open}
        autoHideDuration={4000}
        onClose={() => setSnackbar((s) => ({ ...s, open: false }))}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert
          severity={snackbar.severity}
          onClose={() => setSnackbar((s) => ({ ...s, open: false }))}
        >
          {snackbar.message}
        </Alert>
      </Snackbar>
    </Box>
  );
}

export const Route = createFileRoute("/bans")({
  component: BansPage,
});

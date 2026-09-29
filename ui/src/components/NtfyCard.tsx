import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  FormControlLabel,
  Paper,
  TextField,
  Typography,
} from "@mui/material";
import { useNtfySettings, useSaveNtfySettings } from "../api/hooks";

// hz's own ntfy target: where failed checks and DNS drift are posted.
//
// The access token is write-only. The server never sends it back — only
// hasNtfyToken — so the field always starts blank, a blank field on save
// keeps the stored token, and removing it is its own explicit control.

export default function NtfyCard() {
  const { data } = useNtfySettings();
  const save = useSaveNtfySettings();

  // Seeded from data when it is already cached, and kept in step by the
  // effect when it arrives later.
  const [url, setUrl] = useState(data?.url ?? "");
  const [token, setToken] = useState("");
  const [clearToken, setClearToken] = useState(false);

  useEffect(() => {
    if (data) setUrl(data.url ?? "");
  }, [data]);

  if (!data) return null;

  const onSave = () => {
    save.mutate(
      {
        url,
        // Empty means "keep the stored token", never "erase it".
        token: clearToken ? undefined : token || undefined,
        clearToken: clearToken || undefined,
      },
      {
        onSuccess: () => {
          setToken("");
          setClearToken(false);
        },
      },
    );
  };

  const dirty = url !== (data.url ?? "") || token !== "" || clearToken;

  return (
    <Paper sx={{ p: 3, mt: 2 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, mb: 1 }}>
        <Typography variant="h6" sx={{ fontWeight: 600 }}>
          Notifications (ntfy)
        </Typography>
        <Chip
          size="small"
          label={data.url ? "On" : "Off"}
          color={data.url ? "success" : "default"}
        />
        {data.hasNtfyToken && (
          <Chip size="small" variant="outlined" label="token stored" />
        )}
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        hz posts failed checks and DNS drift to this ntfy topic. An access
        token is optional: without one the topic is public and its name is the
        secret. With one, it is sent as <code>Authorization: Bearer</code>.
        Peer sync copies both to every instance in the fleet.
      </Typography>

      <Box sx={{ display: "flex", gap: 2, flexWrap: "wrap" }}>
        <TextField
          label="ntfy topic URL"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          size="small"
          sx={{ flex: "2 1 320px" }}
          placeholder="https://ntfy.sh/my-homelab-alerts"
          helperText="Empty turns notifications off"
        />
        <TextField
          label="Access token"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          size="small"
          type="password"
          autoComplete="new-password"
          disabled={clearToken}
          sx={{ flex: "1 1 240px" }}
          placeholder={data.hasNtfyToken ? "•••••• (unchanged)" : ""}
          helperText={
            data.hasNtfyToken
              ? "Leave blank to keep the stored token"
              : "Stored on the server, never shown again"
          }
        />
      </Box>

      {data.hasNtfyToken && (
        <FormControlLabel
          control={
            <Checkbox
              checked={clearToken}
              onChange={(e) => setClearToken(e.target.checked)}
            />
          }
          label="Remove the stored token on save"
        />
      )}

      <Box sx={{ mt: 1 }}>
        <Button
          variant="outlined"
          disabled={save.isPending || !dirty}
          onClick={onSave}
        >
          {save.isPending ? "Saving..." : "Save"}
        </Button>
      </Box>
      {save.error && (
        <Alert severity="error" sx={{ mt: 1 }}>
          {save.error instanceof Error ? save.error.message : "Could not save"}
        </Alert>
      )}
    </Paper>
  );
}

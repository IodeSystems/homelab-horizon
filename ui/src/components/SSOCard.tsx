import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Collapse,
  Divider,
  FormControlLabel,
  IconButton,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import KeyboardArrowDownIcon from "@mui/icons-material/KeyboardArrowDown";
import KeyboardArrowUpIcon from "@mui/icons-material/KeyboardArrowUp";
import {
  useOIDCSettings,
  useSaveOIDCSettings,
  useDiscoverOIDC,
} from "../api/hooks";

// Single sign-on, configurable without editing config.json on the server.
//
// The two fields that actually break installations are the issuer and the
// redirect URI, and they fail identically and opaquely at the provider. So the
// redirect URI is shown read-only and copyable (hz derives it from admin_url),
// and "Test discovery" turns a wrong issuer into a sentence instead of a
// failed sign-in an hour later.

const list = (s: string): string[] =>
  s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);

export default function SSOCard() {
  const { data } = useOIDCSettings();
  const save = useSaveOIDCSettings();
  const discover = useDiscoverOIDC();

  const [open, setOpen] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [issuer, setIssuer] = useState("");
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [name, setName] = useState("");
  const [emailDomains, setEmailDomains] = useState("");
  const [groupsClaim, setGroupsClaim] = useState("");
  const [allowedGroups, setAllowedGroups] = useState("");
  const [adminGroups, setAdminGroups] = useState("");
  const [requiredClaim, setRequiredClaim] = useState("");
  const [requiredValue, setRequiredValue] = useState("");
  const [autoProvision, setAutoProvision] = useState(false);

  useEffect(() => {
    if (!data) return;
    setEnabled(data.enabled);
    setIssuer(data.issuer ?? "");
    setClientId(data.clientId ?? "");
    setName(data.name ?? "");
    setEmailDomains((data.allowedEmailDomains ?? []).join(", "));
    setGroupsClaim(data.groupsClaim ?? "");
    setAllowedGroups((data.allowedGroups ?? []).join(", "));
    setAdminGroups((data.adminGroups ?? []).join(", "));
    setAutoProvision(data.autoProvision);
    const req = data.requiredClaims ?? {};
    const first = Object.keys(req)[0];
    setRequiredClaim(first ?? "");
    setRequiredValue(first ? (req[first] ?? []).join(", ") : "");
    if (data.enabled) setOpen(true);
  }, [data]);

  if (!data) return null;

  const onSave = () => {
    save.mutate({
      enabled,
      issuer,
      clientId,
      // Empty means "keep the stored secret": the server never sends it back,
      // so a blank field cannot mean "erase it".
      clientSecret: clientSecret || undefined,
      name: name || undefined,
      allowedEmailDomains: list(emailDomains),
      groupsClaim: groupsClaim || undefined,
      allowedGroups: list(allowedGroups),
      adminGroups: list(adminGroups),
      requiredClaims: requiredClaim
        ? { [requiredClaim]: list(requiredValue) }
        : undefined,
      autoProvision,
    });
    setClientSecret("");
  };

  return (
    <Card variant="outlined" sx={{ mt: 2 }}>
      <CardContent>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <IconButton size="small" onClick={() => setOpen(!open)}>
            {open ? (
              <KeyboardArrowUpIcon fontSize="small" />
            ) : (
              <KeyboardArrowDownIcon fontSize="small" />
            )}
          </IconButton>
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Single sign-on
          </Typography>
          <Chip
            size="small"
            label={enabled ? "On" : "Off"}
            color={enabled ? "success" : "default"}
          />
          {data.secretStored && (
            <Chip size="small" variant="outlined" label="secret stored" />
          )}
        </Box>
        <Typography variant="caption" color="text.secondary">
          Any OpenID Connect provider — Authentik, Keycloak, Authelia, Zitadel,
          Pocket ID. Local accounts and the admin token keep working alongside
          it.
        </Typography>

        {data.notReady && (
          <Alert severity="warning" sx={{ mt: 1 }}>
            {data.notReady}
          </Alert>
        )}

        <Collapse in={open} timeout="auto" unmountOnExit>
          <Stack spacing={2} sx={{ mt: 2 }}>
            <FormControlLabel
              control={
                <Switch
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                />
              }
              label="Offer single sign-on on the login page"
            />

            <Box sx={{ display: "flex", gap: 1, alignItems: "flex-start" }}>
              <TextField
                label="Issuer URL"
                value={issuer}
                onChange={(e) => setIssuer(e.target.value)}
                size="small"
                fullWidth
                placeholder="https://id.example.com"
                helperText="Everything else is read from its discovery document"
              />
              <Button
                onClick={() => discover.mutate({ issuer })}
                disabled={!issuer || discover.isPending}
                sx={{ mt: 0.5, whiteSpace: "nowrap" }}
              >
                Test discovery
              </Button>
            </Box>
            {discover.data && (
              <Alert severity={discover.data.ok ? "success" : "error"}>
                {discover.data.ok
                  ? `Provider answered. Authorization endpoint: ${discover.data.authorizationEndpoint}`
                  : discover.data.error}
              </Alert>
            )}

            <Box sx={{ display: "flex", gap: 2 }}>
              <TextField
                label="Client ID"
                value={clientId}
                onChange={(e) => setClientId(e.target.value)}
                size="small"
                fullWidth
              />
              <TextField
                label="Client secret"
                value={clientSecret}
                onChange={(e) => setClientSecret(e.target.value)}
                size="small"
                type="password"
                fullWidth
                placeholder={data.secretStored ? "•••••• (unchanged)" : ""}
                helperText={
                  data.secretStored
                    ? "Leave blank to keep the stored secret"
                    : "Stored on the server, never shown again"
                }
              />
            </Box>

            <TextField
              label="Redirect URI (give this to the provider)"
              value={data.redirectUri}
              size="small"
              fullWidth
              slotProps={{
                input: {
                  readOnly: true,
                  endAdornment: (
                    <Tooltip title="Copy">
                      <IconButton
                        size="small"
                        onClick={() =>
                          navigator.clipboard?.writeText(data.redirectUri)
                        }
                      >
                        <ContentCopyIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  ),
                },
              }}
              helperText="Derived from admin_url. Register it verbatim; a mismatch fails at the provider with no message here."
            />

            <TextField
              label="Button label"
              value={name}
              onChange={(e) => setName(e.target.value)}
              size="small"
              fullWidth
              placeholder="Sign in with Authentik"
            />

            <Divider textAlign="left">
              <Typography variant="caption" color="text.secondary">
                Who may sign in
              </Typography>
            </Divider>

            <TextField
              label="Allowed email domains"
              value={emailDomains}
              onChange={(e) => setEmailDomains(e.target.value)}
              size="small"
              fullWidth
              placeholder="example.com"
              helperText="Comma separated. An unverified email is refused outright. Empty means no domain gate."
            />

            <Box sx={{ display: "flex", gap: 2 }}>
              <TextField
                label="Required claim"
                value={requiredClaim}
                onChange={(e) => setRequiredClaim(e.target.value)}
                size="small"
                fullWidth
                placeholder="hd"
                helperText="Google Workspace: hd"
              />
              <TextField
                label="…must be one of"
                value={requiredValue}
                onChange={(e) => setRequiredValue(e.target.value)}
                size="small"
                fullWidth
                placeholder="example.com"
                helperText="Comma separated"
              />
            </Box>

            <Box sx={{ display: "flex", gap: 2 }}>
              <TextField
                label="Groups claim"
                value={groupsClaim}
                onChange={(e) => setGroupsClaim(e.target.value)}
                size="small"
                fullWidth
                placeholder="groups"
                helperText="Google Workspace sends none; use the domain gate instead"
              />
              <TextField
                label="Allowed groups"
                value={allowedGroups}
                onChange={(e) => setAllowedGroups(e.target.value)}
                size="small"
                fullWidth
              />
              <TextField
                label="Admin groups"
                value={adminGroups}
                onChange={(e) => setAdminGroups(e.target.value)}
                size="small"
                fullWidth
              />
            </Box>

            <FormControlLabel
              control={
                <Switch
                  checked={autoProvision}
                  onChange={(e) => setAutoProvision(e.target.checked)}
                />
              }
              label="Create an account on first sign-in"
            />
            {autoProvision && (
              <Alert severity="warning">
                hz has one privilege level. With this on, every account the
                provider admits — within the gates above — becomes an
                administrator of this gateway.
              </Alert>
            )}

            <Box sx={{ display: "flex", gap: 1 }}>
              <Button
                variant="contained"
                onClick={onSave}
                disabled={save.isPending}
              >
                Save
              </Button>
              {save.isSuccess && (
                <Typography variant="body2" color="success.main" sx={{ pt: 1 }}>
                  Saved.
                </Typography>
              )}
              {save.isError && (
                <Typography variant="body2" color="error.main" sx={{ pt: 1 }}>
                  {(save.error as Error).message}
                </Typography>
              )}
            </Box>
          </Stack>
        </Collapse>
      </CardContent>
    </Card>
  );
}

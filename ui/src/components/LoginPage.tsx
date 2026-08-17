import { useState, type FormEvent } from "react";
import {
  Box,
  TextField,
  Button,
  Typography,
  Paper,
  Alert,
  Link,
} from "@mui/material";
import {
  useLogin,
  useAuthStatus,
  useCreateUser,
  useLoginTOTP,
  useLoginPasskey,
  useOIDCStatus,
} from "../api/auth";

export default function LoginPage() {
  const status = useAuthStatus();
  const login = useLogin();
  const createUser = useCreateUser();

  const sso = useOIDCStatus();
  const loginTOTP = useLoginTOTP();
  const loginPasskey = useLoginPasskey();

  // Set when the password was right but the account has a second factor. The
  // form becomes a challenge rather than starting over.
  const [pending, setPending] = useState<{ id: string; factors: string[] } | null>(
    null,
  );
  const [code, setCode] = useState("");

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [token, setToken] = useState("");
  // The token is still a first-class way in, not a hidden fallback: it is what
  // a fresh install has, and what recovery documentation tells people to use.
  const [useToken, setUseToken] = useState(false);

  // A failed SSO round trip comes back as a redirect carrying the reason,
  // because the browser is mid-navigation and cannot be handed JSON.
  const ssoError = new URLSearchParams(window.location.search).get("sso_error");

  const needsBootstrap = status.data?.needsBootstrap === true;
  const usersAvailable = status.data?.usersAvailable !== false;
  const busy =
    login.isPending ||
    createUser.isPending ||
    loginTOTP.isPending ||
    loginPasskey.isPending;
  const error =
    login.error?.message ??
    createUser.error?.message ??
    loginTOTP.error?.message ??
    loginPasskey.error?.message;

  // With no identity store there is nothing but the token, so do not offer a
  // username form that cannot work.
  const tokenOnly = !usersAvailable || useToken;

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (tokenOnly) {
      if (token.trim()) login.mutate({ token: token.trim() });
      return;
    }
    if (needsBootstrap) {
      createUser.mutate({ username: username.trim(), password });
      return;
    }
    login.mutate(
      { username: username.trim(), password },
      {
        onSuccess: (res) => {
          if (res.mfaRequired && res.pendingId) {
            setPending({ id: res.pendingId, factors: res.factors ?? [] });
            setPassword("");
          }
        },
      },
    );
  };

  const submitLabel = () => {
    if (busy) return "Working...";
    if (tokenOnly) return "Sign in with token";
    return needsBootstrap ? "Create admin account" : "Sign in";
  };

  const canSubmit = tokenOnly
    ? token.trim().length > 0
    : username.trim().length > 0 && password.length > 0;

  return (
    <Box
      sx={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        bgcolor: "background.default",
      }}
    >
      <Paper sx={{ p: 4, maxWidth: 400, width: "100%", mx: 2 }}>
        <Typography variant="h5" sx={{ mb: 1, fontWeight: 600 }}>
          Homelab Horizon
        </Typography>

        {needsBootstrap && !tokenOnly && (
          <Alert severity="info" sx={{ mb: 2 }}>
            No accounts exist yet. The first one you create administers this
            gateway.
          </Alert>
        )}

        {!usersAvailable && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            The account store is unavailable, so only the admin token works.
            Check the hz log for the database error.
          </Alert>
        )}

        {(error || ssoError) && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error ?? ssoError}
          </Alert>
        )}

        {pending ? (
          <SecondFactorStep
            pending={pending}
            code={code}
            setCode={setCode}
            busy={busy}
            onTOTP={() =>
              loginTOTP.mutate({ pendingId: pending.id, code: code.trim() })
            }
            onPasskey={() => loginPasskey.mutate(pending.id)}
            onCancel={() => {
              // A pending id is single use and already spent on failure, so
              // going back has to mean starting the password step again.
              setPending(null);
              setCode("");
            }}
          />
        ) : (
        <form onSubmit={handleSubmit}>
          {tokenOnly ? (
            <TextField
              fullWidth
              type="password"
              label="Admin Token"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoFocus
              sx={{ mb: 2 }}
            />
          ) : (
            <>
              <TextField
                fullWidth
                label="Username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoFocus
                autoComplete="username"
                sx={{ mb: 2 }}
              />
              <TextField
                fullWidth
                type="password"
                label="Password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete={needsBootstrap ? "new-password" : "current-password"}
                helperText={needsBootstrap ? "At least 12 characters" : undefined}
                sx={{ mb: 2 }}
              />
            </>
          )}

          <Button
            fullWidth
            type="submit"
            variant="contained"
            disabled={busy || !canSubmit}
            size="large"
          >
            {submitLabel()}
          </Button>
        </form>
        )}

        {sso.data?.enabled && !pending && !tokenOnly && (
          <>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ display: "block", textAlign: "center", mt: 2, mb: 1 }}
            >
              or
            </Typography>
            <Button
              fullWidth
              variant="outlined"
              size="large"
              // A plain link, not a fetch: the provider needs to navigate the
              // browser, and an XHR cannot follow a cross-origin redirect into
              // a login page the person has to interact with.
              href="/api/v1/auth/oidc/start"
            >
              Sign in with {sso.data.name}
            </Button>
          </>
        )}

        {usersAvailable && !pending && (
          <Typography variant="body2" sx={{ mt: 2, textAlign: "center" }}>
            <Link
              component="button"
              type="button"
              underline="hover"
              onClick={() => setUseToken((v) => !v)}
            >
              {tokenOnly ? "Sign in with an account" : "Use the admin token"}
            </Link>
          </Typography>
        )}
      </Paper>
    </Box>
  );
}

function SecondFactorStep({
  pending,
  code,
  setCode,
  busy,
  onTOTP,
  onPasskey,
  onCancel,
}: {
  pending: { id: string; factors: string[] };
  code: string;
  setCode: (v: string) => void;
  busy: boolean;
  onTOTP: () => void;
  onPasskey: () => void;
  onCancel: () => void;
}) {
  const hasTOTP = pending.factors.includes("totp");
  const hasPasskey = pending.factors.includes("passkey");

  return (
    <Box>
      <Alert severity="info" sx={{ mb: 2 }}>
        One more step.
      </Alert>

      {hasPasskey && (
        <Button
          fullWidth
          variant={hasTOTP ? "outlined" : "contained"}
          size="large"
          disabled={busy}
          onClick={onPasskey}
          sx={{ mb: hasTOTP ? 2 : 1 }}
        >
          Use a passkey
        </Button>
      )}

      {hasTOTP && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (code.trim().length >= 6) onTOTP();
          }}
        >
          <TextField
            fullWidth
            label="Code from your authenticator app"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            autoFocus
            autoComplete="one-time-code"
            slotProps={{ htmlInput: { inputMode: "numeric", maxLength: 6 } }}
            sx={{ mb: 2 }}
          />
          <Button
            fullWidth
            type="submit"
            variant="contained"
            size="large"
            disabled={busy || code.trim().length < 6}
          >
            {busy ? "Checking..." : "Sign in"}
          </Button>
        </form>
      )}

      <Typography variant="body2" sx={{ mt: 2, textAlign: "center" }}>
        <Link component="button" type="button" underline="hover" onClick={onCancel}>
          Start over
        </Link>
      </Typography>
    </Box>
  );
}

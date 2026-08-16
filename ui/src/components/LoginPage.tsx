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
import { useLogin, useAuthStatus, useCreateUser } from "../api/auth";

export default function LoginPage() {
  const status = useAuthStatus();
  const login = useLogin();
  const createUser = useCreateUser();

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [token, setToken] = useState("");
  // The token is still a first-class way in, not a hidden fallback: it is what
  // a fresh install has, and what recovery documentation tells people to use.
  const [useToken, setUseToken] = useState(false);

  const needsBootstrap = status.data?.needsBootstrap === true;
  const usersAvailable = status.data?.usersAvailable !== false;
  const pending = login.isPending || createUser.isPending;
  const error = login.error?.message ?? createUser.error?.message;

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
    login.mutate({ username: username.trim(), password });
  };

  const submitLabel = () => {
    if (pending) return "Working...";
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

        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}

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
            disabled={pending || !canSubmit}
            size="large"
          >
            {submitLabel()}
          </Button>
        </form>

        {usersAvailable && (
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

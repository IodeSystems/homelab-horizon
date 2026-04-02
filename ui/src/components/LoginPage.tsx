import { useState, type FormEvent } from "react";
import { Box, TextField, Button, Typography, Paper, Alert } from "@mui/material";
import { useLogin } from "../api/auth";

export default function LoginPage() {
  const [token, setToken] = useState("");
  const login = useLogin();

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (token.trim()) {
      login.mutate(token.trim());
    }
  };

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
      <Paper
        sx={{
          p: 4,
          maxWidth: 400,
          width: "100%",
          mx: 2,
        }}
      >
        <Typography variant="h5" sx={{ mb: 3, fontWeight: 600 }}>
          Homelab Horizon
        </Typography>

        {login.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {login.error.message}
          </Alert>
        )}

        <form onSubmit={handleSubmit}>
          <TextField
            fullWidth
            type="password"
            label="Admin Token"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoFocus
            sx={{ mb: 2 }}
          />
          <Button
            fullWidth
            type="submit"
            variant="contained"
            disabled={login.isPending || !token.trim()}
            size="large"
          >
            {login.isPending ? "Authenticating..." : "Login"}
          </Button>
        </form>
      </Paper>
    </Box>
  );
}

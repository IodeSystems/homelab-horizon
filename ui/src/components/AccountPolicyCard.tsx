import { useState, useEffect } from "react";
import {
  Box,
  Button,
  Card,
  CardContent,
  Alert,
  TextField,
  Typography,
  Stack,
} from "@mui/material";
import { usePolicy, useSavePolicy, type AccountPolicy } from "../api/auth";

// The account rules an operator opts into.
//
// Each field says what turns it off and what the standard asks for, because
// these are the settings people change once, under pressure, with an auditor's
// question in front of them.
const FIELDS: {
  key: keyof AccountPolicy;
  label: string;
  help: string;
}[] = [
  {
    key: "idleMinutes",
    label: "Idle timeout (minutes)",
    help: "Signs an account out after this long without a request. 0 turns it off. PCI DSS 8.2.8 wants 15 or less.",
  },
  {
    key: "maxFailedAttempts",
    label: "Lock after failed attempts",
    help: "Consecutive failures before an account locks. -1 turns it off. PCI DSS 8.3.4 wants 10 or fewer.",
  },
  {
    key: "lockoutMinutes",
    label: "Lockout duration (minutes)",
    help: "How long the lock lasts. PCI DSS 8.3.4 wants at least 30.",
  },
  {
    key: "passwordMaxAgeDays",
    label: "Password expires after (days)",
    help: "Forces a change at the next sign-in. 0 turns it off. Accounts with a second factor are exempt, which is what PCI DSS 8.3.9 allows.",
  },
  {
    key: "passwordHistory",
    label: "Passwords remembered",
    help: "How many previous passwords cannot be reused. -1 turns it off. PCI DSS 8.3.7 wants 4.",
  },
];

export default function AccountPolicyCard() {
  const { data } = usePolicy();
  const save = useSavePolicy();
  const [form, setForm] = useState<AccountPolicy | null>(null);

  useEffect(() => {
    if (data) setForm(data);
  }, [data]);

  if (!form) return null;

  const dirty =
    data !== undefined &&
    FIELDS.some((f) => form[f.key] !== data[f.key]);

  return (
    <Card variant="outlined" sx={{ mt: 3 }}>
      <CardContent>
        <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
          Account policy
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Applies to every local account. Passwords must be at least{" "}
          {form.minPasswordLength ?? 12} characters.
        </Typography>

        {form.idleMinutes > 0 && (
          <Alert severity="info" sx={{ mb: 2 }}>
            An idle timeout signs you out too. Yours restarts on every request.
          </Alert>
        )}

        {save.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {save.error.message}
          </Alert>
        )}

        <Stack spacing={2}>
          {FIELDS.map((f) => (
            <TextField
              key={f.key}
              type="number"
              size="small"
              label={f.label}
              helperText={f.help}
              value={form[f.key] ?? 0}
              onChange={(e) =>
                setForm({ ...form, [f.key]: Number(e.target.value) })
              }
            />
          ))}
        </Stack>

        <Box sx={{ mt: 2, display: "flex", gap: 1 }}>
          <Button
            variant="contained"
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate(form)}
          >
            {save.isPending ? "Saving..." : "Save policy"}
          </Button>
          {dirty && (
            <Button onClick={() => data && setForm(data)}>Reset</Button>
          )}
        </Box>
      </CardContent>
    </Card>
  );
}

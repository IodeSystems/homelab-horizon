import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import QRCode from "qrcode";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  CircularProgress,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  TextField,
  Typography,
} from "@mui/material";
import { useMFAStatus, useMFAEnroll, useMFAVerify } from "../api/hooks";

function MFAPage() {
  const status = useMFAStatus();
  const enroll = useMFAEnroll();
  const verify = useMFAVerify();
  const [code, setCode] = useState("");
  const [duration, setDuration] = useState("");
  const [error, setError] = useState("");

  // Rendered in the browser from the provisioning URI. This used to be an
  // <img> pointed at api.qrserver.com, which handed the TOTP shared secret to
  // a third party in a query string — and could never load anyway, since a
  // jailed peer has no route off this box. That is the entire point of the
  // jail.
  const [qrSvg, setQrSvg] = useState("");
  const provisioningUri = enroll.data?.provisioningUri;
  useEffect(() => {
    if (!provisioningUri) {
      setQrSvg("");
      return;
    }
    let live = true;
    QRCode.toString(provisioningUri, { type: "svg", margin: 1, width: 200 })
      .then((svg) => live && setQrSvg(svg))
      .catch(() => live && setQrSvg(""));
    return () => {
      live = false;
    };
  }, [provisioningUri]);

  if (status.isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (status.isError) {
    return (
      <Box sx={{ maxWidth: 480, mx: "auto", mt: 8 }}>
        <Alert severity="info">
          MFA is not available. You may not be connected via VPN, or MFA is not
          enabled.
        </Alert>
      </Box>
    );
  }

  const data = status.data;
  const durations = data?.durations ?? ["2h", "4h", "8h", "forever"];

  // Already has an active session
  if (data?.sessionActive) {
    return (
      <Box sx={{ maxWidth: 480, mx: "auto", mt: 8 }}>
        <Card>
          <CardContent>
            <Typography variant="h5" gutterBottom>
              VPN MFA
            </Typography>
            <Alert severity="success" sx={{ mb: 2 }}>
              Your MFA session is active.
              {data.sessionExpiry &&
                ` Expires: ${new Date(data.sessionExpiry).toLocaleString()}`}
            </Alert>
            <Typography variant="body2" color="text.secondary">
              You have full VPN access according to your routing profile.
            </Typography>
          </CardContent>
        </Card>
      </Box>
    );
  }

  // Not enrolled — show enrollment.
  //
  // `enroll.data ||` is load-bearing. hz persists the secret the moment
  // /enroll is called, so the next poll of /status flips `enrolled` to true
  // while the user is still mid-scan. Keying only off `enrolled` therefore
  // swapped the QR out from under them for the bare verify form, stranding
  // anyone who hadn't finished scanning: the secret is only ever shown once,
  // and recovering needs an admin reset. Hold this view until a code is
  // actually confirmed — the session check above is what ends it.
  if (enroll.data || !data?.enrolled) {
    return (
      <Box sx={{ maxWidth: 480, mx: "auto", mt: 8 }}>
        <Card>
          <CardContent>
            <Typography variant="h5" gutterBottom>
              VPN MFA Setup
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Your VPN access requires multi-factor authentication. Set up your
              authenticator app to continue.
            </Typography>

            {enroll.data ? (
              <>
                <Alert severity="info" sx={{ mb: 2 }}>
                  Scan the QR code below with your authenticator app (Google
                  Authenticator, Authy, etc.), then enter the code to confirm.
                </Alert>
                <Box
                  sx={{
                    textAlign: "center",
                    mb: 2,
                    // The generated SVG is monochrome-on-transparent, which
                    // disappears against a dark card — and a QR needs a quiet
                    // light margin to scan reliably.
                    "& svg": {
                      width: 200,
                      height: 200,
                      background: "#fff",
                      borderRadius: 1,
                      padding: 1,
                    },
                  }}
                  dangerouslySetInnerHTML={{ __html: qrSvg }}
                />
                <Typography
                  variant="caption"
                  sx={{ fontFamily: "monospace", display: "block", mb: 2, textAlign: "center", wordBreak: "break-all" }}
                >
                  Secret: {enroll.data.secret}
                </Typography>
                <TextField
                  label="Enter code from authenticator"
                  value={code}
                  onChange={(e) => {
                    setCode(e.target.value);
                    setError("");
                  }}
                  fullWidth
                  sx={{ mb: 2 }}
                  slotProps={{ htmlInput: { inputMode: "numeric", pattern: "[0-9]*", maxLength: 6 } }}
                />
                <FormControl fullWidth sx={{ mb: 2 }}>
                  <InputLabel>Session Duration</InputLabel>
                  <Select
                    value={duration || durations[0]}
                    label="Session Duration"
                    onChange={(e) => setDuration(e.target.value)}
                  >
                    {durations.map((d: string) => (
                      <MenuItem key={d} value={d}>
                        {d === "forever" ? "Permanent" : d}
                      </MenuItem>
                    ))}
                  </Select>
                </FormControl>
                {error && (
                  <Alert severity="error" sx={{ mb: 2 }}>
                    {error}
                  </Alert>
                )}
                <Button
                  variant="contained"
                  fullWidth
                  disabled={!code || verify.isPending}
                  onClick={() => {
                    verify.mutate(
                      { code, duration: duration || durations[0] || "4h" },
                      {
                        onSuccess: () => {
                          setCode("");
                          status.refetch();
                        },
                        onError: (err) =>
                          setError(
                            err instanceof Error ? err.message : "Invalid code",
                          ),
                      },
                    );
                  }}
                >
                  {verify.isPending ? "Verifying..." : "Verify & Activate"}
                </Button>
              </>
            ) : (
              <>
                {enroll.isError && (
                  <Alert severity="error" sx={{ mb: 2 }}>
                    {enroll.error instanceof Error
                      ? enroll.error.message
                      : "Failed to start enrollment"}
                  </Alert>
                )}
                <Button
                  variant="contained"
                  fullWidth
                  disabled={enroll.isPending}
                  onClick={() => enroll.mutate()}
                >
                  {enroll.isPending ? "Setting up..." : "Set Up Authenticator"}
                </Button>
              </>
            )}
          </CardContent>
        </Card>
      </Box>
    );
  }

  // Enrolled but no active session — verify
  return (
    <Box sx={{ maxWidth: 480, mx: "auto", mt: 8 }}>
      <Card>
        <CardContent>
          <Typography variant="h5" gutterBottom>
            VPN MFA
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            Enter your authenticator code to unlock VPN access.
          </Typography>
          <TextField
            label="Authenticator code"
            value={code}
            onChange={(e) => {
              setCode(e.target.value);
              setError("");
            }}
            fullWidth
            sx={{ mb: 2 }}
            autoFocus
            slotProps={{ htmlInput: { inputMode: "numeric", pattern: "[0-9]*", maxLength: 6 } }}
          />
          <FormControl fullWidth sx={{ mb: 2 }}>
            <InputLabel>Session Duration</InputLabel>
            <Select
              value={duration || durations[0]}
              label="Session Duration"
              onChange={(e) => setDuration(e.target.value)}
            >
              {durations.map((d: string) => (
                <MenuItem key={d} value={d}>
                  {d === "forever" ? "Permanent" : d}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          {error && (
            <Alert severity="error" sx={{ mb: 2 }}>
              {error}
            </Alert>
          )}
          <Button
            variant="contained"
            fullWidth
            disabled={!code || verify.isPending}
            onClick={() => {
              verify.mutate(
                { code, duration: duration || durations[0] || "4h" },
                {
                  onSuccess: () => {
                    setCode("");
                    status.refetch();
                  },
                  onError: (err) =>
                    setError(
                      err instanceof Error ? err.message : "Invalid code",
                    ),
                },
              );
            }}
          >
            {verify.isPending ? "Verifying..." : "Unlock VPN Access"}
          </Button>
        </CardContent>
      </Card>
    </Box>
  );
}

export const Route = createFileRoute("/mfa")({
  component: MFAPage,
});

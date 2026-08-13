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
  Divider,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  TextField,
  Typography,
} from "@mui/material";
import {
  useMFAStatus,
  useMFAEnroll,
  useMFAVerify,
  usePasskeyRegister,
  usePasskeyAssert,
} from "../api/hooks";

function MFAPage() {
  const status = useMFAStatus();
  const enroll = useMFAEnroll();
  const verify = useMFAVerify();
  const passkeyRegister = usePasskeyRegister();
  const passkeyAssert = usePasskeyAssert();
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

  const passkeys = data?.passkeys ?? [];
  const hasFactor = Boolean(data?.enrolled) || passkeys.length > 0;

  // Not enrolled in anything — show setup.
  //
  // `enroll.data ||` is load-bearing. hz persists the secret the moment
  // /enroll is called, so the next poll of /status flips `enrolled` to true
  // while the user is still mid-scan. Keying only off `enrolled` therefore
  // swapped the QR out from under them for the bare verify form, stranding
  // anyone who hadn't finished scanning: the secret is only ever shown once,
  // and recovering needs an admin reset. Hold this view until a code is
  // actually confirmed — the session check above is what ends it.
  if (enroll.data || !hasFactor) {
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
                <PasskeySetup
                  register={passkeyRegister}
                  available={data?.passkeysAvailable ?? false}
                  unavailableReason={data?.passkeysUnavailableReason}
                  fullTunnel={data?.fullTunnel ?? false}
                />
              </>
            )}
          </CardContent>
        </Card>
      </Box>
    );
  }

  // Enrolled but no active session — verify
  // Enrolled in at least one factor, no active session — unlock.
  //
  // A peer may hold either factor or both, so each half renders independently.
  // The duration select and the error line are shared: which factor was used
  // says nothing about how long the session should last, and hiding the
  // selector from a passkey-only peer would silently pick a duration for them.
  const totpEnrolled = Boolean(data?.enrolled);
  const chosenDuration = duration || durations[0] || "4h";

  return (
    <Box sx={{ maxWidth: 480, mx: "auto", mt: 8 }}>
      <Card>
        <CardContent>
          <Typography variant="h5" gutterBottom>
            VPN MFA
          </Typography>

          <FormControl fullWidth sx={{ mb: 2 }}>
            <InputLabel>Session Duration</InputLabel>
            <Select
              value={chosenDuration}
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

          {passkeys.length > 0 && (
            <Button
              variant="contained"
              fullWidth
              disabled={passkeyAssert.isPending}
              onClick={() => {
                setError("");
                passkeyAssert.mutate(
                  { duration: chosenDuration },
                  {
                    onSuccess: () => status.refetch(),
                    onError: (err) =>
                      setError(
                        err instanceof Error
                          ? err.message
                          : "Passkey authentication failed",
                      ),
                  },
                );
              }}
            >
              {passkeyAssert.isPending
                ? "Waiting for passkey..."
                : "Unlock with Passkey"}
            </Button>
          )}

          {passkeys.length > 0 && totpEnrolled && (
            <Divider sx={{ my: 3 }}>
              <Typography variant="caption" color="text.secondary">
                or use a code
              </Typography>
            </Divider>
          )}

          {totpEnrolled && (
            <>
              <TextField
                label="Authenticator code"
                value={code}
                onChange={(e) => {
                  setCode(e.target.value);
                  setError("");
                }}
                fullWidth
                sx={{ mb: 2 }}
                autoFocus={passkeys.length === 0}
                slotProps={{
                  htmlInput: {
                    inputMode: "numeric",
                    pattern: "[0-9]*",
                    maxLength: 6,
                  },
                }}
              />
              <Button
                variant={passkeys.length > 0 ? "outlined" : "contained"}
                fullWidth
                disabled={!code || verify.isPending}
                onClick={() => {
                  verify.mutate(
                    { code, duration: chosenDuration },
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
            </>
          )}

          {error && (
            <Alert severity="error" sx={{ mt: 2 }}>
              {error}
            </Alert>
          )}
        </CardContent>
      </Card>
    </Box>
  );
}

// PasskeySetup offers passkey enrollment alongside the TOTP option. It states
// why it is unavailable rather than hiding, because the reason is a
// deployment fix (an https kiosk_url) that the operator seeing this page may
// well be the one able to make.
function PasskeySetup({
  register,
  available,
  unavailableReason,
  fullTunnel,
}: {
  register: ReturnType<typeof usePasskeyRegister>;
  available: boolean;
  unavailableReason?: string;
  fullTunnel: boolean;
}) {
  const [label, setLabel] = useState("");
  const [err, setErr] = useState("");

  if (!available) {
    return (
      <Alert severity="info" sx={{ mt: 2 }}>
        Passkeys unavailable{unavailableReason ? `: ${unavailableReason}` : ""}.
      </Alert>
    );
  }

  return (
    <Box sx={{ mt: 3 }}>
      <Divider sx={{ mb: 2 }}>
        <Typography variant="caption" color="text.secondary">
          or
        </Typography>
      </Divider>
      {/* Shown before the button, not after a failure: enrolling a passkey
          this peer can never use is a dead end that needs an admin reset. */}
      {fullTunnel && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          <strong>Full-tunnel peer — use a passkey on this device only.</strong>
          <br />
          Scanning a QR code with your phone <strong>will not work</strong>.
          That flow needs the internet to reach a relay service, and while
          you&apos;re locked out this device has no internet — only this
          portal. Use a passkey built into this device (Touch ID, Windows
          Hello) or a USB security key. If you only have a phone,{" "}
          <strong>set up an authenticator code instead</strong>.
        </Alert>
      )}
      <TextField
        label="Device name (optional)"
        placeholder="work laptop"
        value={label}
        onChange={(e) => setLabel(e.target.value)}
        fullWidth
        sx={{ mb: 2 }}
      />
      <Button
        variant="outlined"
        fullWidth
        disabled={register.isPending}
        onClick={() => {
          setErr("");
          register.mutate(
            { label: label.trim() },
            {
              onError: (e) =>
                setErr(
                  e instanceof Error ? e.message : "Passkey setup failed",
                ),
            },
          );
        }}
      >
        {register.isPending ? "Waiting for passkey..." : "Add a Passkey"}
      </Button>
      {err && (
        <Alert severity="error" sx={{ mt: 2 }}>
          {err}
        </Alert>
      )}
    </Box>
  );
}

export const Route = createFileRoute("/mfa")({
  component: MFAPage,
});

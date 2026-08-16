import { useState, useEffect } from "react";
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  Typography,
  Stack,
} from "@mui/material";
import QRCode from "qrcode";
import {
  useAccountFactors,
  useTOTPEnroll,
  useTOTPConfirm,
  useRemoveFactor,
  useAccountPasskeyRegister,
} from "../api/auth";

// Second factors for the signed-in account.
//
// Distinct from the VPN MFA page, which enrols peers for the captive portal.
// Same primitives, different thing being protected: this gates the admin UI.
export default function AccountSecurity() {
  const { data, isLoading, error } = useAccountFactors();
  const remove = useRemoveFactor();
  const registerPasskey = useAccountPasskeyRegister();
  const [totpOpen, setTotpOpen] = useState(false);

  if (isLoading || !data) return null;
  if (error) {
    return <Alert severity="error">Could not load factors: {error.message}</Alert>;
  }

  const factors = data.factors ?? [];
  const hasTOTP = factors.some((f) => f.kind === "totp");

  return (
    <Card variant="outlined" sx={{ mt: 3 }}>
      <CardContent>
        <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
          Your sign-in security
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          With a second factor enrolled, your password alone no longer signs you
          in.
        </Typography>

        {factors.length === 0 && (
          <Alert severity="info" sx={{ mb: 2 }}>
            No second factor. Your password is the only thing protecting this
            gateway's admin interface.
          </Alert>
        )}

        {factors.some((f) => f.cloneWarning) && (
          <Alert severity="error" sx={{ mb: 2 }}>
            A passkey's signature counter went backwards, which can mean the
            authenticator was copied. Remove it and enrol a new one.
          </Alert>
        )}

        <Stack spacing={1} sx={{ mb: 2 }}>
          {factors.map((f) => (
            <Box
              key={f.id}
              sx={{
                display: "flex",
                alignItems: "center",
                justifyContent: "space-between",
                border: 1,
                borderColor: "divider",
                borderRadius: 1,
                px: 2,
                py: 1,
              }}
            >
              <Box>
                <Typography variant="body2" sx={{ fontWeight: 500 }}>
                  {f.kind === "totp" ? "Authenticator app" : f.label || "Passkey"}
                  {f.cloneWarning && (
                    <Chip
                      size="small"
                      color="error"
                      label="clone warning"
                      sx={{ ml: 1 }}
                    />
                  )}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {f.lastUsed
                    ? `last used ${new Date(f.lastUsed).toLocaleString()}`
                    : "never used"}
                </Typography>
              </Box>
              <Button
                size="small"
                color="warning"
                disabled={remove.isPending}
                onClick={() => remove.mutate(f.id)}
              >
                Remove
              </Button>
            </Box>
          ))}
        </Stack>

        {remove.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {remove.error.message}
          </Alert>
        )}
        {registerPasskey.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {registerPasskey.error.message}
          </Alert>
        )}

        <Stack direction="row" spacing={1}>
          <Button
            variant="outlined"
            disabled={hasTOTP}
            onClick={() => setTotpOpen(true)}
          >
            {hasTOTP ? "Authenticator app added" : "Add authenticator app"}
          </Button>
          <Button
            variant="outlined"
            disabled={!data.passkeysAvailable || registerPasskey.isPending}
            onClick={() => registerPasskey.mutate("passkey")}
          >
            {registerPasskey.isPending ? "Waiting for device..." : "Add passkey"}
          </Button>
        </Stack>

        {!data.passkeysAvailable && (
          <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: "block" }}>
            Passkeys unavailable: {data.passkeysUnavailableReason}
          </Typography>
        )}
      </CardContent>

      <TOTPDialog open={totpOpen} onClose={() => setTotpOpen(false)} />
    </Card>
  );
}

function TOTPDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const enroll = useTOTPEnroll();
  const confirm = useTOTPConfirm();
  const [code, setCode] = useState("");
  const [qr, setQr] = useState("");

  const uri = enroll.data?.provisioningUri;
  const secret = enroll.data?.secret;

  // Ask for a secret as the dialog opens, so the QR is on screen rather than
  // behind another click.
  useEffect(() => {
    if (open && !enroll.data && !enroll.isPending) enroll.mutate();
  }, [open, enroll]);

  // The QR is rendered locally. Sending a provisioning URI to a third-party
  // generator would hand the shared secret to whoever runs it.
  useEffect(() => {
    if (!uri) {
      setQr("");
      return;
    }
    let live = true;
    QRCode.toString(uri, { type: "svg", margin: 1, width: 200 })
      .then((svg) => live && setQr(svg))
      .catch(() => live && setQr(""));
    return () => {
      live = false;
    };
  }, [uri]);

  const close = () => {
    setCode("");
    enroll.reset();
    confirm.reset();
    onClose();
  };

  return (
    <Dialog open={open} onClose={close} maxWidth="xs" fullWidth>
      <DialogTitle>Add authenticator app</DialogTitle>
      <DialogContent>
        {enroll.error && <Alert severity="error">{enroll.error.message}</Alert>}
        {confirm.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {confirm.error.message}
          </Alert>
        )}

        {qr && (
          <Box
            sx={{ display: "flex", justifyContent: "center", my: 2 }}
            dangerouslySetInnerHTML={{ __html: qr }}
          />
        )}
        {secret && (
          <Typography
            variant="caption"
            sx={{ display: "block", textAlign: "center", mb: 2, wordBreak: "break-all" }}
            color="text.secondary"
          >
            Or enter this secret by hand: <code>{secret}</code>
          </Typography>
        )}

        <Alert severity="info" sx={{ mb: 2 }}>
          Nothing is saved until a code from the app is accepted, so a scan that
          did not work cannot lock you out.
        </Alert>

        <TextField
          fullWidth
          label="Code from the app"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          slotProps={{ htmlInput: { inputMode: "numeric", maxLength: 6 } }}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={close}>Cancel</Button>
        <Button
          variant="contained"
          disabled={confirm.isPending || code.trim().length < 6}
          onClick={() =>
            confirm.mutate(code.trim(), { onSuccess: () => close() })
          }
        >
          {confirm.isPending ? "Checking..." : "Confirm"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

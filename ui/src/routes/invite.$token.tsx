import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import QRCode from "qrcode";
import { Alert, Box, Button, Card, CardContent, CircularProgress, TextField, Typography } from "@mui/material";
import { apiFetch } from "../api/client";
import type { InviteCheckResp, InviteRedeemResp } from "../api/generated-types";
import { cleanDeviceName, fillConfig, generateWGKeyPair, redeemRequest, x25519Unavailable } from "../components/invite/keygen";

// The VPN invite page (N4i). Reached from /invite/<token> on the kiosk vhost,
// BEFORE the person is on the VPN, so it renders without a session (__root's
// PUBLIC_ROUTES). The WireGuard keypair is made HERE, in the browser; hz gets
// the public key only (components/invite/keygen.ts).

export type InviteState =
  | { kind: "checking" }
  | { kind: "invalid" }
  | { kind: "unsupported"; why: string }
  | { kind: "ready"; check: InviteCheckResp }
  | { kind: "working" }
  | { kind: "failed"; error: string; check?: InviteCheckResp }
  | { kind: "done"; name: string; address: string; config: string; qrSvg: string };

export function InviteView({
  state,
  deviceName,
  onDeviceName,
  onRedeem,
}: {
  state: InviteState;
  deviceName: string;
  onDeviceName: (s: string) => void;
  onRedeem: () => void;
}) {
  const body = (() => {
    switch (state.kind) {
      case "checking":
      case "working":
        return (
          <Box sx={{ display: "flex", gap: 2, alignItems: "center" }}>
            <CircularProgress size={24} />
            <Typography>{state.kind === "checking" ? "Checking the invite…" : "Generating your key on this device…"}</Typography>
          </Box>
        );
      case "invalid":
        return (
          <Alert severity="error">
            This invite link is not valid: it was already used, it expired, or it was revoked. Ask for a new one.
          </Alert>
        );
      case "unsupported":
        return <Alert severity="error">{state.why}</Alert>;
      case "ready":
      case "failed": {
        const check = state.check;
        return (
          <>
            {state.kind === "failed" && (
              <Alert severity="error" sx={{ mb: 2 }}>
                {state.error}
              </Alert>
            )}
            {check && (
              <>
                <Typography variant="body2" sx={{ mb: 2 }}>
                  This link makes one VPN client ({check.profile}). Your device generates its own key; the private
                  half never leaves this page. The link works once
                  {check.expiresAt ? `, until ${new Date(check.expiresAt).toLocaleString()}` : ""}.
                </Typography>
                {check.name ? (
                  <Typography sx={{ mb: 2 }}>
                    Device: <strong>{check.name}</strong>
                  </Typography>
                ) : (
                  <TextField
                    fullWidth
                    label="Device name (e.g. carl-phone)"
                    value={deviceName}
                    onChange={(e) => onDeviceName(e.target.value)}
                    sx={{ mb: 2 }}
                  />
                )}
                <Button
                  variant="contained"
                  onClick={onRedeem}
                  disabled={!check.name && cleanDeviceName(deviceName) === ""}
                >
                  Generate my VPN config
                </Button>
              </>
            )}
          </>
        );
      }
      case "done":
        return (
          <>
            <Alert severity="warning" sx={{ mb: 2 }}>
              Save this now. It holds this device's private key, which exists only on this page: hz never had it, and
              cannot show it again.
            </Alert>
            <Typography sx={{ mb: 1 }}>
              <strong>{state.name}</strong> — VPN address {state.address}
            </Typography>
            {state.qrSvg ? (
              <Box
                sx={{ bgcolor: "#fff", p: 1, display: "inline-block", borderRadius: 1, mb: 2 }}
                data-testid="invite-qr"
                dangerouslySetInnerHTML={{ __html: state.qrSvg }}
              />
            ) : null}
            <Box component="pre" sx={{ p: 1, bgcolor: "action.hover", overflowX: "auto", fontSize: 13, mb: 2 }}>
              {state.config}
            </Box>
            <Button
              variant="contained"
              onClick={() => {
                const blob = new Blob([state.config], { type: "application/octet-stream" });
                const a = document.createElement("a");
                a.href = URL.createObjectURL(blob);
                a.download = `${state.name}.conf`;
                a.click();
                URL.revokeObjectURL(a.href);
              }}
            >
              Download {state.name}.conf
            </Button>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>
              Import it into the WireGuard app (or scan the QR code), connect, then open the kiosk to pass MFA.
            </Typography>
          </>
        );
    }
  })();
  return (
    <Box sx={{ maxWidth: 560, mx: "auto", mt: 6, px: 2 }}>
      <Card>
        <CardContent>
          <Typography variant="h5" gutterBottom>
            VPN invite
          </Typography>
          {body}
        </CardContent>
      </Card>
    </Box>
  );
}

function InvitePage() {
  const { token } = Route.useParams();
  const [state, setState] = useState<InviteState>({ kind: "checking" });
  const [deviceName, setDeviceName] = useState("");

  useEffect(() => {
    let live = true;
    (async () => {
      const why = await x25519Unavailable();
      if (why) return live && setState({ kind: "unsupported", why });
      try {
        const check = await apiFetch<InviteCheckResp>("/vpn/invite/check", {
          method: "POST",
          body: JSON.stringify({ token }),
        });
        if (live) setState(check.valid ? { kind: "ready", check } : { kind: "invalid" });
      } catch (e) {
        if (live) setState({ kind: "failed", error: `Could not check the invite: ${(e as Error).message}` });
      }
    })();
    return () => {
      live = false;
    };
  }, [token]);

  const redeem = async () => {
    const check = state.kind === "ready" || state.kind === "failed" ? state.check : undefined;
    setState({ kind: "working" });
    try {
      const pair = await generateWGKeyPair();
      const resp = await apiFetch<InviteRedeemResp>("/vpn/invite/redeem", {
        method: "POST",
        body: JSON.stringify(redeemRequest(token, pair, deviceName)),
      });
      const config = fillConfig(resp.config, pair.privateKey);
      const qrSvg = await QRCode.toString(config, { type: "svg", margin: 1, width: 240 }).catch(() => "");
      setState({ kind: "done", name: resp.name, address: resp.address, config, qrSvg });
    } catch (e) {
      setState({ kind: "failed", error: (e as Error).message, check });
    }
  };

  return <InviteView state={state} deviceName={deviceName} onDeviceName={setDeviceName} onRedeem={redeem} />;
}

export const Route = createFileRoute("/invite/$token")({
  component: InvitePage,
});

import { useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import BlockIcon from "@mui/icons-material/BlockOutlined";
import ContentCopyIcon from "@mui/icons-material/ContentCopyOutlined";
import FingerprintIcon from "@mui/icons-material/FingerprintOutlined";
import PendingIcon from "@mui/icons-material/PendingActionsOutlined";
import { useCMDeny, useCMRegistrations } from "../api/hooks";
import type { CMRegistrationResp } from "../api/generated-types";

// The config manager's approval queue.
//
// This page holds no key and does no crypto, and that is not an omission to be
// fixed later. hz serves this bundle; a browser served by hz cannot defend
// against hz, so every mitigation written here would run in the attacker's own
// code. The wrap step lives in the `hz` CLI — a locally installed binary,
// outside hz's control at the moment of use. What is left for this page is to
// put the operator in front of the CLI with the right id, and to say what the
// ceremony is actually checking. Do not add a key input.
//
// No value appears here either. Every config value is sealed and hz holds no
// key, so there is nothing to show but names, addresses, states and lineage.

function relativeTime(isoStr: string | undefined): string {
  if (!isoStr) return "never";
  const d = new Date(isoStr);
  if (isNaN(d.getTime()) || d.getTime() === 0) return "never";
  const seconds = Math.floor((Date.now() - d.getTime()) / 1000);
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function address(reg: CMRegistrationResp): string {
  return `${reg.environment}/${reg.app}/${reg.role}`;
}

// A command to run somewhere else. Matches RemoteVantages' CopyBox: a
// one-liner that has to be retyped is not a helper.
function CopyBox({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "flex-start",
        gap: 1,
        p: 1,
        bgcolor: "action.hover",
        borderRadius: 1,
      }}
    >
      <Typography
        component="code"
        sx={{
          flex: 1,
          fontFamily: "monospace",
          fontSize: "0.72rem",
          whiteSpace: "pre-wrap",
          wordBreak: "break-all",
          lineHeight: 1.6,
        }}
      >
        {text}
      </Typography>
      <Tooltip title={copied ? "Copied" : "Copy"}>
        <IconButton
          size="small"
          onClick={() => {
            void navigator.clipboard?.writeText(text);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          <ContentCopyIcon fontSize="small" />
        </IconButton>
      </Tooltip>
    </Box>
  );
}

// The fingerprint is the only defence against hz handing the approver a public
// key whose private half hz holds. The check cannot happen here — it happens in
// the CLI, against what the BOX printed at key generation — so this block's job
// is to name that and to make the value on this page visibly not the reference
// value. Showing it unlabelled would invite exactly the transcription the CLI
// refuses to enable by declining to print its own copy before the prompt.
function FingerprintBlock({ fingerprint }: { fingerprint: string }) {
  return (
    <Box sx={{ mt: 1 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 0.5 }}>
        <FingerprintIcon fontSize="small" color="action" />
        <Typography variant="caption" sx={{ fontWeight: 600 }}>
          Fingerprint, as hz reports it
        </Typography>
      </Box>
      {fingerprint ? (
        <Typography
          component="code"
          sx={{
            display: "block",
            fontFamily: "monospace",
            fontSize: "0.78rem",
            letterSpacing: "0.02em",
            wordBreak: "break-all",
            mt: 0.25,
          }}
        >
          {fingerprint}
        </Typography>
      ) : (
        <Typography variant="body2" color="error.main" sx={{ mt: 0.25 }}>
          hz cannot read this machine's public key, so it has no fingerprint to
          report. Do not approve it.
        </Typography>
      )}
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.5 }}>
        Not the value to compare against. <code>hz cm approve</code> asks you to
        type the fingerprint the <strong>box</strong> printed when it generated
        its key — read it off that console. If hz substituted its own public key,
        it substituted this line too, so matching the CLI against this page
        proves nothing.
      </Typography>
    </Box>
  );
}

function PendingRow({
  reg,
  onDeny,
}: {
  reg: CMRegistrationResp;
  onDeny: () => void;
}) {
  // Machine names are agent-supplied, globally unique and first-come: anything
  // that can reach the registration endpoint can claim a name before the real
  // box boots, and an operator seeing an expected name in an expected
  // environment approves it. The environment the box enrolled with is not
  // authoritative and nothing resolves against it — it is carried purely so a
  // disagreement shows up here, which is one of the few signals against a
  // squat. So it is an alert, not a muted field.
  const squat =
    !!reg.enrolledEnvironment && reg.enrolledEnvironment !== reg.environment;

  return (
    <Box sx={{ py: 1.5, borderTop: 1, borderColor: "divider" }}>
      <Box
        sx={{
          display: "flex",
          alignItems: "flex-start",
          justifyContent: "space-between",
          gap: 2,
        }}
      >
        <Box sx={{ minWidth: 0 }}>
          <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
            <Typography variant="body2" sx={{ fontWeight: 600 }}>
              {reg.machineName || "(unnamed)"}
            </Typography>
            <Chip
              size="small"
              variant="outlined"
              label={address(reg)}
              sx={{ height: 20, fontSize: "0.7rem", fontFamily: "monospace" }}
            />
            <Chip
              size="small"
              variant="outlined"
              label={`v${reg.version}`}
              sx={{ height: 20, fontSize: "0.7rem" }}
            />
            {squat && (
              <Chip
                size="small"
                color="warning"
                label="environment mismatch"
                sx={{ height: 20, fontSize: "0.7rem" }}
              />
            )}
          </Box>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ display: "block", mt: 0.25, fontFamily: "monospace", wordBreak: "break-all" }}
          >
            {reg.machineId} · {reg.id}
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
            first seen {relativeTime(reg.createdAt)}
            {reg.lastSeenAt ? ` · last asked ${relativeTime(reg.lastSeenAt)}` : ""}
          </Typography>
        </Box>
        <Button
          size="small"
          color="error"
          variant="outlined"
          startIcon={<BlockIcon />}
          onClick={onDeny}
          sx={{ flexShrink: 0 }}
        >
          Deny
        </Button>
      </Box>

      {squat && (
        <Alert severity="warning" sx={{ mt: 1.5 }}>
          <AlertTitle sx={{ fontSize: "0.85rem" }}>
            Enrolled as <code>{reg.enrolledEnvironment}</code>, asking for{" "}
            <code>{reg.environment}</code>
          </AlertTitle>
          <Typography variant="body2">
            The box claimed one environment when it first enrolled and is now
            asking for a key to a different one. That is what a name squat looks
            like: a machine name is first-come, so anything that can reach the
            registration endpoint can claim <code>{reg.machineName}</code> before
            the real box boots. Confirm with the box itself, on its own console,
            before approving. The fingerprint below is the only thing that tells
            the two apart.
          </Typography>
        </Alert>
      )}

      <FingerprintBlock fingerprint={reg.fingerprint} />

      <Typography variant="caption" sx={{ display: "block", mt: 1.5, mb: 0.5, fontWeight: 600 }}>
        Approve it from a terminal
      </Typography>
      <CopyBox text={`hz cm approve ${reg.id}`} />
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.5 }}>
        The command wraps this environment's key to the box's public key on your
        machine; the key itself never reaches hz. It will refuse to send anything
        unless the fingerprint you type matches the key hz served.
      </Typography>
    </Box>
  );
}

export function CMApprovals() {
  const { data, isLoading, error } = useCMRegistrations("pending");
  const deny = useCMDeny();

  const [denying, setDenying] = useState<CMRegistrationResp | null>(null);
  const [reason, setReason] = useState("");
  const [denyError, setDenyError] = useState("");

  if (isLoading) return null;
  if (error) {
    return (
      <Alert severity="error" sx={{ mb: 3 }}>
        Could not load the approval queue: {error.message}
      </Alert>
    );
  }

  const pending = data ?? [];

  function openDeny(reg: CMRegistrationResp) {
    setDenying(reg);
    setReason("");
    setDenyError("");
  }

  function submitDeny() {
    if (!denying) return;
    setDenyError("");
    deny.mutate(
      { id: denying.id, body: { reason: reason.trim() } },
      {
        onSuccess: () => setDenying(null),
        onError: (err) => setDenyError(err.message),
      },
    );
  }

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      <CardContent>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <PendingIcon fontSize="small" color="action" />
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Approval queue
          </Typography>
          {pending.length > 0 && (
            <Chip size="small" color="primary" label={pending.length} sx={{ height: 20 }} />
          )}
        </Box>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: 680 }}>
          Boxes that have enrolled and are waiting for an environment key.
          Approving one happens in the <code>hz</code> CLI on your own machine,
          not here: the wrap needs the environment key, and a page hz serves is
          the wrong place to hold one. This queue exists to tell you which
          registration to approve and what the CLI is going to ask you to check.
        </Typography>

        {pending.length > 0 && (
          <Alert severity="warning" sx={{ mt: 2 }}>
            <AlertTitle sx={{ fontWeight: 600 }}>
              Nothing here has been vouched for
            </AlertTitle>
            A registration is a <strong>request</strong>, not a credential. Anything
            that can reach hz can enqueue one — including an unprivileged local
            process, which may claim any environment it likes, <code>prod</code>{" "}
            included. The queue shows what was <em>claimed</em>.
            <br />
            <br />
            You are the check. Approving is what grants the key, and the CLI will
            ask you to type the fingerprint the box itself printed — compare it
            against the box, not against the value shown here, which hz would
            also control if hz were the thing lying to you.
          </Alert>
        )}

        {pending.length === 0 ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            Nothing waiting. A box appears here shortly after it enrols.
          </Typography>
        ) : (
          <Box sx={{ mt: 1.5 }}>
            {pending.map((reg) => (
              <PendingRow key={reg.id} reg={reg} onDeny={() => openDeny(reg)} />
            ))}
          </Box>
        )}
      </CardContent>

      <Dialog
        open={denying !== null}
        onClose={() => setDenying(null)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Deny {denying?.machineName || denying?.id}?</DialogTitle>
        <DialogContent>
          <Stack spacing={2}>
            {denyError && <Alert severity="error">{denyError}</Alert>}
            <Typography variant="body2">
              This records the reason and clears hz's copy of any wrapped key for{" "}
              <code>{denying ? address(denying) : ""}</code>. That is all it does.
            </Typography>
            {/* Denial is not revocation, and the copy must not imply it is. A
                box that was ever approved unwrapped the environment key onto
                its own disk; nothing hz deletes reaches it, and it keeps
                reading every value at that address including ones sealed
                later. Rotating the key is the only revocation there is. */}
            <Alert severity="info">
              <Typography variant="body2">
                It is <strong>not</strong> a revocation. If this box was ever
                approved it already holds the unwrapped environment key on its
                own disk, and denying reaches nothing there — it keeps opening
                every value at this address, including ones sealed after today.
                Rotating the environment key is the only revocation.
              </Typography>
            </Alert>
            <TextField
              label="Reason"
              size="small"
              multiline
              minRows={2}
              autoFocus
              required
              helperText="Required. A denial with no stated cause is indistinguishable from a mistake six months later."
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDenying(null)}>Cancel</Button>
          <Button
            color="error"
            variant="contained"
            disabled={reason.trim() === "" || deny.isPending}
            onClick={submitDeny}
          >
            Deny
          </Button>
        </DialogActions>
      </Dialog>
    </Card>
  );
}

import { useState } from "react";
import {
  Box,
  Button,
  MenuItem,
  TextField,
  Card,
  CardContent,
  Chip,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Typography,
  Stack,
  LinearProgress,
} from "@mui/material";
import CheckCircleIcon from "@mui/icons-material/CheckCircleOutlined";
import ErrorOutlineIcon from "@mui/icons-material/ErrorOutlineOutlined";
import {
  usePCIControls,
  useSetSAQLevel,
  useSavePolicy,
  usePolicy,
  useDisableAdminToken,
  useFixLogRetention,
  type PCIControl,
  type AccountPolicy,
  type SAQLevel,
} from "../api/auth";

// The PCI checklist.
//
// One row per control, unmet first. What a row offers depends on how much
// judgment the fix needs: a safe fix is a button, a fix that can lock somebody
// out is a button behind a dialog that says what they are agreeing to, and a
// fix hz cannot safely apply is a sentence saying where to go instead.
//
// The tab deliberately calls the same endpoints an operator would use by hand
// rather than a generic "apply" route — those endpoints carry refusals worth
// keeping (the admin-token disable will not strand the last way in; the MFA
// scope change will not jail admins who have no second factor), and their
// error messages are better than anything this page could invent.

export default function PCITab() {
  const { data, isLoading, error } = usePCIControls();

  if (isLoading) return <LinearProgress />;
  if (error) {
    return <Alert severity="error">Could not load controls: {error.message}</Alert>;
  }
  if (!data) return null;

  const applicable = data.controls.filter((c) => c.applicable);
  const unmet = applicable.filter((c) => !c.ok);
  const met = applicable.filter((c) => c.ok);
  const outOfLevel = data.controls.filter((c) => !c.applicable);

  return (
    <Box>
      <Alert severity="info" sx={{ mb: 3 }}>
        {data.disclaimer}
      </Alert>

      <SAQLevelPicker level={data.saqLevel} />

      <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
        {unmet.length === 0
          ? "Every control is in its hardened setting"
          : `${unmet.length} of ${applicable.length} controls are not in their hardened setting`}
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Several of these are deliberate choices rather than defects — an idle
        timeout signs you out too, and VPN MFA jails peers until they enrol.
        Anything that can lock you out asks before it acts.
      </Typography>

      <Stack spacing={1.5}>
        {unmet.map((c) => (
          <ControlRow key={c.name} control={c} />
        ))}
      </Stack>

      {met.length > 0 && (
        <Box sx={{ mt: 4 }}>
          <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 1 }}>
            Already hardened ({met.length})
          </Typography>
          <Stack spacing={1}>
            {met.map((c) => (
              <ControlRow key={c.name} control={c} />
            ))}
          </Stack>
        </Box>
      )}

      {outOfLevel.length > 0 && (
        <Box sx={{ mt: 4, opacity: 0.65 }}>
          <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 0.5 }}>
            Not asked about at this level ({outOfLevel.length})
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
            {/* Shown rather than hidden: they are still hardening worth having,
                and a client on a stricter questionnaire turns them into work. */}
            Still reported, because they are worth doing anyway — but your
            questionnaire does not ask about them, so they are not findings.
          </Typography>
          <Stack spacing={1}>
            {outOfLevel.map((c) => (
              <ControlRow key={c.name} control={c} />
            ))}
          </Stack>
        </Box>
      )}
    </Box>
  );
}

// Which questionnaire the operator answers decides which requirements are
// theirs. SAQ A asks about four of the controls hz reports; SAQ A-EP asks about
// all twelve, which is the gap a client demanding A-EP would open up.
type SAQOption = { value: SAQLevel; label: string; help: string };

const SAQ_UNDECLARED: SAQOption = {
  value: "",
  label: "Not declared",
  help: "Report everything hz knows how to check.",
};

const SAQ_LEVELS: SAQOption[] = [
  SAQ_UNDECLARED,
  {
    value: "a",
    label: "SAQ A",
    help: "Payments fully outsourced — a redirect or an iframe served entirely by the processor.",
  },
  {
    value: "a-ep",
    label: "SAQ A-EP",
    help: "Your site serves the page that creates the payment form, so it can affect the transaction without ever seeing card data.",
  },
  {
    value: "d",
    label: "SAQ D",
    help: "The full standard.",
  },
];

function SAQLevelPicker({ level }: { level: SAQLevel }) {
  const save = useSetSAQLevel();
  const current = SAQ_LEVELS.find((l) => l.value === level) ?? SAQ_UNDECLARED;

  return (
    <Box sx={{ mb: 3, p: 2, border: 1, borderColor: "divider", borderRadius: 1 }}>
      <TextField
        select
        size="small"
        label="Questionnaire"
        value={level}
        disabled={save.isPending}
        onChange={(e) => save.mutate(e.target.value as SAQLevel)}
        sx={{ minWidth: 220 }}
      >
        {SAQ_LEVELS.map((l) => (
          <MenuItem key={l.value} value={l.value}>
            {l.label}
          </MenuItem>
        ))}
      </TextField>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1 }}>
        {current.help} Requirement mapping is transcribed from the Council's own
        SAQ documents; re-check it against the current questionnaire before
        relying on it for an assessment.
      </Typography>
      {save.error && (
        <Alert severity="error" sx={{ mt: 1 }}>
          {save.error.message}
        </Alert>
      )}
    </Box>
  );
}

function ControlRow({ control }: { control: PCIControl }) {
  const [confirming, setConfirming] = useState(false);
  const fix = useFixFor(control);
  const kind = control.remediation?.kind;

  return (
    <Card variant="outlined">
      <CardContent sx={{ py: 1.5, "&:last-child": { pb: 1.5 } }}>
        <Box sx={{ display: "flex", alignItems: "flex-start", gap: 1.5 }}>
          {control.ok ? (
            <CheckCircleIcon color="success" fontSize="small" sx={{ mt: 0.4 }} />
          ) : (
            <ErrorOutlineIcon color="warning" fontSize="small" sx={{ mt: 0.4 }} />
          )}

          <Box sx={{ flexGrow: 1, minWidth: 0 }}>
            <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
              <Typography variant="body2" sx={{ fontWeight: 600 }}>
                {control.title}
              </Typography>
              <Chip size="small" variant="outlined" label={control.requirement} />
            </Box>

            <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
              {control.wants}
            </Typography>

            {control.detail && (
              <Typography variant="caption" sx={{ display: "block", mt: 0.5 }}>
                {control.detail}
              </Typography>
            )}

            {!fix && control.remediation?.hint && (
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: "block", mt: 0.5, fontStyle: "italic" }}
              >
                {control.remediation.hint}
              </Typography>
            )}

            {fix?.error && (
              <Alert severity="error" sx={{ mt: 1 }}>
                {fix.error}
              </Alert>
            )}
          </Box>

          {!control.ok && kind !== "manual" && fix && (
            <Button
              size="small"
              variant="outlined"
              color={kind === "decision" ? "warning" : "primary"}
              disabled={fix.pending}
              onClick={() => (kind === "decision" ? setConfirming(true) : fix.run())}
            >
              {fix.pending ? "Applying..." : control.remediation?.label}
            </Button>
          )}
        </Box>
      </CardContent>

      {fix && (
        <ConfirmDialog
          open={confirming}
          control={control}
          onClose={() => setConfirming(false)}
          onConfirm={() => {
            setConfirming(false);
            fix.run();
          }}
        />
      )}
    </Card>
  );
}

function ConfirmDialog({
  open,
  control,
  onClose,
  onConfirm,
}: {
  open: boolean;
  control: PCIControl;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
      <DialogTitle>{control.title}</DialogTitle>
      <DialogContent>
        <Alert severity="warning" sx={{ mb: 2 }}>
          {control.remediation?.warning}
        </Alert>
        {control.remediation?.hint && (
          <Typography variant="body2" color="text.secondary">
            {control.remediation.hint}
          </Typography>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button color="warning" variant="contained" onClick={onConfirm}>
          {control.remediation?.label?.replace(/…$/, "") ?? "Apply"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// useFixFor maps a control onto the endpoint that already implements its fix.
// Returns null for controls hz will not apply, so the row renders its hint
// instead of a button.
function useFixFor(control: PCIControl): {
  run: () => void;
  pending: boolean;
  error?: string;
} | null {
  const { data: policy } = usePolicy();
  const savePolicy = useSavePolicy();
  const disableToken = useDisableAdminToken();
  const fixRetention = useFixLogRetention();

  const savePolicyPatch = (patch: Partial<AccountPolicy>) => {
    if (!policy) return;
    savePolicy.mutate({ ...policy, ...patch });
  };

  switch (control.name) {
    case "password_history":
      return {
        run: () => savePolicyPatch({ passwordHistory: 4 }),
        pending: savePolicy.isPending,
        error: savePolicy.error?.message,
      };
    case "login_lockout":
      return {
        run: () =>
          savePolicyPatch({ maxFailedAttempts: 10, lockoutMinutes: 30 }),
        pending: savePolicy.isPending,
        error: savePolicy.error?.message,
      };
    case "session_idle_timeout":
      return {
        run: () => savePolicyPatch({ idleMinutes: 15 }),
        pending: savePolicy.isPending,
        error: savePolicy.error?.message,
      };
    case "password_rotation":
      return {
        run: () => savePolicyPatch({ passwordMaxAgeDays: 90 }),
        pending: savePolicy.isPending,
        error: savePolicy.error?.message,
      };
    case "log_persistence":
      return {
        run: () => fixRetention.mutate(),
        pending: fixRetention.isPending,
        error: fixRetention.error?.message,
      };
    case "no_shared_admin_token":
      return {
        run: () => disableToken.mutate(),
        pending: disableToken.isPending,
        error: disableToken.error?.message,
      };
    default:
      // The VPN MFA controls included: enabling them is a multi-field decision
      // (scope, session lengths, who is enrolled) that the VPN MFA tab already
      // presents properly. A one-button version here would have to pick those
      // for the operator.
      return null;
  }
}

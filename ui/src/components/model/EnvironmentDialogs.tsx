/**
 * Declaring, editing and removing an environment (a "rung"), from the controls
 * on the Environments panel of a project's Overview (`p.$project.index.tsx`).
 *
 * SAME SHAPE AS `ProjectDialogs.tsx`, deliberately not imported from it — a
 * separate file so the two land without touching each other's diff. Add writes
 * immediately, because declaring a rung renders nothing on its own (no DNS
 * record, no proxy backend, no machine change) until something is placed on
 * it. Remove runs the server's dry run first: the dialog asks what the
 * removal would take before it offers the button that does it, and the list it
 * shows is the server's own list.
 *
 * EVERY "PROMOTED FROM" CONTROL IS A SELECT, NEVER A TEXT FIELD.
 * `config.CheckPromotion` refuses a promotion whose source sits in a different
 * project — "promotion is within one project" (`internal/config/config.go:810`)
 * — and `ValidateEnvironments` refuses a `from` that names no rung actually
 * declared in that project. A free-text field would let the operator type an
 * edge Save refuses every time; the select only ever offers rungs that already
 * exist in the SAME project, so a promotion edge to an undeclared rung cannot
 * be typed here at all.
 *
 * ADD ALWAYS DECLARES INTO THE OVERVIEW'S OWN PROJECT. The Environments panel
 * may be showing this project plus its descendants (the own/own+descendants
 * scope control), but "+" has one destination — the project whose page you are
 * on — never a descendant merely visible in the list. Edit and remove act on
 * the RUNG'S OWN PROJECT instead, which is `env.project` and not necessarily
 * the page's project when the wider scope is showing a descendant's rung.
 */
import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  MenuItem,
  TextField,
  Typography,
} from "@mui/material";
import {
  useAddEnvironment,
  useRemoveEnvironment,
  useSetEnvironment,
} from "../../api/environmentHooks";
import type { DependantResp, EnvironmentResp, RemovalResp } from "../../api/generated-types";
import { POSTURES } from "./model.ts";

/**
 * This project's already-declared rungs, as the only legal values for a
 * promotion source. `excludeName` drops the rung being edited — nothing
 * promotes from itself, and `ValidateEnvironments`'s cycle walk would refuse it
 * anyway, but there is no reason to offer a choice Save always rejects.
 */
function FromSelect({
  value,
  onChange,
  siblings,
  excludeName,
}: {
  value: string;
  onChange: (v: string) => void;
  siblings: EnvironmentResp[];
  excludeName?: string;
}) {
  const options = siblings.filter((s) => s.name !== excludeName);
  return (
    <TextField
      select
      fullWidth
      label="Promoted from"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      helperText={
        options.length === 0
          ? "No other rung in this project yet to promote from — declare one first, or leave this blank."
          : "Only a rung already declared in this same project — a promotion edge cannot cross projects."
      }
      sx={{ mb: 2 }}
    >
      <MenuItem value="">
        <em>none — nothing promotes into this rung</em>
      </MenuItem>
      {options.map((s) => (
        <MenuItem key={s.name} value={s.name} sx={{ fontFamily: "monospace" }}>
          {s.name}
        </MenuItem>
      ))}
    </TextField>
  );
}

export function AddEnvironmentDialog({
  open,
  project,
  siblings,
  onClose,
}: {
  open: boolean;
  /** The Overview's own project. Never a descendant — see the file header. */
  project: string;
  /** `project`'s already-declared rungs, for the from-select. */
  siblings: EnvironmentResp[];
  onClose: () => void;
}) {
  const add = useAddEnvironment();
  const [name, setName] = useState("");
  const [posture, setPosture] = useState("");
  const [from, setFrom] = useState("");
  const [version, setVersion] = useState("");

  // Each opening starts blank, not from the last attempt.
  useEffect(() => {
    if (open) {
      setName("");
      setPosture("");
      setFrom("");
      setVersion("");
      add.reset();
    }
  }, [open]);

  const trimmed = name.trim();
  const canSubmit = trimmed !== "" && posture !== "" && !add.isPending;

  const submit = () =>
    add.mutate(
      {
        project,
        name: trimmed,
        posture,
        ...(from ? { from } : {}),
        ...(version.trim() ? { version: version.trim() } : {}),
      },
      { onSuccess: onClose },
    );

  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>
        Add an environment to <code>{project}</code>
      </DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
          Declaring a rung renders nothing by itself — no machine, no service moves — until
          something is placed on it or a service is assigned to it.
        </Typography>
        <TextField
          autoFocus
          fullWidth
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && canSubmit) submit();
          }}
          helperText='Free text, separate from posture — an environment named "prod" may honestly sit at staging&apos;s posture.'
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <TextField
          select
          fullWidth
          label="Posture"
          value={posture}
          onChange={(e) => setPosture(e.target.value)}
          helperText="Closed to dev, staging and prod (config.PostureRank) — a posture nothing ranks would compare below every other rung."
          sx={{ mb: 2 }}
        >
          {POSTURES.map((p) => (
            <MenuItem key={p} value={p}>
              {p}
            </MenuItem>
          ))}
        </TextField>
        <FromSelect value={from} onChange={setFrom} siblings={siblings} />
        <TextField
          fullWidth
          label="Version"
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          helperText="Left blank, hz projects NO PACKAGE AT ALL for this rung — not the newest one."
          sx={{ mb: 1 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        {add.error ? (
          <Alert severity="error" sx={{ mt: 1 }}>
            {add.error.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={!canSubmit}>
          {add.isPending ? "Adding…" : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function EditEnvironmentDialog({
  env,
  siblings,
  onClose,
}: {
  /** The rung being edited, its OWN project and name; null is closed. */
  env: EnvironmentResp | null;
  /** `env`'s siblings within ITS OWN project, for the from-select. */
  siblings: EnvironmentResp[];
  onClose: () => void;
}) {
  const set = useSetEnvironment();
  const [posture, setPosture] = useState("");
  const [from, setFrom] = useState("");
  const [version, setVersion] = useState("");

  // Each opening starts from what hz holds now, not from the last edit.
  useEffect(() => {
    if (env === null) return;
    setPosture(env.posture);
    setFrom(env.from ?? "");
    setVersion(env.version ?? "");
    set.reset();
  }, [env]);

  // Only a field that actually changed is sent — EnvironmentSetReq's pointer
  // contract (environmentHooks.ts) — so an untouched field is never restated
  // and a promotion edge cannot be dropped by accident on an unrelated edit.
  const changedPosture = env !== null && posture !== env.posture ? posture : undefined;
  const changedFrom = env !== null && from !== (env.from ?? "") ? from : undefined;
  const changedVersion = env !== null && version !== (env.version ?? "") ? version : undefined;
  const dirty = changedPosture !== undefined || changedFrom !== undefined || changedVersion !== undefined;

  const submit = () => {
    if (env === null) return;
    set.mutate(
      {
        project: env.project,
        name: env.name,
        ...(changedPosture !== undefined ? { posture: changedPosture } : {}),
        ...(changedFrom !== undefined ? { from: changedFrom } : {}),
        ...(changedVersion !== undefined ? { version: changedVersion } : {}),
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open={env !== null} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>
        Edit {env ? <code>{env.project}/{env.name}</code> : null}
      </DialogTitle>
      <DialogContent>
        <TextField
          select
          fullWidth
          label="Posture"
          value={posture}
          onChange={(e) => setPosture(e.target.value)}
          helperText="Closed to dev, staging and prod (config.PostureRank)."
          sx={{ mb: 2 }}
        >
          {POSTURES.map((p) => (
            <MenuItem key={p} value={p}>
              {p}
            </MenuItem>
          ))}
        </TextField>
        <FromSelect value={from} onChange={setFrom} siblings={siblings} excludeName={env?.name} />
        <TextField
          fullWidth
          label="Version"
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          helperText="Cleared, hz projects NO PACKAGE AT ALL for this rung — not the newest one."
          sx={{ mb: 1 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        {set.error ? (
          <Alert severity="error" sx={{ mt: 1 }}>
            {set.error.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={!dirty || set.isPending}>
          {set.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

/** Copied from `ProjectDialogs.tsx` rather than shared — see the file header. */
function DependantList({ title, items }: { title: string; items: DependantResp[] }) {
  if (items.length === 0) return null;
  return (
    <Box sx={{ mb: 1.5 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
        {title}
      </Typography>
      {items.map((d) => (
        <Typography key={`${d.kind}:${d.name}`} variant="body2" sx={{ color: "text.secondary" }}>
          <code>
            {d.kind} {d.name}
          </code>{" "}
          — {d.how}
        </Typography>
      ))}
    </Box>
  );
}

export function RemoveEnvironmentDialog({
  target,
  onClose,
}: {
  /** The rung to remove, its OWN project and name; null is closed. */
  target: { project: string; name: string } | null;
  onClose: () => void;
}) {
  const preview = useRemoveEnvironment();
  const remove = useRemoveEnvironment();
  const [cascade, setCascade] = useState(false);
  const [plan, setPlan] = useState<RemovalResp | null>(null);

  // The dry run, on open and whenever cascade flips: the button below is only
  // ever offered against the answer to exactly the request it will send.
  useEffect(() => {
    if (target === null) {
      setCascade(false);
      setPlan(null);
      preview.reset();
      remove.reset();
      return;
    }
    setPlan(null);
    preview.mutate({ project: target.project, name: target.name, cascade }, { onSuccess: setPlan });
  }, [target, cascade]);

  const blocked = plan?.blocked ?? [];
  const confirm = () => {
    if (target === null) return;
    remove.mutate(
      { project: target.project, name: target.name, cascade, confirm: true },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open={target !== null} onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>
        Remove {target ? <code>{target.project}/{target.name}</code> : null}
      </DialogTitle>
      <DialogContent>
        <FormControlLabel
          control={<Checkbox checked={cascade} onChange={(e) => setCascade(e.target.checked)} />}
          label="Cascade: also cut every promotion edge into this rung and unassign its services"
          sx={{ mb: 1 }}
        />
        {preview.isPending || (!plan && !preview.error) ? (
          <Box sx={{ display: "flex", alignItems: "center", gap: 2, py: 2 }}>
            <CircularProgress size={20} />
            <Typography>Asking hz what this would take — nothing is written yet…</Typography>
          </Box>
        ) : preview.error ? (
          <Alert severity="error">{preview.error.message}</Alert>
        ) : plan ? (
          <>
            {blocked.length > 0 ? (
              <Alert severity="warning" sx={{ mb: 1.5 }}>
                hz refuses this removal while these depend on it.{" "}
                {cascade ? "" : "Cascade takes them too, and is shown here before it does anything."}
              </Alert>
            ) : null}
            <DependantList title="Blocked by" items={blocked} />
            <DependantList title="Would remove" items={plan.removes} />
          </>
        ) : null}
        {remove.error ? <Alert severity="error">{remove.error.message}</Alert> : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          color="error"
          variant="contained"
          onClick={confirm}
          disabled={!plan || blocked.length > 0 || remove.isPending}
        >
          {remove.isPending ? "Removing…" : "Remove"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

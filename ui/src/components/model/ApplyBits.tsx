/**
 * N4a on a project's Overview: what each rung RUNS (its newest apply), its
 * hold, and the Apply / Hold / Unhold actions. Server:
 * `internal/server/handlers_api_apply.go`.
 *
 * A PROMOTION APPROVES; AN APPLY IS WHAT RUNS. A nested hz pulls the newest
 * apply, never the newest promotion — so a rung is held by default, and the
 * Apply action is offered exactly when the rung's newest promotion has not
 * been applied yet.
 *
 * APPLIED HAS FOUR ANSWERS, never a blank (invariant 2): version · who · when,
 * "nothing applied" when hz answered with none, "asking…", and "cannot ask"
 * when hz could not be asked. Hold likewise: reason · who, "none", or the
 * same two unknowns.
 *
 * Panels are separate from their `<Dialog>` shells: SSR renders nothing for a
 * Portal, and the render checks draw the panels.
 */
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  TextField,
  Typography,
} from "@mui/material";
import type { EnvironmentResp, HoldResp, PromotionResp, RungDeployStateResp } from "../../api/generated-types";
import { useApply, useHold } from "../../api/deployHooks";
import { formatAge } from "../drift/observation.ts";

/** What hz answered about applies and holds. */
export type AppliedSource =
  | { known: true; rungs: RungDeployStateResp[] }
  | { known: false; loading: true }
  | { known: false; loading: false; why: string };

/** One rung's state, or null when hz answered and does not list it. */
export function stateFor(source: AppliedSource, project: string, environment: string): RungDeployStateResp | null {
  if (!source.known) return null;
  return source.rungs.find((r) => r.project === project && r.environment === environment) ?? null;
}

/**
 * The promotion an Apply would apply: the NEWEST promotion into the rung,
 * when it is not what the rung already runs. null = nothing to offer.
 */
export function applyOffer(
  source: AppliedSource,
  promotions: PromotionResp[] | undefined,
  env: EnvironmentResp,
): PromotionResp | null {
  if (!source.known || promotions === undefined) return null;
  const newest = promotions
    .filter((p) => p.project === env.project && p.to === env.name)
    .reduce<PromotionResp | null>((a, p) => (a === null || p.id > a.id ? p : a), null);
  if (newest === null) return null;
  const st = stateFor(source, env.project, env.name);
  if (st?.applied && st.applied.promotion_id === newest.id) return null;
  return newest;
}

function Unknown({ source, what }: { source: AppliedSource; what: string }) {
  if (!source.known && source.loading) {
    return (
      <Typography component="span" variant="body2" data-applied="loading" sx={{ color: "text.secondary" }}>
        asking…
      </Typography>
    );
  }
  const why = !source.known && !source.loading ? source.why : "";
  return (
    <Typography
      component="span"
      variant="body2"
      data-applied="unknown"
      sx={{ color: "warning.main" }}
      title={`hz could not be asked ${what}: ${why}`}
    >
      cannot ask
    </Typography>
  );
}

/** One rung's Applied cell. */
export function AppliedCell({ source, env }: { source: AppliedSource; env: EnvironmentResp }) {
  if (!source.known) return <Unknown source={source} what="what this rung runs" />;
  const st = stateFor(source, env.project, env.name);
  const a = st?.applied;
  if (!a) {
    return (
      <Typography
        component="span"
        variant="body2"
        data-applied="none"
        sx={{ color: "text.secondary" }}
        title="Nothing was applied to this rung. A nested hz serving it answers 404 until an apply — a promotion alone changes nothing it pulls."
      >
        nothing applied
      </Typography>
    );
  }
  const deleted = st?.artifact?.deleted_at;
  return (
    <Typography
      component="span"
      variant="body2"
      data-applied="applied"
      sx={{ fontFamily: "monospace" }}
      title={`apply #${a.id} of promotion #${a.promotion_id} · sha256 ${a.artifact_sha256} · ${a.applied_at}`}
    >
      {a.version} · {a.applied_by} · {formatAge(a.age_seconds)} ago
      {deleted ? (
        <Typography
          component="span"
          variant="body2"
          data-artifact-deleted
          sx={{ color: "error.main", ml: 1 }}
          title={st?.artifact?.deleted_why}
        >
          artifact deleted {deleted}
        </Typography>
      ) : null}
    </Typography>
  );
}

/** One rung's Hold cell. */
export function HoldCell({ source, env }: { source: AppliedSource; env: EnvironmentResp }) {
  if (!source.known) return <Unknown source={source} what="whether this rung is held" />;
  const h = stateFor(source, env.project, env.name)?.hold;
  if (!h) {
    return (
      <Typography component="span" variant="body2" data-hold="none" sx={{ color: "text.secondary" }}>
        none
      </Typography>
    );
  }
  return (
    <Typography component="span" variant="body2" data-hold="held" sx={{ color: "warning.main" }} title={`held at ${h.at}`}>
      held: {h.reason} · {h.by}
    </Typography>
  );
}

/** The Apply dialog's contents. */
export function ApplyPanel({
  env,
  offer,
  onClose,
}: {
  env: EnvironmentResp;
  offer: PromotionResp;
  onClose: () => void;
}) {
  const apply = useApply();
  useEffect(() => {
    apply.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [env.project, env.name, offer.id]);
  return (
    <>
      <DialogTitle>
        Apply <code>{offer.version}</code> to{" "}
        <code>
          {env.project}/{env.name}
        </code>
      </DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ mb: 2 }} data-apply-offer={offer.id}>
          Promotion #{offer.id}: {offer.from} → {offer.to}, by {offer.promotedBy}, {formatAge(offer.ageSeconds)} ago.
          Artifact <code>{offer.artifact_sha256.slice(0, 12)}</code>.
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          The rung then runs it: a nested hz serving {env.name} pulls it on its next poll.
        </Typography>
        {apply.error ? (
          <Alert severity="error" sx={{ mt: 2, whiteSpace: "pre-line" }} data-apply-refusal>
            {apply.error.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          disabled={apply.isPending}
          onClick={() =>
            apply.mutate({ project: env.project, environment: env.name, version: offer.version }, { onSuccess: onClose })
          }
        >
          Apply
        </Button>
      </DialogActions>
    </>
  );
}

export function ApplyDialog({
  env,
  offer,
  onClose,
}: {
  env: EnvironmentResp | null;
  offer: PromotionResp | null;
  onClose: () => void;
}) {
  return (
    <Dialog open={env !== null && offer !== null} onClose={onClose} fullWidth maxWidth="xs">
      {env !== null && offer !== null ? <ApplyPanel env={env} offer={offer} onClose={onClose} /> : null}
    </Dialog>
  );
}

/** Hold (with a reason) or, when held, Unhold. */
export function HoldPanel({
  env,
  held,
  onClose,
}: {
  env: EnvironmentResp;
  held: HoldResp | null | undefined;
  onClose: () => void;
}) {
  const hold = useHold(true);
  const unhold = useHold(false);
  const [reason, setReason] = useState("");
  useEffect(() => {
    setReason("");
    hold.reset();
    unhold.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [env.project, env.name]);
  const rung = `${env.project}/${env.name}`;
  const err = hold.error ?? unhold.error;
  return (
    <>
      <DialogTitle>
        {held ? "Unhold" : "Hold"} <code>{rung}</code>
      </DialogTitle>
      <DialogContent>
        {held ? (
          <Typography variant="body2" data-hold-panel="held">
            Held by {held.by} at {held.at}: {held.reason}
          </Typography>
        ) : (
          <TextField
            fullWidth
            autoFocus
            label="Reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            helperText="Required. A hold rides desired; whoever acts on it must not flip while it is set."
            sx={{ mt: 1 }}
            data-hold-panel="free"
          />
        )}
        {err ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            {err.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        {held ? (
          <Button
            variant="contained"
            disabled={unhold.isPending}
            onClick={() => unhold.mutate({ project: env.project, environment: env.name }, { onSuccess: onClose })}
          >
            Unhold
          </Button>
        ) : (
          <Button
            variant="contained"
            color="warning"
            disabled={reason.trim() === "" || hold.isPending}
            onClick={() =>
              hold.mutate({ project: env.project, environment: env.name, reason: reason.trim() }, { onSuccess: onClose })
            }
          >
            Hold
          </Button>
        )}
      </DialogActions>
    </>
  );
}

export function HoldDialog({
  env,
  held,
  onClose,
}: {
  env: EnvironmentResp | null;
  held: HoldResp | null | undefined;
  onClose: () => void;
}) {
  return (
    <Dialog open={env !== null} onClose={onClose} fullWidth maxWidth="xs">
      {env !== null ? <HoldPanel env={env} held={held} onClose={onClose} /> : null}
    </Dialog>
  );
}

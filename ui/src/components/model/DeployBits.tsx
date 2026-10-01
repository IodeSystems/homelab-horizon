/**
 * The release controls on a project's Overview (plan/plan.md "Redline push to
 * prod — the plan", H1–H3): the Reported cell, the Promote dialog, and the
 * promotion record under the Environments table.
 *
 * REPORTED HAS THREE ANSWERS, never a blank (invariant 2): what the rung last
 * reported (version · host · age), "nothing reported" when hz answered and has
 * no report, and "cannot ask" when hz could not be asked. The second is an
 * ordinary early state; the third is a failed instrument, and they read
 * differently.
 *
 * PROMOTE IS OFFERED ONLY ON A RUNG WITH A `from` EDGE — there is nothing to
 * promote from otherwise, and `config.CheckPromotion` would refuse it. The
 * version is prefilled from the SOURCE rung's newest report, because that is
 * the only version the evidence gate accepts; the server's refusal is shown
 * verbatim, since its words are the instruction.
 *
 * Panels are separate from their `<Dialog>` shells for the reason
 * `SegmentDialogs.tsx` gives: SSR renders nothing for a Portal.
 */
import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import type { DeployReportResp, EnvironmentResp, PromotionResp } from "../../api/generated-types";
import { usePromote } from "../../api/deployHooks";
import { formatAge } from "../drift/observation.ts";
import { EmptyRow } from "./FlowBits";

/**
 * What hz answered about reports: the list, still asking, or that it could
 * not be asked. Loading is not failure, and neither is "nothing reported".
 */
export type ReportSource =
  | { known: true; reports: DeployReportResp[] }
  | { known: false; loading: true }
  | { known: false; loading: false; why: string };

/** The newest report for one rung, or null when the rung never reported. */
export function reportFor(source: ReportSource, project: string, environment: string): DeployReportResp | null {
  if (!source.known) return null;
  return source.reports.find((r) => r.project === project && r.environment === environment) ?? null;
}

/** One rung's Reported cell. */
export function ReportedCell({ source, env }: { source: ReportSource; env: EnvironmentResp }) {
  if (!source.known && source.loading) {
    return (
      <Typography component="span" variant="body2" data-reported="loading" sx={{ color: "text.secondary" }}>
        asking…
      </Typography>
    );
  }
  if (!source.known) {
    return (
      <Typography
        component="span"
        variant="body2"
        data-reported="unknown"
        sx={{ color: "warning.main" }}
        title={`hz could not be asked what this rung reported: ${source.why}`}
      >
        cannot ask
      </Typography>
    );
  }
  const r = reportFor(source, env.project, env.name);
  if (r === null) {
    return (
      <Typography
        component="span"
        variant="body2"
        data-reported="none"
        sx={{ color: "text.secondary" }}
        title="No deploy has posted /api/v1/deploys/report for this rung. A promote out of it is refused until one does."
      >
        nothing reported
      </Typography>
    );
  }
  return (
    <Typography
      component="span"
      variant="body2"
      data-reported="reported"
      sx={{ fontFamily: "monospace" }}
      title={`${r.describe || r.version} · sha256 ${r.artifact_sha256} · reported by ${r.reportedBy} at ${r.reportedAt}`}
    >
      {r.version} · {r.host} · {formatAge(r.ageSeconds)} ago
    </Typography>
  );
}

/** The dialog's contents. `sourceReport` is the newest report of `env.from`. */
export function PromotePanel({
  env,
  sourceReport,
  onClose,
}: {
  env: EnvironmentResp;
  sourceReport: DeployReportResp | null;
  onClose: () => void;
}) {
  const promote = usePromote();
  const from = env.from ?? "";
  const [version, setVersion] = useState(sourceReport?.version ?? "");
  const [allowDowngrade, setAllowDowngrade] = useState(false);

  useEffect(() => {
    setVersion(sourceReport?.version ?? "");
    setAllowDowngrade(false);
    promote.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [env.project, env.name, sourceReport?.id]);

  const submit = () =>
    promote.mutate(
      { project: env.project, from, to: env.name, version: version.trim(), allowDowngrade },
      { onSuccess: onClose },
    );

  return (
    <>
      <DialogTitle>
        Promote{" "}
        <code>
          {env.project}/{from}
        </code>{" "}
        → <code>{env.name}</code>
      </DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
          {sourceReport
            ? `${from} last reported ${sourceReport.version} on ${sourceReport.host}, ${formatAge(sourceReport.ageSeconds)} ago.`
            : `${from} has reported nothing — hz refuses a promote without a report.`}
          {env.version ? ` ${env.name} declares ${env.version}.` : ` ${env.name} declares no version.`}
        </Typography>
        <TextField
          fullWidth
          label="Version"
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          helperText={`Must be ${from}'s newest reported version. Its artifact sha256 is pinned.`}
          sx={{ mb: 1, "& input": { fontFamily: "monospace" } }}
        />
        <FormControlLabel
          control={<Checkbox checked={allowDowngrade} onChange={(e) => setAllowDowngrade(e.target.checked)} />}
          label="Allow a downgrade (a rollback — recorded as one)"
        />
        {promote.error ? (
          <Alert severity="error" sx={{ mt: 1 }} data-promote-refusal>
            {promote.error.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={version.trim() === "" || promote.isPending}>
          Promote
        </Button>
      </DialogActions>
    </>
  );
}

export function PromoteDialog({
  env,
  sourceReport,
  onClose,
}: {
  /** The TARGET rung; null is closed. */
  env: EnvironmentResp | null;
  sourceReport: DeployReportResp | null;
  onClose: () => void;
}) {
  return (
    <Dialog open={env !== null} onClose={onClose} fullWidth maxWidth="xs">
      {env !== null ? <PromotePanel env={env} sourceReport={sourceReport} onClose={onClose} /> : null}
    </Dialog>
  );
}

/** The promotion record: who, when, version, from → to. */
export function RecentPromotions({
  promotions,
  error,
  loading,
  project,
}: {
  promotions: PromotionResp[] | undefined;
  error: Error | null;
  loading: boolean;
  project: string;
}) {
  if (error) {
    return (
      <Alert severity="warning" sx={{ mb: 3 }}>
        hz could not be asked for the promotion record: {error.message}
      </Alert>
    );
  }
  if (loading || promotions === undefined) {
    return <Typography sx={{ color: "text.secondary", mb: 3 }}>Asking hz for the promotion record…</Typography>;
  }
  if (promotions.length === 0) {
    return (
      <Box sx={{ mb: 3 }}>
        <EmptyRow text={`No promotions in ${project}.`} />
      </Box>
    );
  }
  return (
    <TableContainer component={Paper} sx={{ mb: 3 }}>
      <Table size="small" aria-label="Recent promotions">
        <TableHead>
          <TableRow>
            <TableCell>Promoted</TableCell>
            <TableCell>Version</TableCell>
            <TableCell>From → to</TableCell>
            <TableCell>By</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {promotions.map((p) => (
            <TableRow key={p.id} hover>
              <TableCell title={p.promotedAt}>{formatAge(p.ageSeconds)} ago</TableCell>
              <TableCell sx={{ fontFamily: "monospace" }} title={`sha256 ${p.artifact_sha256}`}>
                {p.version}
                {p.downgrade ? (
                  <Typography component="span" variant="body2" sx={{ color: "warning.main", ml: 1 }}>
                    downgrade
                  </Typography>
                ) : null}
              </TableCell>
              <TableCell sx={{ fontFamily: "monospace" }}>
                {p.project}/{p.from} → {p.to}
              </TableCell>
              <TableCell sx={{ fontFamily: "monospace" }}>{p.promotedBy}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

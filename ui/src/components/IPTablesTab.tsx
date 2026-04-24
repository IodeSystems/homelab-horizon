import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  IconButton,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from "@mui/material";
import DeleteIcon from "@mui/icons-material/Delete";
import VerifiedUserIcon from "@mui/icons-material/VerifiedUser";
import RemoveCircleOutlineIcon from "@mui/icons-material/RemoveCircleOutline";
import RefreshIcon from "@mui/icons-material/Refresh";
import {
  useBlessIPTablesRule,
  useIPTablesRules,
  useReconcileIPTables,
  useRemoveIPTablesRule,
  useUnblessIPTablesRule,
} from "../api/hooks";
import type {
  ClassifiedRule,
  IPTablesReport,
  IPTablesRule,
  IPTablesRuleState,
} from "../api/types";
import { ruleCanonical } from "../api/types";

// StateChip colors each classification distinctly so the table reads at a
// glance. "expected" is muted success (common, don't grab attention),
// "stale" is warning (auto-heal will remove), "blessed" is info
// (admin-approved), "unknown" is error (needs review).
function StateChip({ state }: { state: IPTablesRuleState }) {
  const props = (() => {
    switch (state) {
      case "expected":
        return { color: "success" as const, variant: "outlined" as const };
      case "stale":
        return { color: "warning" as const, variant: "filled" as const };
      case "blessed":
        return { color: "info" as const, variant: "outlined" as const };
      case "unknown":
        return { color: "error" as const, variant: "filled" as const };
    }
  })();
  return <Chip label={state} size="small" {...props} />;
}

function RowActions({
  rule,
  state,
  onBless,
  onUnbless,
  onRemove,
}: {
  rule: IPTablesRule;
  state: IPTablesRuleState;
  onBless: () => void;
  onUnbless: () => void;
  onRemove: () => void;
}) {
  const canonical = ruleCanonical(rule);
  return (
    <Stack direction="row" spacing={0.5}>
      {state === "unknown" && (
        <>
          <Tooltip title={`Bless "${canonical}" — reconciler will leave it alone`}>
            <IconButton size="small" color="info" onClick={onBless}>
              <VerifiedUserIcon fontSize="small" />
            </IconButton>
          </Tooltip>
          <Tooltip title="Remove this rule from iptables">
            <IconButton size="small" color="error" onClick={onRemove}>
              <DeleteIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </>
      )}
      {state === "blessed" && (
        <Tooltip title="Unbless — rule stays live, but returns to unknown classification">
          <IconButton size="small" onClick={onUnbless}>
            <RemoveCircleOutlineIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      )}
      {state === "stale" && (
        <Tooltip title="Remove now — auto-heal will do this on the next reconcile anyway">
          <IconButton size="small" color="warning" onClick={onRemove}>
            <DeleteIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      )}
      {state === "expected" && <Typography variant="caption">—</Typography>}
    </Stack>
  );
}

// Filter chips — click to narrow the table. Summary counts come from the
// /rules response (server-computed) so UI + server agree on what each means.
function FilterChips({
  summary,
  filter,
  onFilter,
}: {
  summary: { expected: number; stale: number; blessed: number; unknown: number };
  filter: IPTablesRuleState | "all";
  onFilter: (s: IPTablesRuleState | "all") => void;
}) {
  const chipFor = (label: string, value: IPTablesRuleState | "all", count: number) => (
    <Chip
      label={`${label} (${count})`}
      onClick={() => onFilter(value)}
      variant={filter === value ? "filled" : "outlined"}
      color={
        value === "stale" || value === "unknown"
          ? count > 0
            ? "error"
            : "default"
          : "default"
      }
      size="small"
    />
  );
  const total = summary.expected + summary.stale + summary.blessed + summary.unknown;
  return (
    <Stack direction="row" spacing={1} flexWrap="wrap">
      {chipFor("All", "all", total)}
      {chipFor("Expected", "expected", summary.expected)}
      {chipFor("Stale", "stale", summary.stale)}
      {chipFor("Blessed", "blessed", summary.blessed)}
      {chipFor("Unknown", "unknown", summary.unknown)}
    </Stack>
  );
}

function ReportDialog({
  report,
  onClose,
}: {
  report: IPTablesReport | null;
  onClose: () => void;
}) {
  return (
    <Dialog open={!!report} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>Reconcile report</DialogTitle>
      <DialogContent>
        {report && (
          <Stack spacing={2}>
            <DialogContentText>
              Summary — expected: {report.summary.expected}, stale: {report.summary.stale},
              blessed: {report.summary.blessed}, unknown: {report.summary.unknown}.
            </DialogContentText>
            {report.inferred_old && (
              <Alert severity="info">
                Inferred old iface <strong>{report.inferred_old}</strong> from stale
                MASQUERADE rule — LastLocalIface now persisted so subsequent reconciles
                have an explicit baseline.
              </Alert>
            )}
            {report.deleted && report.deleted.length > 0 && (
              <Box>
                <Typography variant="subtitle2">Deleted ({report.deleted.length})</Typography>
                {report.deleted.map((r, i) => (
                  <Typography key={i} variant="caption" sx={{ fontFamily: "monospace", display: "block" }}>
                    {ruleCanonical(r)}
                  </Typography>
                ))}
              </Box>
            )}
            {report.added && report.added.length > 0 && (
              <Box>
                <Typography variant="subtitle2">Added ({report.added.length})</Typography>
                {report.added.map((r, i) => (
                  <Typography key={i} variant="caption" sx={{ fontFamily: "monospace", display: "block" }}>
                    {ruleCanonical(r)}
                  </Typography>
                ))}
              </Box>
            )}
            {report.errors && report.errors.length > 0 && (
              <Alert severity="error">
                <Stack spacing={0.5}>
                  {report.errors.map((e, i) => (
                    <Typography key={i} variant="body2">{e}</Typography>
                  ))}
                </Stack>
              </Alert>
            )}
          </Stack>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
}

function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  confirmColor,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  body: string;
  confirmLabel: string;
  confirmColor?: "error" | "warning" | "primary";
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Dialog open={open} onClose={onCancel}>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ fontFamily: "monospace" }}>{body}</DialogContentText>
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button color={confirmColor || "primary"} variant="contained" onClick={onConfirm}>
          {confirmLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function IPTablesTab() {
  const { data, isLoading, error } = useIPTablesRules();
  const bless = useBlessIPTablesRule();
  const unbless = useUnblessIPTablesRule();
  const remove = useRemoveIPTablesRule();
  const reconcile = useReconcileIPTables();

  const [filter, setFilter] = useState<IPTablesRuleState | "all">("all");
  const [report, setReport] = useState<IPTablesReport | null>(null);
  const [removeTarget, setRemoveTarget] = useState<IPTablesRule | null>(null);

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", p: 4 }}>
        <CircularProgress />
      </Box>
    );
  }
  if (error) {
    return <Alert severity="error">Failed to load iptables rules: {error.message}</Alert>;
  }
  if (!data) return null;

  const rows = filter === "all" ? data.rules : data.rules.filter((r) => r.state === filter);

  return (
    <Stack spacing={2}>
      <Paper sx={{ p: 2, bgcolor: "background.default" }} variant="outlined">
        <Typography variant="body2" color="text.secondary">
          Horizon-managed iptables rules on this host — nat POSTROUTING, filter FORWARD, and
          the WG-FORWARD chain. Rules in other chains are not shown here. Auto-heal deletes
          <strong> stale </strong> rules and adds missing <strong>expected</strong> rules on every
          60s health-check tick. <strong>Unknown</strong> rules are surfaced for you to bless
          (keep) or remove; nothing outside horizon's managed chains is ever auto-touched.
        </Typography>
      </Paper>

      <Paper variant="outlined" sx={{ p: 2 }}>
        <Stack direction="row" spacing={2} alignItems="center" sx={{ mb: 2 }}>
          <FilterChips summary={data.summary} filter={filter} onFilter={setFilter} />
          <Box sx={{ flex: 1 }} />
          <Button
            variant="contained"
            startIcon={reconcile.isPending ? <CircularProgress size={16} /> : <RefreshIcon />}
            onClick={() => reconcile.mutate(undefined, { onSuccess: (r) => setReport(r) })}
            disabled={reconcile.isPending}
          >
            Reconcile now
          </Button>
        </Stack>

        <TableContainer>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell width={80}>State</TableCell>
                <TableCell width={60}>Table</TableCell>
                <TableCell width={120}>Chain</TableCell>
                <TableCell>Rule</TableCell>
                <TableCell width={130}>Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5}>
                    <Typography variant="body2" color="text.secondary" sx={{ py: 2, textAlign: "center" }}>
                      No rules match this filter.
                    </Typography>
                  </TableCell>
                </TableRow>
              )}
              {rows.map((cr: ClassifiedRule, i) => (
                <TableRow key={i} hover>
                  <TableCell>
                    <Stack spacing={0.5}>
                      <StateChip state={cr.state} />
                      {cr.reason && (
                        <Tooltip title={cr.reason}>
                          <Typography variant="caption" color="text.secondary">
                            ⓘ
                          </Typography>
                        </Tooltip>
                      )}
                    </Stack>
                  </TableCell>
                  <TableCell sx={{ fontFamily: "monospace" }}>{cr.rule.Table}</TableCell>
                  <TableCell sx={{ fontFamily: "monospace" }}>{cr.rule.Chain}</TableCell>
                  <TableCell sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}>
                    {cr.rule.Args.join(" ")}
                  </TableCell>
                  <TableCell>
                    <RowActions
                      rule={cr.rule}
                      state={cr.state}
                      onBless={() => bless.mutate(ruleCanonical(cr.rule))}
                      onUnbless={() => unbless.mutate(ruleCanonical(cr.rule))}
                      onRemove={() => setRemoveTarget(cr.rule)}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      </Paper>

      <ReportDialog report={report} onClose={() => setReport(null)} />
      <ConfirmDialog
        open={!!removeTarget}
        title="Remove iptables rule"
        body={removeTarget ? ruleCanonical(removeTarget) : ""}
        confirmLabel="Delete"
        confirmColor="error"
        onConfirm={() => {
          if (removeTarget) remove.mutate(removeTarget);
          setRemoveTarget(null);
        }}
        onCancel={() => setRemoveTarget(null)}
      />
    </Stack>
  );
}

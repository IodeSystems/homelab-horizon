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
import TerminalIcon from "@mui/icons-material/Terminal";
import VerifiedUserIcon from "@mui/icons-material/VerifiedUser";
import RemoveCircleOutlineIcon from "@mui/icons-material/RemoveCircleOutlined";
import RefreshIcon from "@mui/icons-material/Refresh";
import {
  useBlessIPTablesRule,
  useIPTablesRules,
  useReconcileIPTables,
  useServices,
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
  onShowRemoveCommand,
}: {
  rule: IPTablesRule;
  state: IPTablesRuleState;
  onBless: () => void;
  onUnbless: () => void;
  onShowRemoveCommand: () => void;
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
          <Tooltip title="Show the shell command that removes this rule">
            <IconButton size="small" onClick={onShowRemoveCommand}>
              <TerminalIcon fontSize="small" />
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
        <Tooltip title='Removed by "Reconcile now", and by the 60s tick anyway — or show the shell command'>
          <IconButton size="small" color="warning" onClick={onShowRemoveCommand}>
            <TerminalIcon fontSize="small" />
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
    <Stack direction="row" spacing={1} sx={{ flexWrap: "wrap" }}>
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

// removeCommand renders the exact shell line that deletes one rule.
// The same three fields the deleted POST /iptables/remove took from its
// request body — table, chain, args — laid out for a human to run instead.
function removeCommand(rule: IPTablesRule): string {
  return ["sudo iptables", "-t", rule.Table, "-D", rule.Chain, ...rule.Args].join(" ");
}

// RemoveCommandDialog replaces the old delete button.
//
// hz no longer shells `iptables -D` with a table, chain and args taken from a
// request body — that was an authenticated arbitrary-firewall-delete primitive
// (privilege-classification.md §3.3), and no amount of validation narrows it.
// The screen keeps the useful half: it already knows the exact rule, so it
// shows the exact command, and says which rules hz will remove on its own.
function RemoveCommandDialog({
  rule,
  state,
  onClose,
}: {
  rule: IPTablesRule | null;
  state: IPTablesRuleState | null;
  onClose: () => void;
}) {
  return (
    <Dialog open={!!rule} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>Remove this rule at the shell</DialogTitle>
      <DialogContent>
        {state === "stale" ? (
          <DialogContentText sx={{ mb: 2 }}>
            This rule is <strong>stale</strong>, so horizon removes it itself — press{" "}
            <strong>Reconcile now</strong> above, or wait for the 60-second tick. You only
            need the command below if you want it gone before then.
          </DialogContentText>
        ) : (
          <DialogContentText sx={{ mb: 2 }}>
            This rule is <strong>unknown</strong> to horizon, so horizon will never remove
            it — it is not one of ours. If you want it kept, press <strong>Bless</strong> on
            the row instead. If you want it gone, run this on the host:
          </DialogContentText>
        )}
        <Paper
          variant="outlined"
          sx={{ p: 1.5, fontFamily: "monospace", fontSize: "0.85rem", overflowX: "auto" }}
        >
          {rule ? removeCommand(rule) : ""}
        </Paper>
        <DialogContentText variant="caption" sx={{ mt: 2, display: "block" }}>
          Horizon deliberately has no button for this. Deleting an arbitrary firewall rule
          on request is the one privileged action it will not perform on its own behalf.
        </DialogContentText>
      </DialogContent>
      <DialogActions>
        <Button variant="contained" onClick={onClose}>
          Close
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// ForwardsPanel lists the configured layer-4 port forwards, which is what the
// HZ-PREROUTING / HZ-POSTROUTING / HZ-FORWARD rules below are generated from.
// Read-only: forwards are edited on the service.
function ForwardsPanel() {
  const { data: services } = useServices();
  const rows = (services ?? []).flatMap((svc) =>
    (svc.forwards ?? []).map((f) => ({ service: svc.name, ...f })),
  );
  if (rows.length === 0) return null;
  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Port forwards ({rows.length})
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Traffic addressed to this gateway on the port is DNATed to the backend
        (HZ-PREROUTING), masqueraded (HZ-POSTROUTING) and accepted (HZ-FORWARD).
        Edit them on the service.
      </Typography>
      <TableContainer>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Service</TableCell>
              <TableCell>Proto</TableCell>
              <TableCell>Gateway port</TableCell>
              <TableCell>Backend</TableCell>
              <TableCell>Name</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={`${r.proto}-${r.port}`}>
                <TableCell>{r.service}</TableCell>
                <TableCell sx={{ fontFamily: "monospace" }}>{r.proto}</TableCell>
                <TableCell sx={{ fontFamily: "monospace" }}>{r.port}</TableCell>
                <TableCell sx={{ fontFamily: "monospace" }}>{r.backend}</TableCell>
                <TableCell>{r.name || r.description || "—"}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </Paper>
  );
}

export function IPTablesTab() {
  const { data, isLoading, error } = useIPTablesRules();
  const bless = useBlessIPTablesRule();
  const unbless = useUnblessIPTablesRule();
  const reconcile = useReconcileIPTables();

  const [filter, setFilter] = useState<IPTablesRuleState | "all">("all");
  const [report, setReport] = useState<IPTablesReport | null>(null);
  const [removeTarget, setRemoveTarget] = useState<ClassifiedRule | null>(null);

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
          Horizon-managed iptables rules on this host — nat POSTROUTING, filter FORWARD, the
          WG-FORWARD and WG-INPUT chains, the jumps into WG-INPUT and HZ-PREROUTING, and the
          port-forward chains HZ-PREROUTING, HZ-POSTROUTING and HZ-FORWARD. Rules in other
          chains are not shown here. Auto-heal deletes
          <strong> stale </strong> rules and adds missing <strong>expected</strong> rules on every
          60s health-check tick. <strong>Unknown</strong> rules are surfaced for you to bless
          (keep); nothing outside horizon's managed chains is ever auto-touched. Horizon has no
          delete button — a rule it does not own is removed at the shell, and each row will show
          you the exact command.
        </Typography>
      </Paper>

      <ForwardsPanel />

      <Paper variant="outlined" sx={{ p: 2 }}>
        <Stack direction="row" spacing={2} sx={{ alignItems: "center", mb: 2 }}>
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
                      onShowRemoveCommand={() => setRemoveTarget(cr)}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      </Paper>

      <ReportDialog report={report} onClose={() => setReport(null)} />
      <RemoveCommandDialog
        rule={removeTarget?.rule ?? null}
        state={removeTarget?.state ?? null}
        onClose={() => setRemoveTarget(null)}
      />
    </Stack>
  );
}

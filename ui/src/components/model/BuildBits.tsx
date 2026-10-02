/**
 * The build registry on screen: a project's Builds tab, one build's page, the
 * provenance line in the Promote dialog, and the builders in Settings.
 * Server: `internal/server/handlers_api_builds.go`.
 *
 * BUILD NUMBERS COME FROM hz. A builder (a declared machine with a builder
 * token) starts a build; hz allocates its number. Every list here answers in
 * three ways (invariant 2): the rows, an answered empty list, or "cannot ask".
 * Loading is a fourth sentence, not a blank.
 *
 * ABANDONED is the server's word, derived on read: no finish and no log or
 * state activity for 2h. Nothing here computes it.
 *
 * Panels are separate from their `<Dialog>` shells for the reason
 * `SegmentDialogs.tsx` gives: SSR renders nothing for a Portal.
 */
import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  IconButton,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import { Delete as DeleteIcon } from "@mui/icons-material";
import type { ArtifactResp, BuildResp, BuildStepResp, BuilderResp } from "../../api/generated-types";
import { useMintBuilder, useRemoveBuilder } from "../../api/buildHooks";
import { AddButton, EmptyRow, TabHeader } from "./FlowBits";

/** What hz answered: the rows, still asking, or that it could not be asked. */
export type Source<T> =
  | { known: true; rows: T }
  | { known: false; loading: true }
  | { known: false; loading: false; why: string };

export function sourceOf<T>(q: { data?: T; error: Error | null; isLoading: boolean }): Source<T> {
  if (q.data !== undefined) return { known: true, rows: q.data };
  if (q.error) return { known: false, loading: false, why: q.error.message };
  return { known: false, loading: true };
}

/** "3m 12s", "2h 5m", "41s". */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "unknown";
  const s = Math.floor(seconds);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

const RESULT_COLOR: Record<string, "success" | "error" | "warning" | "info"> = {
  passed: "success",
  failed: "error",
  abandoned: "warning",
  running: "info",
};

export function ResultChip({ result, failedStep }: { result: string; failedStep?: string }) {
  const label = result === "failed" && failedStep ? `failed at ${failedStep}` : result;
  return (
    <Chip
      size="small"
      variant="outlined"
      label={label}
      color={RESULT_COLOR[result] ?? "default"}
      data-build-result={result}
      title={result === "abandoned" ? "No finish, and no log or state activity for 2h. Its number is never reused." : undefined}
    />
  );
}

function sha12(s: string): string {
  return s.slice(0, 12);
}

/** A link to hz's page for one build. */
export function BuildNumberLink({ project, number }: { project: string; number: number }) {
  return (
    <Link
      to="/p/$project/builds/$number"
      params={{ project, number: String(number) }}
      data-build-link={number}
      style={{ fontFamily: "monospace" }}
    >
      #{number}
    </Link>
  );
}

function Asking({ what }: { what: string }) {
  return <Typography sx={{ color: "text.secondary" }}>Asking hz for {what}…</Typography>;
}

function CannotAsk({ what, why }: { what: string; why: string }) {
  return (
    <Alert severity="warning" data-cannot-ask>
      hz could not be asked for {what}: {why}
    </Alert>
  );
}

/** A project's builds: number, version, commit, machine, result, duration, artifact. */
export function BuildsTable({
  source,
  emptyText,
  showProject = false,
}: {
  source: Source<BuildResp[]>;
  emptyText: string;
  /** The unscoped list names each row's project. */
  showProject?: boolean;
}) {
  if (!source.known) {
    return source.loading ? <Asking what="its builds" /> : <CannotAsk what="its builds" why={source.why} />;
  }
  if (source.rows.length === 0) return <EmptyRow text={emptyText} />;
  return (
    <TableContainer component={Paper}>
      <Table size="small" aria-label="Builds">
        <TableHead>
          <TableRow>
            {showProject ? <TableCell>Project</TableCell> : null}
            <TableCell>Build</TableCell>
            <TableCell>Version</TableCell>
            <TableCell>Commit</TableCell>
            <TableCell>Machine</TableCell>
            <TableCell>Result</TableCell>
            <TableCell>Duration</TableCell>
            <TableCell>Artifact</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {source.rows.map((b) => (
            <TableRow key={b.id} hover data-build-row={b.number}>
              {showProject ? <TableCell sx={{ fontFamily: "monospace" }}>{b.project}</TableCell> : null}
              <TableCell>
                <BuildNumberLink project={b.project} number={b.number} />
              </TableCell>
              <TableCell sx={{ fontFamily: "monospace" }} title={b.describe || undefined}>
                {b.version}
              </TableCell>
              <TableCell sx={{ fontFamily: "monospace" }} title={b.commit}>
                {b.commit.slice(0, 8)}
              </TableCell>
              <TableCell sx={{ fontFamily: "monospace" }}>{b.machine}</TableCell>
              <TableCell>
                <ResultChip result={b.result} failedStep={b.failed_step} />
              </TableCell>
              <TableCell>{formatDuration(b.duration_seconds)}</TableCell>
              <TableCell sx={{ fontFamily: "monospace" }} title={b.artifact_sha256 || undefined}>
                {b.artifact_sha256 ? sha12(b.artifact_sha256) : "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

/** The steps a build reported, in its order. */
export function BuildSteps({ steps }: { steps: BuildStepResp[] }) {
  if (steps.length === 0) return <EmptyRow text="This build has reported no steps." />;
  return (
    <TableContainer component={Paper} sx={{ mb: 2 }}>
      <Table size="small" aria-label="Steps">
        <TableHead>
          <TableRow>
            <TableCell>Step</TableCell>
            <TableCell>Status</TableCell>
            <TableCell>Started</TableCell>
            <TableCell>Finished</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {steps.map((s, i) => (
            <TableRow key={`${i}-${s.name}`} data-step={s.name}>
              <TableCell sx={{ fontFamily: "monospace" }}>{s.name}</TableCell>
              <TableCell>
                <ResultChip result={s.status} />
              </TableCell>
              <TableCell>{s.started_at ?? "—"}</TableCell>
              <TableCell>{s.finished_at ?? "—"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

/** The log's last bytes, or why there are none. */
export function LogTailView({
  build,
  source,
}: {
  build: BuildResp;
  source: Source<{ text: string; size: number; truncated: boolean }>;
}) {
  if (build.log_deleted_at) {
    return (
      <EmptyRow text={`The log was deleted by retention at ${build.log_deleted_at}: ${build.log_deleted_why ?? ""}`} />
    );
  }
  if (!source.known) {
    return source.loading ? <Asking what="the log" /> : <CannotAsk what="the log" why={source.why} />;
  }
  if (source.rows.size === 0) return <EmptyRow text="This build has sent no log." />;
  const shown = source.rows.text.length;
  return (
    <Box>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1 }} data-log-size={source.rows.size}>
        {shown < source.rows.size ? `The last ${shown} of ${source.rows.size} bytes` : `${source.rows.size} bytes`}
        {build.result === "running" ? " · refreshing while it runs" : ""}
        {source.rows.truncated ? " · truncated at the 20 MiB cap" : ""}
      </Typography>
      <Paper
        variant="outlined"
        component="pre"
        data-log-tail
        sx={{ p: 1.5, m: 0, maxHeight: 480, overflow: "auto", fontFamily: "monospace", fontSize: 12, whiteSpace: "pre-wrap" }}
      >
        {source.rows.text}
      </Paper>
    </Box>
  );
}

/** One build's facts, above its steps and log. */
export function BuildFacts({ build }: { build: BuildResp }) {
  const row = (label: string, value: ReactNode) => (
    <TableRow>
      <TableCell sx={{ width: 160, color: "text.secondary" }}>{label}</TableCell>
      <TableCell sx={{ fontFamily: "monospace" }}>{value}</TableCell>
    </TableRow>
  );
  return (
    <TableContainer component={Paper} sx={{ mb: 2 }}>
      <Table size="small" aria-label="Build">
        <TableBody>
          {row("Result", <ResultChip result={build.result} failedStep={build.failed_step} />)}
          {row("Version", build.version)}
          {row("Commit", build.commit)}
          {build.describe ? row("Describe", build.describe) : null}
          {row("Machine", build.machine)}
          {row("Started", build.started_at)}
          {row(build.finished_at ? "Finished" : "Last activity", build.finished_at ?? build.last_activity_at)}
          {row("Duration", formatDuration(build.duration_seconds))}
          {row("Current step", build.step || "—")}
          {row(
            "Artifact",
            build.artifact_sha256 ? (
              <span data-build-artifact>{build.artifact_sha256}</span>
            ) : build.result === "passed" ? (
              "—"
            ) : (
              "none — only a passed build names an artifact"
            ),
          )}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

/**
 * Where the artifact being promoted came from — the provenance gate's
 * question, answered before the button. `sha` is the source rung's newest
 * report's artifact.
 */
export function ProvenanceLine({ source, sha }: { source: Source<ArtifactResp[]>; sha: string }) {
  if (!source.known) {
    return (
      <Typography variant="body2" data-provenance={source.loading ? "loading" : "unknown"} sx={{ color: "text.secondary", mb: 2 }}>
        {source.loading ? "Asking hz where this artifact came from…" : `hz could not be asked where this artifact came from: ${source.why}`}
      </Typography>
    );
  }
  const a = source.rows.find((x) => x.sha256 === sha);
  if (!a) {
    return (
      <Alert severity="warning" data-provenance="not-uploaded" sx={{ mb: 2 }}>
        Artifact {sha12(sha)} is not uploaded to hz. The promote will be refused.
      </Alert>
    );
  }
  if (a.provenance === "build" && a.build_number) {
    return (
      <Typography variant="body2" data-provenance="build" sx={{ mb: 2 }}>
        Artifact {sha12(sha)} is the artifact of build <BuildNumberLink project={a.project} number={a.build_number} />.
      </Typography>
    );
  }
  if (a.provenance === "no build record") {
    return (
      <Typography variant="body2" data-provenance="grandfathered" sx={{ mb: 2, color: "text.secondary" }}>
        Artifact {sha12(sha)}: no build record (grandfathered — uploaded before hz numbered builds).
      </Typography>
    );
  }
  return (
    <Alert severity="warning" data-provenance="unproven" sx={{ mb: 2 }}>
      Artifact {sha12(sha)} has no build record: no passed build names it. The promote will be refused.
    </Alert>
  );
}

// ---------------------------------------------------------------------------
// Builders (Settings).

/** The builders, each with [Revoke], and [Add builder]. */
export function BuildersSection({
  source,
  machines,
  projects,
}: {
  source: Source<BuilderResp[]>;
  machines: string[];
  projects: string[];
}) {
  const [adding, setAdding] = useState<string | null>(null);
  const remove = useRemoveBuilder();
  const add = <AddButton label="Add builder" onClick={() => setAdding("")} />;
  return (
    <Box data-builders>
      <TabHeader title="Builders" action={add} />
      {!source.known ? (
        source.loading ? (
          <Asking what="its builders" />
        ) : (
          <CannotAsk what="its builders" why={source.why} />
        )
      ) : source.rows.length === 0 ? (
        <EmptyRow text="No builders — build numbers come from hz, and only a builder can start a build." action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small" aria-label="Builders">
            <TableHead>
              <TableRow>
                <TableCell>Machine</TableCell>
                <TableCell>Projects</TableCell>
                <TableCell align="right">Token</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {source.rows.map((b) => (
                <TableRow key={b.machine} data-builder={b.machine}>
                  <TableCell sx={{ fontFamily: "monospace" }}>{b.machine}</TableCell>
                  <TableCell>{b.projects.join(", ")}</TableCell>
                  <TableCell align="right">
                    <Button size="small" sx={{ textTransform: "none" }} onClick={() => setAdding(b.machine)}>
                      Rotate
                    </Button>
                    <IconButton
                      size="small"
                      color="error"
                      aria-label={`Revoke builder ${b.machine}`}
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(b.machine)}
                    >
                      <DeleteIcon fontSize="small" />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      {remove.error ? (
        <Alert severity="error" sx={{ mt: 1 }}>
          {remove.error.message}
        </Alert>
      ) : null}
      <AddBuilderDialog
        open={adding !== null}
        machine={adding ?? ""}
        current={source.known ? source.rows.find((b) => b.machine === adding) : undefined}
        machines={machines}
        projects={projects}
        onClose={() => setAdding(null)}
      />
    </Box>
  );
}

/**
 * The Add builder dialog's contents. The token is shown ONCE, here, in the
 * answer to the mint; hz keeps only its sha256.
 */
export function AddBuilderPanel({
  machines,
  projects,
  initialMachine,
  initialProjects,
  token,
  error,
  pending,
  onMint,
  onClose,
}: {
  machines: string[];
  projects: string[];
  initialMachine: string;
  initialProjects: string[];
  token: string | null;
  error: string | null;
  pending: boolean;
  onMint: (machine: string, projects: string[]) => void;
  onClose: () => void;
}) {
  const [machine, setMachine] = useState(initialMachine);
  const [chosen, setChosen] = useState<string[]>(initialProjects);
  return (
    <>
      <DialogTitle>{initialMachine ? `Rotate builder ${initialMachine}` : "Add builder"}</DialogTitle>
      <DialogContent>
        {token ? (
          <Box data-builder-token>
            <Alert severity="success" sx={{ mb: 2 }}>
              Builder token for {machine}. Shown once; hz keeps only its sha256.
            </Alert>
            <TextField fullWidth value={token} slotProps={{ input: { readOnly: true } }} sx={{ "& input": { fontFamily: "monospace" } }} />
          </Box>
        ) : (
          <>
            <FormControl fullWidth sx={{ mt: 1, mb: 2 }} disabled={initialMachine !== ""}>
              <InputLabel id="builder-machine">Machine</InputLabel>
              <Select labelId="builder-machine" label="Machine" value={machine} onChange={(e) => setMachine(e.target.value)}>
                {machines.map((m) => (
                  <MenuItem key={m} value={m}>
                    {m}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
            <FormControl fullWidth>
              <InputLabel id="builder-projects">Projects</InputLabel>
              <Select
                labelId="builder-projects"
                label="Projects"
                multiple
                value={chosen}
                onChange={(e) => setChosen(typeof e.target.value === "string" ? e.target.value.split(",") : e.target.value)}
              >
                {projects.map((p) => (
                  <MenuItem key={p} value={p}>
                    {p}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
            {initialMachine ? (
              <Typography variant="body2" sx={{ color: "warning.main", mt: 2 }}>
                Rotating replaces the token: the old one stops working.
              </Typography>
            ) : null}
          </>
        )}
        {error ? (
          <Alert severity="error" sx={{ mt: 2 }} data-builder-refusal>
            {error}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>{token ? "Done" : "Cancel"}</Button>
        {token ? null : (
          <Button variant="contained" disabled={machine === "" || chosen.length === 0 || pending} onClick={() => onMint(machine, chosen)}>
            {pending ? "Minting…" : "Mint builder token"}
          </Button>
        )}
      </DialogActions>
    </>
  );
}

function AddBuilderDialog({
  open,
  machine,
  current,
  machines,
  projects,
  onClose,
}: {
  open: boolean;
  machine: string;
  current: BuilderResp | undefined;
  machines: string[];
  projects: string[];
  onClose: () => void;
}) {
  const mint = useMintBuilder();
  const close = () => {
    mint.reset();
    onClose();
  };
  return (
    <Dialog open={open} onClose={close} fullWidth maxWidth="sm">
      {open ? (
        <AddBuilderPanel
          key={machine}
          machines={machines}
          projects={projects}
          initialMachine={machine}
          initialProjects={current?.projects ?? []}
          token={mint.data?.token ?? null}
          error={mint.error?.message ?? null}
          pending={mint.isPending}
          onMint={(m, ps) => mint.mutate({ machine: m, projects: ps })}
          onClose={close}
        />
      ) : null}
    </Dialog>
  );
}

/**
 * Release lines on a project's Overview, and the restore tests a promotion
 * needs (plan/plan.md "Versions, lines and the restore test"). Server:
 * `internal/server/handlers_api_lines.go`, `GET /api/v1/projects/lines`.
 *
 * A version's LINE is its MAJOR.MINOR.PATCH (1.9.0-1.2 is on 1.9.0). A rung
 * supports its declared line (current), the most recent different line
 * promoted into it (prior), and the project's pins. The server derives these;
 * this file only renders them.
 *
 * EVERY CELL HAS AN ANSWER, never a blank (invariant 2): "no kept backup" is an
 * answer, "none required" is an answer, and "hz cannot say" (a gap, a failed
 * read) reads differently from both. Empty states are one line (amendment 6,
 * `FlowBits.tsx`).
 */
import {
  Alert,
  Box,
  Link as MuiLink,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import type {
  KeptBackupResp,
  ProjectLinesResp,
  RungLinesResp,
  SupportedLineResp,
} from "../../api/generated-types";
import { formatAge } from "../drift/observation.ts";
import { EmptyRow } from "./FlowBits";

/**
 * A build_url: a link only when it is http(s); any other scheme (an R0 bucket
 * path) is shown as text, because a browser cannot open it and a `gs://` href
 * would be a dead link that looks live.
 */
export function BuildLink({ url }: { url: string }) {
  if (url === "") return null;
  if (/^https?:\/\//i.test(url)) {
    return (
      <MuiLink href={url} target="_blank" rel="noreferrer" data-build-url="link" sx={{ fontFamily: "monospace" }}>
        build
      </MuiLink>
    );
  }
  return (
    <Typography component="span" variant="body2" data-build-url="text" sx={{ fontFamily: "monospace" }} title="build_url">
      {url}
    </Typography>
  );
}

function sha12(s: string): string {
  return s.slice(0, 12);
}

function whyText(line: SupportedLineResp): string {
  return line.why.map((w) => (w.kind === "pinned" ? `pinned: ${w.detail}` : w.kind)).join(" · ");
}

function whyTitle(line: SupportedLineResp): string {
  return line.why.map((w) => `${w.kind}: ${w.detail}`).join("\n");
}

function KeptCell({ kept }: { kept: KeptBackupResp | null | undefined }) {
  if (!kept) {
    return (
      <Typography component="span" variant="body2" data-kept="none" sx={{ color: "warning.main" }}>
        no kept backup
      </Typography>
    );
  }
  return (
    <Typography
      component="span"
      variant="body2"
      data-kept="kept"
      sx={{ fontFamily: "monospace" }}
      title={`sha256 ${kept.backup_sha256} · taken by ${kept.taken_by_version} · recorded by ${kept.recorded_by} at ${kept.recorded_at}`}
    >
      {sha12(kept.backup_sha256)} · {kept.location} · {formatAge(kept.age_seconds)} ago
    </Typography>
  );
}

/** The restore-test status of one line, in a few words; the sentence is its title. */
function RestoreCell({ line, from }: { line: SupportedLineResp; from: string }) {
  const r = line.restore;
  const tone =
    r.status === "passed" ? "success.main" : r.status === "no-source" ? "text.secondary" : "warning.main";
  let words: string;
  switch (r.status) {
    case "passed":
      words = `${r.version} passed${r.test ? ` (#${r.test.id})` : ""}`;
      break;
    case "failed":
      words = `${r.version} failed`;
      break;
    case "missing":
      words = `${r.version}: no restore test`;
      break;
    case "superseded":
      words = `${r.version}: tested a superseded backup`;
      break;
    case "no-kept-backup":
      words = "needs a kept backup first";
      break;
    case "no-source-report":
      words = `${from} reported nothing`;
      break;
    case "no-source":
      words = "not asked — no from";
      break;
    default:
      words = r.status;
  }
  return (
    <Typography component="span" variant="body2" data-restore={r.status} sx={{ color: tone }} title={r.sentence}>
      {words}
      {r.test && r.test.build_url ? (
        <>
          {" "}
          <BuildLink url={r.test.build_url} />
        </>
      ) : null}
    </Typography>
  );
}

function RungRows({ project, rung }: { project: string; rung: RungLinesResp }) {
  const name = (
    <TableCell sx={{ fontFamily: "monospace", fontWeight: 700 }}>
      {project}/{rung.environment}
    </TableCell>
  );
  const rows: React.ReactElement[] = [];
  for (const [i, gap] of rung.gaps.entries()) {
    rows.push(
      <TableRow key={`gap-${i}`} data-lines-row="gap">
        {name}
        <TableCell colSpan={4}>
          <Typography component="span" variant="body2" sx={{ color: "warning.main" }}>
            hz cannot say: {gap}
          </Typography>
        </TableCell>
      </TableRow>,
    );
  }
  if (rung.none_required) {
    rows.push(
      <TableRow key="none" data-lines-row="none-required">
        {name}
        <TableCell colSpan={4}>
          <Typography component="span" variant="body2" sx={{ color: "text.secondary" }}>
            {rung.none_required}
          </Typography>
        </TableCell>
      </TableRow>,
    );
  }
  for (const line of rung.supported) {
    rows.push(
      <TableRow key={line.line} hover data-lines-row="line">
        {name}
        <TableCell sx={{ fontFamily: "monospace" }}>{line.line}</TableCell>
        <TableCell title={whyTitle(line)}>{whyText(line)}</TableCell>
        <TableCell>
          <KeptCell kept={line.kept_backup} />
        </TableCell>
        <TableCell>
          <RestoreCell line={line} from={rung.from} />
        </TableCell>
      </TableRow>,
    );
  }
  return <>{rows}</>;
}

/** The Overview's "Release lines" section. */
export function ReleaseLinesTable({
  lines,
  error,
  loading,
  project,
}: {
  lines: ProjectLinesResp | undefined;
  error: Error | null;
  loading: boolean;
  project: string;
}) {
  if (error) {
    return (
      <Alert severity="warning" sx={{ mb: 3 }} data-lines="unknown">
        hz could not be asked for the release lines: {error.message}
      </Alert>
    );
  }
  if (loading || lines === undefined) {
    return (
      <Typography sx={{ color: "text.secondary", mb: 3 }} data-lines="loading">
        Asking hz for the release lines…
      </Typography>
    );
  }
  const anything = lines.rungs.some((r) => r.supported.length > 0 || r.gaps.length > 0);
  return (
    <Box sx={{ mb: 3 }}>
      {!anything ? (
        <Box data-lines="none">
          <EmptyRow
            text={`No supported release lines in ${project} — nothing has been promoted yet, so the next promotion requires no restore test.`}
          />
        </Box>
      ) : (
        <TableContainer component={Paper} data-lines="table">
          <Table size="small" aria-label="Release lines">
            <TableHead>
              <TableRow>
                <TableCell>Rung</TableCell>
                <TableCell>Line</TableCell>
                <TableCell>Why</TableCell>
                <TableCell>Kept backup</TableCell>
                <TableCell>Restore test (next promotion)</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {lines.rungs.map((r) => (
                <RungRows key={r.environment} project={project} rung={r} />
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      <RetiredLine lines={lines} />
    </Box>
  );
}

function RetiredLine({ lines }: { lines: ProjectLinesResp }) {
  if (lines.retired_unknown) {
    return (
      <Typography variant="body2" data-retired="unknown" sx={{ color: "warning.main", mt: 1 }}>
        Retired lines unknown — {lines.retired_unknown}
      </Typography>
    );
  }
  if (lines.retired.length === 0) {
    return (
      <Typography variant="body2" data-retired="none" sx={{ color: "text.secondary", mt: 1 }}>
        No retired lines.
      </Typography>
    );
  }
  return (
    <Typography variant="body2" data-retired="some" sx={{ color: "text.secondary", mt: 1 }}>
      Retired (a kept backup, supported on no rung — the app may delete it; hz deletes nothing):{" "}
      {lines.retired.map((b, i) => (
        <span key={b.line} style={{ fontFamily: "monospace" }} title={`sha256 ${b.backup_sha256}`}>
          {i > 0 ? ", " : ""}
          {b.line} {b.location}
        </span>
      ))}
    </Typography>
  );
}

/**
 * What the Promote dialog shows before its button: the restore tests the
 * TARGET rung's supported lines require. The server is the gate — this is the
 * same derivation, read ahead, so the operator sees the refusal coming.
 */
export type LinesSource =
  | { known: true; lines: ProjectLinesResp }
  | { known: false; loading: true }
  | { known: false; loading: false; why: string };

export function RequiredRestoreTests({
  source,
  project,
  target,
  version,
}: {
  source: LinesSource;
  project: string;
  target: string;
  /** The version in the dialog's field. */
  version: string;
}) {
  if (!source.known && source.loading) {
    return (
      <Typography variant="body2" data-required="loading" sx={{ color: "text.secondary", mb: 1 }}>
        Asking hz which restore tests this promotion needs…
      </Typography>
    );
  }
  if (!source.known) {
    return (
      <Alert severity="warning" data-required="unknown" sx={{ mb: 1 }}>
        hz could not be asked which restore tests this promotion needs ({source.why}). The promote still checks them.
      </Alert>
    );
  }
  const rung = source.lines.rungs.find((r) => r.environment === target);
  if (!rung) {
    return (
      <Alert severity="warning" data-required="unknown" sx={{ mb: 1 }}>
        hz's release lines do not list {project}/{target}. The promote still checks them.
      </Alert>
    );
  }
  if (rung.none_required) {
    return (
      <Typography variant="body2" data-required="none" sx={{ color: "text.secondary", mb: 1 }}>
        Restore tests: {rung.none_required}.
      </Typography>
    );
  }
  const shownFor = rung.supported.find((l) => l.restore.version !== "")?.restore.version ?? "";
  return (
    <Box data-required="list" sx={{ mb: 1 }}>
      <Typography variant="body2" sx={{ fontWeight: 700 }}>
        Required restore tests — every line {project}/{target} supports now
      </Typography>
      {rung.gaps.map((g, i) => (
        <Typography key={`gap-${i}`} variant="body2" data-required-line="gap" sx={{ color: "warning.main" }}>
          hz cannot say: {g}
        </Typography>
      ))}
      {rung.supported.map((l) => (
        <Typography
          key={l.line}
          variant="body2"
          data-required-line={l.restore.status}
          sx={{ color: l.restore.status === "passed" ? "success.main" : "warning.main" }}
        >
          <code>{l.line}</code> ({whyText(l)}): {l.restore.sentence}
        </Typography>
      ))}
      {shownFor !== "" && version.trim() !== "" && version.trim() !== shownFor ? (
        <Typography variant="body2" data-required-for="other" sx={{ color: "text.secondary" }}>
          Shown for {shownFor}, the source rung's newest report — not {version.trim()}.
        </Typography>
      ) : null}
    </Box>
  );
}

/**
 * The outside-in diagnosis panel: what is wrong with each public name, whose
 * device has to change, and the change.
 *
 * Every other thing on the Checks page reports a STATUS. This reports a CAUSE
 * and an INSTRUCTION, which is a different job — a refused HTTPS probe is a
 * red row whether the name points at the wrong address, the router forgot the
 * port forward, or the certificate expired, and those are three trips to three
 * different boxes.
 *
 * Three treatments are load-bearing and are not decoration:
 *
 *   NO ACTION EXISTS FOR A DEVICE HZ CANNOT REACH. `hzCanFix` is false for
 *   every verdict whose fix lives on the router or at a DNS provider, and this
 *   file renders no button at all. hz declares and observes; it never reaches
 *   into a machine, and the router is the extreme case. A greyed-out button
 *   would be worse than none — it reads as a capability someone else has.
 *   `EdgeDiagnosis.selftest.tsx` counts the buttons.
 *
 *   UNKNOWN IS NOT A SHADE OF OK. A name no vantage has reported on is HATCHED
 *   and sorts with the faults, not with the passes. The same rule the drift
 *   screen keeps, for the same reason: if silence rendered like health, an
 *   operator would learn to read "nobody looked" as "fine".
 *
 *   THE CONFIRMATION IS PART OF THE INSTRUCTION. Somebody following the router
 *   fix is standing at a router admin page, not at this screen, so the way to
 *   check their work has to travel with them.
 */
import {
  Alert,
  Box,
  Card,
  CardContent,
  Chip,
  Typography,
} from "@mui/material";
import RouterIcon from "@mui/icons-material/RouterOutlined";
import DnsIcon from "@mui/icons-material/DnsOutlined";
import StorageIcon from "@mui/icons-material/StorageOutlined";
import HelpIcon from "@mui/icons-material/HelpOutlineOutlined";
import LanguageIcon from "@mui/icons-material/LanguageOutlined";
import type { ProbeDiagnosis } from "../api/types";
import { useProbeDiagnosis } from "../api/hooks";

/** Status keys the server sends. "unknown" is a fourth state, not a shade. */
type Tone = "ok" | "warning" | "failed" | "unknown";

function toneOf(status: string): Tone {
  if (status === "ok" || status === "warning" || status === "failed") return status;
  return "unknown";
}

export function toneColor(tone: Tone): string {
  switch (tone) {
    case "ok":
      return "success.main";
    case "warning":
      return "warning.main";
    case "failed":
      return "error.main";
    case "unknown":
      return "text.secondary";
  }
}

/** Diagonal hatch: the texture that means *nobody looked*. Never a colour. */
const hatchSx = {
  backgroundImage:
    "repeating-linear-gradient(45deg, transparent, transparent 4px, rgba(255,255,255,0.12) 4px, rgba(255,255,255,0.12) 8px)",
} as const;

/**
 * Whose box. The label is the whole point of the panel, so it is spelled out
 * rather than left to an icon.
 */
export function deviceLabel(device: string): string {
  switch (device) {
    case "router":
      return "the router — hz cannot change this";
    case "dns":
      return "the DNS provider — hz cannot change this";
    case "hz":
      return "hz (this box)";
    case "backend":
      return "the service behind hz";
    case "vantage":
      return "the outside vantage";
    default:
      return "not yet attributed";
  }
}

function DeviceIcon({ device }: { device: string }) {
  switch (device) {
    case "router":
      return <RouterIcon fontSize="small" />;
    case "dns":
      return <DnsIcon fontSize="small" />;
    case "hz":
    case "backend":
      return <StorageIcon fontSize="small" />;
    default:
      return <HelpIcon fontSize="small" />;
  }
}

/**
 * Sort order is triage order: faults, then warnings, then the names nobody has
 * looked at, then the ones that pass. Unknown sits ABOVE ok deliberately —
 * it is a gap in coverage, and burying it under the healthy rows is how a gap
 * becomes permanent.
 */
const rank: Record<Tone, number> = { failed: 0, warning: 1, unknown: 2, ok: 3 };

export function sortForTriage(rows: ProbeDiagnosis[]): ProbeDiagnosis[] {
  return [...rows].sort((a, b) => {
    const byTone = rank[toneOf(a.status)] - rank[toneOf(b.status)];
    if (byTone !== 0) return byTone;
    if (a.host !== b.host) return a.host < b.host ? -1 : 1;
    return a.vantage < b.vantage ? -1 : 1;
  });
}

/** One verdict, rendered in full. Nothing here is behind a hover. */
export function DiagnosisRow({ row }: { row: ProbeDiagnosis }) {
  const tone = toneOf(row.status);
  const hatched = tone === "unknown";

  return (
    <Box
      sx={{
        py: 1.5,
        borderTop: 1,
        borderColor: "divider",
      }}
    >
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
        <LanguageIcon fontSize="small" color="action" />
        <Typography variant="body2" sx={{ fontWeight: 600, fontFamily: "monospace" }}>
          {row.host}
        </Typography>
        <Chip
          size="small"
          label={row.cause}
          sx={{
            height: 20,
            fontSize: "0.7rem",
            fontWeight: 700,
            letterSpacing: 0.4,
            color: toneColor(tone),
            borderColor: toneColor(tone),
            border: "1px solid",
            bgcolor: "transparent",
            ...(hatched ? hatchSx : {}),
          }}
        />
        <Chip
          size="small"
          variant="outlined"
          icon={<DeviceIcon device={row.device} />}
          label={deviceLabel(row.device)}
          sx={{ height: 22, fontSize: "0.7rem" }}
        />
        <Chip
          size="small"
          variant="outlined"
          label={`seen from ${row.vantage}`}
          sx={{ height: 20, fontSize: "0.7rem" }}
        />
      </Box>

      <Typography variant="body2" sx={{ mt: 0.75 }}>
        {row.summary}
      </Typography>

      {/* The instruction. Body text, not a tooltip: somebody reading this at
          3am should not have to discover it by hovering. */}
      {row.fix && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          <strong>What to change:</strong> {row.fix}
        </Typography>
      )}

      {row.confirm && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          <strong>How to know it worked:</strong> {row.confirm}
        </Typography>
      )}

      {row.evidence.length > 0 && (
        <Box sx={{ mt: 0.75 }}>
          <Typography variant="caption" color="text.secondary">
            What the vantage saw:
          </Typography>
          {row.evidence.map((line) => (
            <Typography
              key={line}
              variant="caption"
              sx={{
                display: "block",
                fontFamily: "monospace",
                fontSize: "0.7rem",
                color: "text.secondary",
                wordBreak: "break-all",
              }}
            >
              {line}
            </Typography>
          ))}
        </Box>
      )}
    </Box>
  );
}

/**
 * The panel.
 *
 * Every state is spelled out: loading, error, no vantage configured, nothing
 * to report. A blank area the operator has to interpret is the failure mode
 * this whole screen exists to remove.
 */
export function EdgeDiagnosis() {
  const { data, isLoading, error } = useProbeDiagnosis();

  const header = (
    <Box>
      <Typography variant="h6" sx={{ fontWeight: 600 }}>
        What is wrong at the edge
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: 720 }}>
        The rows above say whether a probe passed. These say <em>why</em> it did not,
        and <strong>which device has to change</strong>. hz can fix its own
        certificates and its own proxy; it cannot reach the router or the DNS
        provider, so for those the instruction is the whole of what hz can give you.
      </Typography>
    </Box>
  );

  if (isLoading) {
    return (
      <Card variant="outlined" sx={{ mb: 3 }}>
        <CardContent>
          {header}
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            Working out what each name's results add up to…
          </Typography>
        </CardContent>
      </Card>
    );
  }

  if (error) {
    return (
      <Card variant="outlined" sx={{ mb: 3 }}>
        <CardContent>
          {header}
          <Alert severity="error" sx={{ mt: 2 }}>
            Could not load the edge diagnosis: {error.message}. The check rows above are
            unaffected — this panel is the only thing missing.
          </Alert>
        </CardContent>
      </Card>
    );
  }

  const vantages = data?.vantages ?? 0;
  const rows = sortForTriage(data?.diagnoses ?? []);
  const needsAttention = rows.filter((r) => r.status !== "ok");

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      <CardContent>
        {header}

        {/* Zero vantages is its own answer. An empty list of problems normally
            reads as health; here it means hz is not looking from outside at
            all, and saying nothing would be the founding bug again. */}
        {vantages === 0 ? (
          <Alert severity="info" sx={{ mt: 2 }}>
            No outside vantage is configured, so hz has no reading of what the internet
            sees. Nothing here is evidence that the edge works — add a vantage in the
            panel below and hz can start answering this.
          </Alert>
        ) : needsAttention.length === 0 ? (
          <Alert severity="success" sx={{ mt: 2 }}>
            Every public name {vantages === 1 ? "the vantage" : "the vantages"} watch
            {vantages === 1 ? "es" : ""} resolved, connected and answered on the last
            report. {rows.length} name{rows.length === 1 ? "" : "s"} checked.
          </Alert>
        ) : (
          <Box sx={{ mt: 1 }}>
            {needsAttention.map((row) => (
              <DiagnosisRow key={`${row.vantage}:${row.host}`} row={row} />
            ))}
          </Box>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * One machine's card on the drift screen: desired minus observed, before
 * anything is applied.
 *
 * There is NO APPLY BUTTON here, and that is not an omission
 * (plan/design/ui.md). hz publishes; the agent pulls. hz never reaches into
 * a machine, and the agent ships inert. The existing `SyncButton` applies to
 * the four subsystems hz owns on its OWN box and must never be confused for
 * this — which is why this card carries its own standing note saying so.
 */
import { useState } from "react";
import {
  Box,
  Button,
  Chip,
  Divider,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import type { AgentObservation, AgentIPTablesRule } from "../../api/generated-types";
import {
  changeSummaryLine,
  countChanges,
  formatAge,
  groupBySubsystem,
  presentObservation,
  readChangeKind,
  readFirewall,
  readGeneration,
  type Ranked,
} from "./observation";
import { MemoryBanner, ObservedValue, StateBadge, hatchSx, toneColor } from "./Observation";

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <Typography
      variant="subtitle2"
      sx={{ mt: 3, mb: 1, fontWeight: 700, letterSpacing: 0.4, textTransform: "uppercase" }}
    >
      {children}
    </Typography>
  );
}

/** The classification chip the IPTables tab already uses, kept identical. */
function ruleStateColor(state: string): "success" | "warning" | "info" | "error" | "default" {
  switch (state) {
    case "expected":
      return "success";
    case "stale":
      return "warning";
    case "blessed":
      return "info";
    case "unknown":
      return "error";
    default:
      return "default";
  }
}

/**
 * The generation pair, rendered large as a pair.
 *
 * A single "out of date" badge merges four genuinely different situations.
 * Both halves are always on screen, in `observed → desired` order, with the
 * verdict spelled out underneath in a sentence rather than a colour.
 */
function GenerationPair({ row }: { row: AgentObservation }) {
  const g = readGeneration(row);
  const p = presentObservation(row);
  return (
    <Box
      sx={{
        p: 2,
        borderRadius: 1,
        border: "1px solid",
        borderColor: toneColor(g.tone),
        bgcolor: "rgba(255,255,255,0.02)",
      }}
    >
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
        generation — what the machine planned against → what hz would serve it now
      </Typography>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1.5, flexWrap: "wrap", my: 1 }}>
        <Box
          component="span"
          title={row.generation || "no generation reported"}
          sx={{
            fontFamily: "monospace",
            fontSize: "1.35rem",
            color: g.observed ? "text.primary" : "text.secondary",
            opacity: p.dimmed ? 0.7 : 1,
            px: p.hatched && g.observed ? 0.75 : 0,
            borderRadius: 0.5,
            ...(p.hatched && g.observed ? hatchSx : {}),
          }}
        >
          {g.observed ?? "none reported"}
        </Box>
        <Box component="span" sx={{ fontSize: "1.35rem", color: "text.secondary" }}>
          →
        </Box>
        <Box
          component="span"
          title={row.desiredGeneration || "hz holds no desired state for this machine"}
          sx={{
            fontFamily: "monospace",
            fontSize: "1.35rem",
            color: g.desired ? "text.primary" : "text.secondary",
          }}
        >
          {g.desired ?? "hz has none"}
        </Box>
        <Chip
          size="small"
          label={g.headline}
          sx={{
            fontWeight: 700,
            color: toneColor(g.tone),
            borderColor: toneColor(g.tone),
            border: "1px solid",
            bgcolor: "transparent",
          }}
        />
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        {g.meaning}
      </Typography>
    </Box>
  );
}

/**
 * The plan the machine computed, sectioned by subsystem.
 *
 * No-ops are rendered, at reduced opacity, rather than filtered out — a screen
 * that hides them implies work on every poll. `remove` is the one kind that
 * destroys something and it is marked as such in three ways: its own mark, its
 * own colour, and a line naming the consequence.
 */
function Changes({ row }: { row: AgentObservation }) {
  const counts = countChanges(row.changes);
  const groups = groupBySubsystem(row.changes);
  const p = presentObservation(row);

  if (row.changes.length === 0) {
    return (
      <Box>
        <SectionHeading>Changes</SectionHeading>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          {p.state === "nothing-to-report"
            ? "No rows, because hz manages nothing on this machine. There is no desired state to compare against, so an empty plan here is the correct and permanent answer."
            : p.state === "silent"
              ? "No rows, because nothing has ever been reported. This is an absence, not an empty plan."
              : "The machine reported an empty plan: hz has targets here and the agent found nothing it would write."}
        </Typography>
      </Box>
    );
  }

  return (
    <Box>
      <SectionHeading>Changes</SectionHeading>
      <Box sx={{ display: "flex", alignItems: "baseline", gap: 2, flexWrap: "wrap", mb: 1 }}>
        <Typography sx={{ fontFamily: "monospace", fontWeight: 700 }}>
          {changeSummaryLine(counts)}
        </Typography>
        <Typography variant="caption" sx={{ color: "text.secondary" }}>
          What the machine would write if it were applying. It is not applying.
        </Typography>
      </Box>
      {counts.remove > 0 && (
        <Box
          sx={{
            p: 1.5,
            mb: 2,
            borderRadius: 1,
            border: "1px solid",
            borderColor: "error.main",
          }}
        >
          <Typography variant="body2">
            <strong>
              {counts.remove} {counts.remove === 1 ? "removal" : "removals"}.
            </strong>{" "}
            A removal deletes a file inside a directory hz claims, because hz has
            stopped listing it — nothing in the desired state names it any more.
            It is the only kind here that destroys something.
          </Typography>
        </Box>
      )}
      {groups.map((g) => (
        <Box key={g.subsystem} sx={{ mb: 2 }}>
          <Typography variant="caption" sx={{ color: "text.secondary", fontWeight: 700 }}>
            {g.subsystem}
          </Typography>
          {g.changes.map((ch, i) => {
            const k = readChangeKind(ch.kind);
            return (
              <Box
                key={`${ch.subsystem}-${ch.target}-${i}`}
                sx={{
                  display: "flex",
                  alignItems: "flex-start",
                  gap: 1.5,
                  py: 0.75,
                  borderBottom: "1px solid rgba(255,255,255,0.06)",
                  // A no-op must visibly be a no-op.
                  opacity: k.noop ? 0.45 : 1,
                }}
              >
                <Box
                  component="span"
                  sx={{
                    fontFamily: "monospace",
                    fontWeight: 700,
                    fontSize: "1.1rem",
                    width: 20,
                    textAlign: "center",
                    color: toneColor(k.tone),
                    flexShrink: 0,
                  }}
                >
                  {k.symbol}
                </Box>
                <Chip
                  size="small"
                  label={k.label}
                  sx={{
                    minWidth: 96,
                    fontWeight: k.destructive ? 700 : 400,
                    color: toneColor(k.tone),
                    borderColor: toneColor(k.tone),
                    border: "1px solid",
                    bgcolor: "transparent",
                    flexShrink: 0,
                    ...(k.kind === "unknown" ? hatchSx : {}),
                  }}
                />
                <Box sx={{ minWidth: 0 }}>
                  <Typography
                    sx={{
                      fontFamily: "monospace",
                      wordBreak: "break-all",
                      textDecoration: k.destructive ? "line-through" : "none",
                    }}
                  >
                    {ch.target}
                  </Typography>
                  <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
                    {k.meaning}
                  </Typography>
                  {ch.detail && (
                    <Typography
                      variant="caption"
                      sx={{ color: "text.secondary", display: "block", fontFamily: "monospace" }}
                    >
                      {ch.detail}
                    </Typography>
                  )}
                </Box>
              </Box>
            );
          })}
        </Box>
      ))}
    </Box>
  );
}

/**
 * The firewall as the machine read it.
 *
 * `readable: false` must render as "hz cannot look", with the agent's own
 * sentence. "I cannot look" and "there is nothing there" are the two answers
 * that flag exists to keep apart, and an absent section is a third thing again:
 * hz has no opinion about this machine's firewall.
 */
function Firewall({ row }: { row: AgentObservation }) {
  const [showRules, setShowRules] = useState(false);
  const f = readFirewall(row.iptables);
  const ipt = row.iptables;

  return (
    <Box>
      <SectionHeading>Firewall</SectionHeading>
      <Box
        sx={{
          p: 1.5,
          borderRadius: 1,
          border: "1px solid",
          borderColor: toneColor(f.tone),
          ...(f.kind === "unreadable" ? hatchSx : {}),
        }}
      >
        <Typography sx={{ fontWeight: 700 }}>{f.headline}</Typography>
        <Typography variant="body2" sx={{ color: "text.secondary", mt: 0.5 }}>
          {f.meaning}
        </Typography>
        {f.kind === "unreadable" && (
          <Typography variant="body2" sx={{ color: "text.secondary", mt: 1, fontStyle: "italic" }}>
            This is not an empty firewall. hz has no reading at all here, so
            nothing below can be taken as a clean result.
          </Typography>
        )}
      </Box>

      {ipt && ipt.readable && ipt.rules.length > 0 && (
        <Box sx={{ mt: 1.5 }}>
          <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap", mb: 1 }}>
            <Chip size="small" color="success" variant="outlined" label={`expected ${ipt.summary.expected}`} />
            <Chip size="small" color="warning" variant="outlined" label={`stale ${ipt.summary.stale}`} />
            <Chip size="small" color="info" variant="outlined" label={`blessed ${ipt.summary.blessed}`} />
            <Chip size="small" color="error" variant="outlined" label={`unknown ${ipt.summary.unknown}`} />
          </Box>
          <Button size="small" variant="outlined" onClick={() => setShowRules((v) => !v)}>
            {showRules ? "Hide the live rules" : `Show the ${ipt.rules.length} live rules`}
          </Button>
          {showRules && (
            <TableContainer component={Paper} variant="outlined" sx={{ mt: 1 }}>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Table</TableCell>
                    <TableCell>Chain</TableCell>
                    <TableCell>Rule</TableCell>
                    <TableCell>hz says</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {ipt.rules.map((r: AgentIPTablesRule, i: number) => (
                    <TableRow key={`${r.canonical}-${i}`}>
                      <TableCell sx={{ fontFamily: "monospace" }}>{r.table}</TableCell>
                      <TableCell sx={{ fontFamily: "monospace" }}>{r.chain}</TableCell>
                      <TableCell sx={{ fontFamily: "monospace", wordBreak: "break-all" }}>
                        {r.display}
                      </TableCell>
                      <TableCell>
                        <Chip
                          size="small"
                          variant="outlined"
                          color={ruleStateColor(r.state)}
                          label={r.state}
                        />
                        {r.reason && (
                          <Typography variant="caption" sx={{ display: "block", color: "text.secondary" }}>
                            {r.reason}
                          </Typography>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </Box>
      )}
    </Box>
  );
}

export function MachineDrift({ row, ranked }: { row: AgentObservation; ranked: Ranked }) {
  const p = presentObservation(row);

  return (
    <Paper sx={{ p: 2.5, mb: 2 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap" }}>
        <Typography sx={{ fontFamily: "monospace", fontSize: "1.25rem", fontWeight: 700 }}>
          {row.machine}
        </Typography>
        <StateBadge presentation={p} ageSeconds={row.ageSeconds} />
        {row.agentVersion ? (
          <Chip size="small" variant="outlined" label={`agent ${row.agentVersion}`} />
        ) : (
          <Chip size="small" variant="outlined" label="agent version not reported" />
        )}
        {row.applying && (
          <Chip size="small" color="error" label="running with --apply" sx={{ fontWeight: 700 }} />
        )}
        {!row.enrolled && (
          <Chip size="small" color="warning" variant="outlined" label="no credential — history only" />
        )}
        {row.truncated && (
          <Chip size="small" color="warning" variant="outlined" label="report truncated by hz" />
        )}
      </Box>

      <Typography variant="body2" sx={{ color: "text.secondary", mt: 1 }}>
        {p.meaning}
      </Typography>

      {ranked.reasons.length > 0 && (
        <Box component="ul" sx={{ pl: 2.5, my: 1.5 }}>
          {ranked.reasons.map((r, i) => (
            <Typography component="li" variant="body2" key={i} sx={{ mb: 0.5 }}>
              {r}
            </Typography>
          ))}
        </Box>
      )}

      {row.sameForSeconds > 0 && (
        <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1.5 }}>
          This condition has held for {formatAge(row.sameForSeconds)} — the report has said
          the same thing that long.
        </Typography>
      )}

      {/* One banner over the whole observed side, not a chip per row. */}
      {p.hasReading && p.dimmed && <MemoryBanner ageSeconds={row.ageSeconds} />}

      <GenerationPair row={row} />

      <Box sx={{ display: "flex", gap: 3, flexWrap: "wrap", mt: 2 }}>
        <ObservedValue
          label="in sync with the generation it planned against"
          value={p.hasReading ? (row.inSync ? "yes" : "no") : "—"}
          presentation={p}
          ageSeconds={row.ageSeconds}
        />
        <ObservedValue
          label="outstanding changes"
          value={p.hasReading ? String(row.pending) : "—"}
          presentation={p}
          ageSeconds={row.ageSeconds}
        />
        <ObservedValue
          label="targets the agent could not read"
          value={p.hasReading ? String(row.unknown) : "—"}
          presentation={p}
          ageSeconds={row.ageSeconds}
        />
      </Box>

      <Divider sx={{ my: 2 }} />
      <Changes row={row} />
      <Firewall row={row} />

      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mt: 2.5 }}>
        Nothing on this card applies anything. hz publishes a generation and the
        agent collects it on its own poll; hz never reaches into a machine. (The
        Sync control elsewhere in this app is a different thing entirely — it
        applies to the four subsystems hz owns on its own box.)
      </Typography>
    </Paper>
  );
}

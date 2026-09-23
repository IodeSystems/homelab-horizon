/**
 * One machine's projection — `project(global, machine)`, rendered.
 *
 * What hz says this box should look like, computed from hz's own records
 * alone. The gateway goes through this exactly as every other machine does;
 * there is no special case here because there is none in the projection.
 *
 * NOTHING ON THIS PAGE APPLIES ANYTHING. hz publishes a generation and the
 * agent collects it on its own poll. hz never reaches into a machine, and
 * there is no Apply button here by design rather than by omission.
 *
 * # The rule this screen is built around
 *
 * **An empty section and an unknown section are different states.**
 *
 *   Empty, no gap  → hz says "nothing is wanted here". An opinion.
 *   Empty, a gap   → hz says "I have no opinion, and here is what would tell
 *                    me". Not an opinion, and not nothing.
 *
 * `Segment.Resolved: false` means the interface is UNKNOWN, not absent. The
 * founding bug of this whole system was an empty value selecting a production
 * default, and a screen that draws both of the above as a blank area
 * reintroduces it. So every section states which of the two it is, in words,
 * and `Unresolved` is given the same weight as the sections it qualifies —
 * rendered beside each one, and again as a whole at the top, never as a
 * footnote.
 *
 * Every gap carries a `reason` (the kind of not-knowing, a key) and a `why`
 * (prose naming what would close it). Both render: a kind with no next step is
 * a dead end, and a next step with no kind hides whose problem it is.
 */
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import { useMachineProjection } from "../api/hooks";
import type { MachineProjectionResp } from "../api/generated-types";
import {
  gapsFor,
  gapsOutsideSections,
  readSection,
  readSegment,
} from "../components/model/model";
import {
  GapNote,
  ScreenHeading,
  SectionPanel,
  ToneChip,
} from "../components/model/ModelBits";

/**
 * The section keys, in the order MachineConfig declares them.
 *
 * They are the projection's own Section* constants. A gap naming something
 * that is not in this list still renders — see `gapsOutsideSections` — because
 * the agent payload composes further sections onto the projection and a gap
 * this screen has no panel for is still hz saying it does not know something.
 */
const SECTIONS = ["machine", "segments", "forwards", "hosts", "packages", "feeds", "units"];

/** How to read this page, on the page. Four lines, at the top, not a tooltip. */
function Legend() {
  const rows = [
    {
      label: "hz has an opinion",
      tone: "fresh" as const,
      text: "hz computed this section and it has content. The values are hz's assertion, not a machine's report — nothing on this page is an observation, so nothing on it has an age.",
      hatched: false,
    },
    {
      label: "nothing wanted here",
      tone: "neutral" as const,
      text: "hz computed this section and the answer is empty. This is an OPINION: hz wants nothing here, and an agent reading it correctly leaves the thing alone.",
      hatched: false,
    },
    {
      label: "hz has no opinion",
      tone: "hatched" as const,
      text: "hz could NOT compute this section. The gap beside it says which kind of not-knowing it is and what would close it. This is the state an empty list must never be mistaken for.",
      hatched: true,
    },
  ];
  return (
    <Paper sx={{ p: 2, mb: 3 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 1 }}>
        How to read a section
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
        An empty section and an unknown section are different states, and this page never draws them the
        same. Every section below says which of the three it is before it shows you anything.
      </Typography>
      {rows.map((r) => (
        <Box key={r.label} sx={{ display: "flex", gap: 1.5, alignItems: "flex-start", mb: 1 }}>
          <Box sx={{ minWidth: 190, flexShrink: 0 }}>
            <ToneChip label={r.label} tone={r.tone} hatched={r.hatched} dashed={r.hatched} />
          </Box>
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {r.text}
          </Typography>
        </Box>
      ))}
    </Paper>
  );
}

function Rows({ head, body }: { head: string[]; body: (string | React.ReactNode)[][] }) {
  return (
    <TableContainer>
      <Table size="small">
        <TableHead>
          <TableRow>
            {head.map((h) => (
              <TableCell key={h} sx={{ fontWeight: 700 }}>
                {h}
              </TableCell>
            ))}
          </TableRow>
        </TableHead>
        <TableBody>
          {body.map((r, i) => (
            <TableRow key={i}>
              {r.map((c, j) => (
                <TableCell key={j} sx={{ fontFamily: "monospace" }}>
                  {c}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

function Projection({ mc }: { mc: MachineProjectionResp }) {
  const gaps = mc.unresolved ?? [];

  const machineGaps = gapsFor(gaps, "machine");
  const segments = readSection(
    mc.segments.length,
    gapsFor(gaps, "segments"),
    "This machine is in no segment, because its record declares none. hz wants it in none.",
  );
  const forwards = readSection(
    mc.forwards.length,
    gapsFor(gaps, "forwards"),
    "None declared — and none is the SAFE value, not a missing one. Forwarding between a machine's own segment interfaces is denied by default; a crossing is a declared exception with a reason. Empty is the rule's own answer.",
  );
  const hosts = readSection(
    mc.hosts.length,
    gapsFor(gaps, "hosts"),
    "hz wants no /etc/hosts entry on this machine.",
  );
  const packages = readSection(
    mc.packages.length,
    gapsFor(gaps, "packages"),
    "No package. Nothing is registered on this machine that asks for one, or the rungs it hosts declare no version — and a rung with no version projects NO package rather than the newest one.",
  );
  const feeds = readSection(
    mc.feeds.length,
    gapsFor(gaps, "feeds"),
    "No apt source. Nothing in this machine's projects or their ancestors declares a feed — so if a package were wanted, there would be no source carrying it.",
  );
  const units = readSection(
    mc.units.length,
    gapsFor(gaps, "units"),
    "No unit. There is no instance on this machine for one to be named after.",
  );
  const elsewhere = gapsOutsideSections(gaps, SECTIONS);

  return (
    <>
      {machineGaps.length > 0 ? (
        <Paper sx={{ p: 2, mb: 2 }}>
          <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
            hz does not have a record for this machine
          </Typography>
          <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
            Everything below was still computed, and every empty section below must be read against this: hz
            is not saying it wants nothing on this box, it is saying nobody has told it about the box.
          </Typography>
          {machineGaps.map((g, i) => (
            <GapNote key={i} gap={g} />
          ))}
        </Paper>
      ) : null}

      <Paper sx={{ p: 2, mb: 2 }}>
        <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap" }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
            What hz could not compute
          </Typography>
          <ToneChip
            label={gaps.length === 0 ? "nothing — every section is an opinion" : `${gaps.length} gap${gaps.length === 1 ? "" : "s"}`}
            tone={gaps.length === 0 ? "fresh" : "hatched"}
            hatched={gaps.length > 0}
            dashed={gaps.length > 0}
          />
        </Box>
        <Typography variant="body2" sx={{ color: "text.secondary", mt: 0.5 }}>
          {gaps.length === 0
            ? "hz computed every section of this projection. Any empty section below is hz's opinion that nothing is wanted there — which an agent correctly acts on by leaving the thing alone."
            : "Each gap is listed again inside the section it is about, so no section can be read without it. The kinds do not close the same way: a record closes one, the machine's own agent closes another, and a third clears on its own."}
        </Typography>
        {gaps.length > 0 ? (
          <Box sx={{ mt: 1.5 }}>
            {gaps.map((g, i) => (
              <GapNote key={i} gap={g} />
            ))}
          </Box>
        ) : null}
      </Paper>

      <SectionPanel
        title="Segments"
        blurb="The network segments this machine is a member of."
        reading={segments}
      >
        {mc.segments.map((seg) => {
          const reading = readSegment(seg);
          return (
            <Paper key={seg.name} variant="outlined" sx={{ p: 1.5, mb: 1, bgcolor: "transparent" }}>
              <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap", mb: 0.5 }}>
                <Typography sx={{ fontFamily: "monospace", fontWeight: 700 }}>{seg.name}</Typography>
                <ToneChip
                  label={reading.label}
                  tone={reading.tone}
                  hatched={!reading.resolved}
                  dashed={!reading.resolved}
                />
              </Box>
              <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
                {reading.meaning}
              </Typography>
              {reading.resolved ? (
                <Rows
                  head={["interface", "address", "peers"]}
                  body={[[seg.interface ?? "", seg.address ?? "", (seg.peers ?? []).join(" · ")]]}
                />
              ) : (
                <Box sx={{ display: "flex", gap: 2, flexWrap: "wrap" }}>
                  {["interface", "address", "peers"].map((f) => (
                    <ToneChip key={f} label={`${f}: unknown`} tone="hatched" hatched dashed />
                  ))}
                </Box>
              )}
            </Paper>
          );
        })}
      </SectionPanel>

      <SectionPanel
        title="Forwards"
        blurb="Declared exceptions to “a machine may not forward between its own segment interfaces”."
        reading={forwards}
      >
        {mc.forwards.length > 0 ? (
          <Rows head={["from", "to", "reason"]} body={mc.forwards.map((f) => [f.from, f.to, f.reason])} />
        ) : null}
      </SectionPanel>

      <SectionPanel
        title="Hosts"
        blurb="What /etc/hosts should carry — the names this machine resolves without asking anything."
        reading={hosts}
      >
        {mc.hosts.length > 0 ? (
          <Rows head={["name", "address"]} body={mc.hosts.map((h) => [h.name, h.address])} />
        ) : null}
      </SectionPanel>

      <SectionPanel
        title="Packages"
        blurb="What should be installed, at an exact version, held. Derived from the instances registered here: an instance at rung E of project P means P's package at E's declared version."
        reading={packages}
      >
        {mc.packages.length > 0 ? (
          <Rows
            head={["package", "version", "held"]}
            body={mc.packages.map((p) => [
              p.name,
              p.version,
              p.hold ? "held — apt will not move it" : "NOT held",
            ])}
          />
        ) : null}
      </SectionPanel>

      <SectionPanel
        title="Feeds"
        blurb="The apt sources those packages come from, resolved up the project tree. A machine cannot install a held version without the source that carries it, so the projection answers both halves."
        reading={feeds}
      >
        {mc.feeds.length > 0 ? (
          <Rows
            head={["from project", "url", "suite", "component", "signing key"]}
            body={mc.feeds.map((f) => [
              f.from,
              f.url,
              f.suite,
              f.component,
              f.key_id || "none — the feed is not pinned to a signing key",
            ])}
          />
        ) : null}
      </SectionPanel>

      <SectionPanel
        title="Units"
        blurb="Which instance units should exist and be enabled. The name carries all four coordinates: <project>@<environment>-<app>-<role>.service."
        reading={units}
      >
        {mc.units.length > 0 ? (
          <Rows
            head={["unit", "enabled"]}
            body={mc.units.map((u) => [u.name, u.enabled ? "enabled" : "present, not enabled"])}
          />
        ) : null}
      </SectionPanel>

      {elsewhere.length > 0 ? (
        <Paper sx={{ p: 2, mb: 2 }}>
          <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
            Gaps in sections this page does not show
          </Typography>
          <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
            hz named a section that is not part of the projection itself — the agent&apos;s payload composes
            further sections onto it. They are rendered rather than dropped: a gap this page has no panel for
            is still hz saying it does not know something.
          </Typography>
          {elsewhere.map((g, i) => (
            <GapNote key={i} gap={g} />
          ))}
        </Paper>
      ) : null}

      <Paper variant="outlined" sx={{ p: 2, bgcolor: "transparent" }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          Serial: {mc.serial}
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          Always 0, and NOT the generation. The serial is a monotonic rollback floor — hz refusing to serve a
          machine a generation older than one it already applied — and nothing produces one yet. &ldquo;Is
          this current&rdquo; is answered by the generation, a content hash, on Drift.
        </Typography>
      </Paper>
    </>
  );
}

function MachineProjectionScreen() {
  const { machine } = Route.useParams();
  const navigate = useNavigate();
  const { data, isLoading, error } = useMachineProjection(machine);

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 4 }}>
        <CircularProgress size={24} />
        <Typography>Asking hz what it says about {machine}…</Typography>
      </Box>
    );
  }

  if (error) {
    return (
      <Box sx={{ p: 3 }}>
        <Alert severity="error">
          hz could not be asked what it says about {machine}:{" "}
          {error instanceof Error ? error.message : String(error)}. This is a failure between this browser and
          hz — it is not hz saying it has no opinion about the machine, which would have been an answer with
          gaps in it.
        </Alert>
        <Button onClick={() => navigate({ to: "/machines" })} sx={{ mt: 2 }} variant="outlined">
          Back to machines
        </Button>
      </Box>
    );
  }

  return (
    <Box sx={{ p: 3 }}>
      <ScreenHeading
        title={machine}
        blurb="What hz says this machine should look like, computed from hz's own records alone. Nothing here is a reading from the machine — no value on this page has an age, because none of them came from the box. Nothing here applies anything either: hz publishes and the agent collects on its own poll."
        right={
          <Button onClick={() => navigate({ to: "/machines" })} variant="outlined" size="small">
            Back to machines
          </Button>
        }
      />
      <Legend />
      {data ? <Projection mc={data} /> : null}
    </Box>
  );
}

export const Route = createFileRoute("/machines/$machine")({
  component: MachineProjectionScreen,
});

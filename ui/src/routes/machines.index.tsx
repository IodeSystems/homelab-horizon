/**
 * Machines — what boxes there are, what segments they are in, what they host.
 *
 * Read-only. Sorted by IDENTITY, not by status: a fleet this size is read as a
 * list, and a table that reorders itself under the operator is unreadable.
 * What needs attention has a ranked home on /drift.
 *
 * THERE IS NO PROJECT COLUMN and the lede says why. A machine carries no
 * project and no environment — those are coordinates of an INSTANCE, and the
 * most important machine hosts two projects at once. Putting a project on a
 * machine row would force a choice the model refuses to make.
 *
 * Three states this screen must not flatten (plan/example-projection.md §4):
 *
 *   A machine that hosts NOTHING is not silent. A build box with a segment, an
 *   agent and a healthy poll has nothing to report an observed version FOR,
 *   permanently and by design.
 *   A machine hz cannot read registrations for is not a machine that hosts
 *   nothing.
 *   A segment membership hz cannot resolve is not a machine with no network.
 */
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Divider,
  Paper,
  Typography,
} from "@mui/material";
import { useMachines, useVersionDrift } from "../api/hooks";
import type { InstanceVersion, MachineResp } from "../api/generated-types";
import { readHosting, readInstanceSource, readMultiHomed } from "../components/model/model";
import {
  CannotAskBanner,
  Declared,
  DriftVerdict,
  ObservedVersion,
  ScreenHeading,
  ToneChip,
} from "../components/model/ModelBits";

function InstanceRow({ row }: { row: InstanceVersion }) {
  return (
    <Paper variant="outlined" sx={{ p: 1.5, mb: 1, bgcolor: "transparent" }}>
      <Typography sx={{ fontFamily: "monospace", fontWeight: 700, mb: 0.5 }}>{row.address}</Typography>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
        project {row.project || "—"} · environment {row.environment} · app {row.app} · role {row.role}. The
        four parts are the address; the machine record carries none of them.
      </Typography>
      <Box sx={{ display: "flex", gap: 4, flexWrap: "wrap" }}>
        <Box sx={{ minWidth: 180 }}>
          <Declared
            label="rung declares"
            value={row.desiredVersion}
            absentNote="no declared version — hz projects no package for this rung at all"
          />
        </Box>
        <Box sx={{ minWidth: 260 }}>
          <Typography
            variant="caption"
            sx={{ color: "text.secondary", textTransform: "uppercase", letterSpacing: 0.5 }}
          >
            this instance reported
          </Typography>
          <ObservedVersion row={row} />
        </Box>
        <Box sx={{ minWidth: 200 }}>
          <Typography
            variant="caption"
            sx={{ color: "text.secondary", textTransform: "uppercase", letterSpacing: 0.5, display: "block" }}
          >
            hz&apos;s verdict
          </Typography>
          <DriftVerdict row={row} />
        </Box>
      </Box>
    </Paper>
  );
}

function MachineCard({
  machine,
  instances,
}: {
  machine: MachineResp;
  instances: InstanceVersion[] | null;
}) {
  const navigate = useNavigate();
  const segments = machine.segments ?? [];
  const bridge = readMultiHomed(segments, machine.note ?? "");
  const hosting = readHosting(machine.name, instances);

  return (
    <Paper sx={{ p: 2, mb: 2 }}>
      <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap", mb: 0.5 }}>
        <Typography variant="h6" sx={{ fontWeight: 700, fontFamily: "monospace" }}>
          {machine.name}
        </Typography>
        {bridge.multiHomed ? <ToneChip label={bridge.headline} tone={bridge.tone} /> : null}
        <ToneChip
          label={machine.enrolled ? "agent credential issued" : "no agent credential"}
          tone={machine.enrolled ? "fresh" : "neutral"}
        />
        <Box sx={{ flex: 1 }} />
        <Button
          onClick={() => navigate({ to: "/machines/$machine", params: { machine: machine.name } })}
          variant="outlined"
          size="small"
        >
          What hz says about this machine
        </Button>
      </Box>

      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
        {machine.enrolled
          ? "hz holds an agent credential for this box. That is hz's own file beside its config, and it says nothing about whether the box is alive — liveness is the agent channel's question, on Drift."
          : "hz holds no agent credential for this box, so it has never been able to poll. Declared and not enrolled is an ordinary state, not a fault."}
      </Typography>

      <Divider sx={{ my: 1.5 }} />

      <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
        Segments
      </Typography>
      {segments.length === 0 ? (
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 1 }}>
          No segment membership is declared. hz can therefore say nothing about what this machine can reach —
          that is an absent declaration, not an isolated box.
        </Typography>
      ) : (
        <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap", my: 1 }}>
          {segments.map((s) => (
            <ToneChip key={s} label={s} tone="hatched" dashed />
          ))}
        </Box>
      )}
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
        {bridge.meaning} Every membership above is a NAME. There is no Segment record, so what interface,
        address or peer set it confers is unknown rather than empty.
      </Typography>

      <Divider sx={{ my: 1.5 }} />

      <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap", mb: 0.5 }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
          Instances placed here
        </Typography>
        <ToneChip
          label={hosting.headline}
          tone={hosting.tone}
          hatched={hosting.knowledge === "unknown"}
          dashed={hosting.knowledge === "unknown"}
        />
      </Box>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
        {hosting.meaning}
      </Typography>
      {hosting.instances.map((row) => (
        <InstanceRow key={row.address} row={row} />
      ))}
    </Paper>
  );
}

function MachinesScreen() {
  const machines = useMachines();
  const drift = useVersionDrift();

  if (machines.isLoading) {
    return (
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 4 }}>
        <CircularProgress size={24} />
        <Typography>Asking hz which machines it declares…</Typography>
      </Box>
    );
  }

  if (machines.error) {
    return (
      <Box sx={{ p: 3 }}>
        <Alert severity="error">
          hz could not be asked which machines it declares:{" "}
          {machines.error instanceof Error ? machines.error.message : String(machines.error)}. This screen is
          empty because the question failed, not because the fleet is.
        </Alert>
      </Box>
    );
  }

  const rows = machines.data ?? [];
  // NOT `isError ? null : (data ?? [])`. A pending read has no error and no
  // data, and `?? []` would turn "hz has not said yet" into "nothing is placed
  // anywhere" — every box below captioned as hosting nothing, authoritatively,
  // until the answer lands. Only a SUCCEEDED read yields rows.
  const source = readInstanceSource(drift, "The instance list on every machine below");
  const instances: InstanceVersion[] | null = source.known ? source.instances : null;

  return (
    <Box sx={{ p: 3 }}>
      <ScreenHeading
        title="Machines"
        blurb="Every box hz declares, its segment memberships, and the instances placed on it. There is no project column: a machine carries no project and no environment — an instance's four-part address carries both, and one machine routinely hosts instances from two projects."
      />

      {source.known ? null : <CannotAskBanner what={source.what} detail={source.detail} />}

      {source.known && source.unadmitted > 0 ? (
        <Alert severity="info" sx={{ mb: 2 }}>
          {source.unadmitted} registered address{source.unadmitted === 1 ? " is" : "es are"} not listed on any
          machine below, because nobody has approved {source.unadmitted === 1 ? "it" : "them"} yet. Pending is
          per ADDRESS, not per box — a machine already here can be waiting on a new one. They are in Config →
          Approvals.
        </Alert>
      ) : null}

      {rows.length === 0 ? (
        <Alert severity="info">
          hz declares no machine. That is an empty list, not a failed read — nothing has been declared yet,
          and hz issues an agent credential only for a machine it has been told about.
        </Alert>
      ) : (
        rows.map((m) => <MachineCard key={m.name} machine={m} instances={instances} />)
      )}
    </Box>
  );
}

export const Route = createFileRoute("/machines/")({
  component: MachinesScreen,
});

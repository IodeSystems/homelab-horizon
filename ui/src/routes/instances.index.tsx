/**
 * `/instances` — the Homelab Horizon instances: this gateway and its HA peers.
 *
 * Operator, 2026-09-29: *"these are Homelab Horizon Instances — they can be in
 * a project, or a part of a cluster."* Cluster is the HA peer fleet. A row is
 * one hz; the address audit that `/hosts` used to be is the detail of the
 * `this` row, at `/instances/$instance`.
 *
 * [Add instance] asks for the KIND first (operator, 2026-09-29, relayed from
 * the redline session): a **cluster node** — another copy of this hz, an HA
 * peer sharing its records — goes to the existing join flow on Settings → HA
 * Fleet; a **nested instance** — a separate hz with its own records and keys,
 * joined as a segment client (redline-prod-hz) — is DECLARED from here: a
 * nested hz is a Machine that runs hz (plan.md Tier 1b, decided 2026-09-29),
 * so the option opens `AddMachineDialog` with its hz URL required, and a rung
 * is then placed in it through `Environment.Upstream`. What a nested instance
 * cannot do yet — mirror packages, proxy config — is said on the option in
 * `NESTED_WAITS_ON`: greyed with a reason, never removed, and shrinking as
 * each piece lands.
 */
import { useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  List,
  ListItemButton,
  ListItemText,
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
import type { InstanceResp } from "../api/generated-types";
import { useProjects } from "../api/hooks";
import { useInstances } from "../api/instanceHooks";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { LocationCell, PROJECT_COLUMN_LABEL } from "../components/model/ProjectBits";
import { buildProjectIndex } from "../components/model/projectRoutes.ts";
import { isNested, peersLabel, projectOf, readInstancesSource } from "../components/instances/instances";
import { AddMachineDialog } from "../components/model/MachineDialogs";

/** What a declared nested instance cannot do yet — plan.md Tier 1b. N3
 * (`Environment.Upstream`) is built and is no longer listed. The tunnel code
 * (N1) landed but has never run on a box and does not survive a reboot, so it
 * stays. Kept beside the option it qualifies so the two cannot drift apart
 * unnoticed. */
export const NESTED_WAITS_ON = [
  "a credential the child presents to the parent (enrolment)",
  "an armed agent",
  "the registry crossing (packages mirrored, config proxied)",
];

/** The kind choice, apart from its dialog so a render check can draw it. */
export function AddInstanceKindPanel({ onCluster, onNested }: { onCluster: () => void; onNested: () => void }) {
  return (
    <List disablePadding data-instance-kinds>
      <ListItemButton onClick={onCluster} aria-label="Cluster node">
        <ListItemText
          primary="Cluster node"
          secondary="Another copy of this hz — an HA peer that shares its records. Joined from Settings → HA Fleet."
        />
      </ListItemButton>
      <ListItemButton onClick={onNested} aria-label="Declare a nested instance" data-nested-kind>
        <ListItemText
          primary="Declare a nested instance"
          secondary={
            <>
              A separate hz with its own records and keys, in its own project (e.g. a prod gateway). Declares its
              machine with the URL it answers on, and creates the VPN client it reaches this hz through (only
              this hz&apos;s API); a rung is then placed in it (Upstream).{" "}
              <span data-nested-cannot>
                It cannot yet mirror packages or proxy config — that waits on: {NESTED_WAITS_ON.join("; ")}.
              </span>
            </>
          }
        />
      </ListItemButton>
    </List>
  );
}

function AddInstance() {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [declaring, setDeclaring] = useState(false);
  return (
    <>
      <AddButton label="Add instance" onClick={() => setOpen(true)} />
      <Dialog open={open} onClose={() => setOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>Add an instance — which kind?</DialogTitle>
        <DialogContent sx={{ px: 1 }}>
          <AddInstanceKindPanel
            onCluster={() => {
              setOpen(false);
              navigate({ to: "/settings", search: { tab: "ha-fleet" } });
            }}
            onNested={() => {
              setOpen(false);
              setDeclaring(true);
            }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOpen(false)}>Cancel</Button>
        </DialogActions>
      </Dialog>
      <AddMachineDialog nested open={declaring} onClose={() => setDeclaring(false)} />
    </>
  );
}

function rowKey(row: InstanceResp): string {
  return `${row.self ? "self" : isNested(row) ? "nested" : "peer"}:${row.name}`;
}

export function InstancesScreen() {
  const query = useInstances();
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const navigate = useNavigate();
  const source = readInstancesSource(query);

  return (
    <Box sx={{ p: 3 }}>
      <TabHeader title="Instances" action={<AddInstance />} />

      {source.state === "failed" ? (
        <Alert severity="error">
          hz could not be asked which instances it knows: {source.message}. This list is empty because the
          read failed, not because there are none.
        </Alert>
      ) : source.state === "loading" ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz which instances it knows…</Typography>
        </Box>
      ) : source.rows.length === 0 ? (
        <EmptyRow text="hz listed no instances, not even itself." action={<AddInstance />} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Instance</TableCell>
                <TableCell>Address</TableCell>
                <TableCell>{PROJECT_COLUMN_LABEL}</TableCell>
                <TableCell>Cluster</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {source.rows.map((row) => (
                <TableRow
                  key={rowKey(row)}
                  hover
                  data-instance-row={row.name}
                  sx={{ cursor: "pointer" }}
                  onClick={() => navigate({ to: "/instances/$instance", params: { instance: row.name } })}
                >
                  <TableCell>
                    <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
                      <Link
                        to="/instances/$instance"
                        params={{ instance: row.name }}
                        onClick={(e) => e.stopPropagation()}
                        style={{ fontFamily: "monospace", fontWeight: 600 }}
                      >
                        {row.name}
                      </Link>
                      {row.self ? <Chip size="small" color="primary" variant="outlined" label="this" /> : null}
                    </Box>
                  </TableCell>
                  <TableCell sx={{ fontFamily: "monospace" }}>
                    {row.address || (
                      <Typography component="span" variant="body2" sx={{ color: "text.secondary", fontStyle: "italic" }}>
                        not detected
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell>
                    <LocationCell index={index} project={projectOf(row)} />
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" component="span" sx={{ fontWeight: 600 }}>
                      {row.role}
                    </Typography>{" "}
                    <Typography variant="body2" component="span" sx={{ color: "text.secondary" }}>
                      {peersLabel(row, source.rows)}
                    </Typography>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}

export const Route = createFileRoute("/instances/")({
  component: InstancesScreen,
});

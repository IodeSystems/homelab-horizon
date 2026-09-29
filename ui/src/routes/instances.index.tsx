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
 * joined as a segment client (redline-prod-hz) — is shown greyed with what it
 * waits on, because its backend does not exist yet (plan.md, Tier 1b).
 * Greyed with a reason, never removed.
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
import { useProjects } from "../api/hooks";
import { useInstances } from "../api/instanceHooks";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { LocationCell, PROJECT_COLUMN_LABEL } from "../components/model/ProjectBits";
import { buildProjectIndex } from "../components/model/projectRoutes.ts";
import { peersLabel, projectOf, readInstancesSource } from "../components/instances/instances";

/** What a nested instance waits on — plan.md Tier 1b, N1–N4. Kept beside the
 * option it greys so the two cannot drift apart unnoticed. */
export const NESTED_WAITS_ON = [
  "a segment that is an actual WireGuard tunnel",
  "an armed agent",
  "Environment.Upstream",
  "the registry crossing (packages mirrored, config proxied)",
];

/** The kind choice, apart from its dialog so a render check can draw it. */
export function AddInstanceKindPanel({ onCluster }: { onCluster: () => void }) {
  return (
    <List disablePadding data-instance-kinds>
      <ListItemButton onClick={onCluster} aria-label="Cluster node">
        <ListItemText
          primary="Cluster node"
          secondary="Another copy of this hz — an HA peer that shares its records. Joined from Settings → HA Fleet."
        />
      </ListItemButton>
      <ListItemButton disabled aria-label="Nested instance" data-nested-disabled>
        <ListItemText
          primary="Nested instance — not available yet"
          secondary={`A separate hz with its own records and keys, in its own project and segment, joined as a segment client (e.g. a prod gateway). Waits on: ${NESTED_WAITS_ON.join("; ")}.`}
        />
      </ListItemButton>
    </List>
  );
}

function AddInstance() {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
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
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOpen(false)}>Cancel</Button>
        </DialogActions>
      </Dialog>
    </>
  );
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
                  key={`${row.self ? "self" : "peer"}:${row.name}`}
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

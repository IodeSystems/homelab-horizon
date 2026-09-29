/**
 * `/instances` — the Homelab Horizon instances: this gateway and its HA peers.
 *
 * Operator, 2026-09-29: *"these are Homelab Horizon Instances — they can be in
 * a project, or a part of a cluster."* Cluster is the HA peer fleet. A row is
 * one hz; the address audit that `/hosts` used to be is the detail of the
 * `this` row, at `/instances/$instance`.
 *
 * [Add instance] links to the existing HA-peer join flow on Settings → HA
 * Fleet. It is not rebuilt here.
 */
import { useMemo } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Chip,
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

function AddInstance() {
  const navigate = useNavigate();
  return (
    <AddButton
      label="Add instance"
      ariaLabel="Add instance — joins a peer from Settings → HA Fleet"
      onClick={() => navigate({ to: "/settings", search: { tab: "ha-fleet" } })}
    />
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

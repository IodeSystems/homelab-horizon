/**
 * `/p/$project/vpn` — the VPN clients tab inside a project (plan/design/ui.md,
 * Decision 1, amendment 6). Rows whose `project` is in scope; `""` is global
 * and shows only on the unscoped `/vpn` tab.
 *
 * [Add VPN client] opens the same dialog as `/vpn`, fixed to this project —
 * the same reuse `AddServiceDialog` gets from `services.tsx`. The post-create
 * result (config/QR) is shown here too, not just on the unscoped screen.
 */
import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
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
import { useVPNPeers } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { AddPeerDialog, PeerResultDialog } from "./vpn";
import type { AddPeerResponse } from "../api/types";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectVPNclients() {
  const { index, resolution, scope, param } = useProjectContext();
  const q = useVPNPeers();
  const [adding, setAdding] = useState(false);
  const [peerResult, setPeerResult] = useState<{
    result: AddPeerResponse;
    name: string;
  } | null>(null);

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);
  const mine = (q.data ?? []).filter((r) => inScope(reading.names, r.project));
  const add = <AddButton label="Add VPN client" onClick={() => setAdding(true)} />;

  return (
    <Box>
      <TabHeader title={`VPN clients in ${route.name}`} action={add} />
      <ScopeControl reading={reading} to="/p/$project/vpn" param={param} />
      {q.error ? (
        <Alert severity="error">hz could not be asked: {q.error.message}</Alert>
      ) : q.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No VPN clients attributed to ${route.name}.`} action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Client</TableCell><TableCell>Allowed IPs</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((r) => (
                <TableRow key={r.publicKey} hover>
                  <TableCell sx={{ fontFamily: "monospace" }}>{r.name}</TableCell><TableCell sx={{ fontFamily: "monospace" }}>{r.allowedIPs}</TableCell>
                  <TableCell>
                    <LocationCell index={index} project={r.project} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <AddPeerDialog
        open={adding}
        project={route.name}
        onClose={() => setAdding(false)}
        onResult={(result, name) => {
          setAdding(false);
          setPeerResult({ result, name });
        }}
      />
      <PeerResultDialog
        open={peerResult !== null}
        onClose={() => setPeerResult(null)}
        result={peerResult?.result ?? null}
        name={peerResult?.name ?? ""}
      />
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/vpn")({
  component: ProjectVPNclients,
});

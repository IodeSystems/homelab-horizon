/**
 * `/instances/$instance` — one hz instance.
 *
 * For THIS instance: its project (with the control that puts it in one) and
 * the address audit that used to be `/hosts`. For a PEER: its project, read
 * only, and its role in the cluster — hz does not declare a peer's machine
 * from here, because a peer ID is not necessarily that box's hostname and a
 * record under the wrong name matches nothing. For a NESTED hz (a declared
 * machine that runs its own hz): its URL and owner, both from its Machine
 * record, edited on that machine's page — this hz never contacts it.
 *
 * The self control never sends a name. Undeclared → `machines/add` with
 * `self: true`, which the server resolves to its own hostname (CLAUDE.md
 * invariant 7); declared → `machines/set`. See `useSetSelfProject`.
 */
import { useEffect, useMemo, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import type { InstanceResp } from "../api/generated-types";
import { useProjects } from "../api/hooks";
import { useInstances, useSetSelfProject } from "../api/instanceHooks";
import { useMintInstanceToken } from "../api/deployHooks";
import { TabHeader } from "../components/model/FlowBits";
import { LocationCell } from "../components/model/ProjectBits";
import { buildProjectIndex, type ProjectIndex } from "../components/model/projectRoutes.ts";
import { HostsAudit } from "../components/hosts/HostsAudit";
import { findInstance, projectOf, readInstancesSource } from "../components/instances/instances";

function when(unix: number): string {
  return unix > 0 ? new Date(unix * 1000).toISOString().replace("T", " ").replace(/\.\d+Z$/, "Z") : "never";
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <TableRow>
      <TableCell sx={{ color: "text.secondary", width: 160 }}>{label}</TableCell>
      <TableCell>{children}</TableCell>
    </TableRow>
  );
}

function ProjectDialog({
  row,
  open,
  onClose,
}: {
  row: InstanceResp;
  open: boolean;
  onClose: () => void;
}) {
  const projects = useProjects();
  const set = useSetSelfProject();
  const [project, setProject] = useState(projectOf(row));

  useEffect(() => {
    if (open) {
      setProject(projectOf(row));
      set.reset();
    }
  }, [open, row]);

  const submit = () =>
    set.mutate({ declared: row.declared, name: row.name, project }, { onSuccess: () => onClose() });

  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>
        Project for <code>{row.name}</code>
      </DialogTitle>
      <DialogContent>
        <TextField
          select
          fullWidth
          label="Project"
          value={project}
          onChange={(e) => setProject(e.target.value)}
          sx={{ mt: 1, mb: 2 }}
        >
          <MenuItem value="">global</MenuItem>
          {(projects.data ?? []).map((p) => (
            <MenuItem key={p.name} value={p.name} sx={{ fontFamily: "monospace" }}>
              {p.name}
            </MenuItem>
          ))}
        </TextField>
        {set.error ? <Alert severity="error">{set.error.message}</Alert> : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={set.isPending}>
          {set.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function ProjectControl({ row, index }: { row: InstanceResp; index: ProjectIndex }) {
  const [open, setOpen] = useState(false);

  const control = row.role === "nested" ? (
    <Typography variant="caption" sx={{ color: "text.secondary" }}>
      Its owner is its machine record&apos;s: edit it on{" "}
      <Link to="/machines/$machine" params={{ machine: row.name }}>
        its machine page
      </Link>
      .
    </Typography>
  ) : !row.self ? (
    <Typography variant="caption" sx={{ color: "text.secondary" }}>
      A peer&apos;s machine is declared on Machines, under its hostname — which may differ from its peer ID.
    </Typography>
  ) : row.role === "replica" ? (
    <Typography variant="caption" sx={{ color: "text.secondary" }}>
      This instance is read-only. Change it on the config primary{row.primaryId ? ` (${row.primaryId})` : ""}.
    </Typography>
  ) : (
    <Button
      aria-label={row.declared ? "Change project" : "Put in a project"}
      variant="outlined"
      size="small"
      onClick={() => setOpen(true)}
      sx={{ textTransform: "none" }}
    >
      {row.declared ? "Change project" : "Put in a project"}
    </Button>
  );

  return (
    <Box sx={{ display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap" }}>
      <LocationCell index={index} project={projectOf(row)} />
      {row.declared ? null : (
        <Typography variant="caption" sx={{ color: "text.secondary" }}>
          no machine record
        </Typography>
      )}
      <Box sx={{ flex: 1 }} />
      {control}
      {row.self ? <ProjectDialog row={row} open={open} onClose={() => setOpen(false)} /> : null}
    </Box>
  );
}

/**
 * The mint result, apart from its dialog so a render check can draw it. The
 * token is in the mutation's answer and nowhere else: closing the dialog
 * forgets it, and hz keeps only its sha256.
 */
export function MintTokenPanel({
  machine,
  token,
  error,
  pending,
  onMint,
  onClose,
}: {
  machine: string;
  token: string | null;
  error: string | null;
  pending: boolean;
  onMint: () => void;
  onClose: () => void;
}) {
  return (
    <>
      <DialogTitle>
        Instance token for <code>{machine}</code>
      </DialogTitle>
      <DialogContent>
        {token ? (
          <>
            <Alert severity="warning" sx={{ mb: 2 }} data-token-shown-once>
              Shown once. hz keeps only its sha256 — copy it now into the child&apos;s upstream.token_file (0600).
            </Alert>
            <TextField
              fullWidth
              value={token}
              slotProps={{ input: { readOnly: true }, htmlInput: { "data-instance-token": true } }}
              sx={{ "& input": { fontFamily: "monospace" } }}
            />
          </>
        ) : (
          <Typography variant="body2" data-token-offer>
            {machine} pulls its rungs&apos; applied artifact with this token, and forwards deploy reports. A new
            token replaces the old one, which stops working.
          </Typography>
        )}
        {error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            {error}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>{token ? "Done" : "Cancel"}</Button>
        {token ? null : (
          <Button variant="contained" onClick={onMint} disabled={pending}>
            {pending ? "Minting…" : "Mint instance token"}
          </Button>
        )}
      </DialogActions>
    </>
  );
}

function MintTokenDialog({ machine, open, onClose }: { machine: string; open: boolean; onClose: () => void }) {
  const mint = useMintInstanceToken();
  const close = () => {
    mint.reset();
    onClose();
  };
  return (
    <Dialog open={open} onClose={close} fullWidth maxWidth="sm">
      <MintTokenPanel
        machine={machine}
        token={mint.data?.token ?? null}
        error={mint.error?.message ?? null}
        pending={mint.isPending}
        onMint={() => mint.mutate({ machine })}
        onClose={close}
      />
    </Dialog>
  );
}

/** A nested hz's token and last pull — what this hz records of it, never a dial. */
export function NestedPullFacts({ row }: { row: InstanceResp }) {
  const [open, setOpen] = useState(false);
  const n = row.nested;
  return (
    <>
      <Fact label="Instance token">
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap" }}>
          <Typography variant="body2" data-instance-token-state={n?.hasToken ? "minted" : "none"}>
            {n?.hasToken ? "minted" : "none minted — it cannot pull from this hz"}
          </Typography>
          <Button size="small" variant="outlined" sx={{ textTransform: "none" }} onClick={() => setOpen(true)}>
            {n?.hasToken ? "Mint a new instance token" : "Mint instance token"}
          </Button>
        </Box>
        <MintTokenDialog machine={row.name} open={open} onClose={() => setOpen(false)} />
      </Fact>
      <Fact label="Last pull">
        {n?.pullsUnknown ? (
          <Typography variant="body2" data-last-pull="unknown" sx={{ color: "warning.main" }}>
            cannot ask: {n.pullsUnknown}
          </Typography>
        ) : !n || n.lastPullAt === 0 ? (
          <Typography variant="body2" data-last-pull="never" sx={{ color: "text.secondary" }}>
            never pulled
          </Typography>
        ) : (
          <Typography variant="body2" data-last-pull="pulled">
            {when(n.lastPullAt)} · {n.lastPullRung}
          </Typography>
        )}
      </Fact>
    </>
  );
}

export function InstanceScreen({ name }: { name: string }) {
  const query = useInstances();
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const source = readInstancesSource(query);

  if (source.state === "failed") {
    return (
      <Box sx={{ p: 3 }}>
        <TabHeader title={name} />
        <Alert severity="error">
          hz could not be asked about its instances: {source.message}.
        </Alert>
      </Box>
    );
  }
  if (source.state === "loading") {
    return (
      <Box sx={{ p: 3 }}>
        <TabHeader title={name} />
        <Box sx={{ display: "flex", alignItems: "center", gap: 2 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz about {name}…</Typography>
        </Box>
      </Box>
    );
  }

  const row = findInstance(source.rows, name);
  if (!row) {
    return (
      <Box sx={{ p: 3 }}>
        <TabHeader title={name} />
        <Alert severity="warning">
          hz knows no instance named {name}. <Link to="/instances">All instances</Link>
        </Alert>
      </Box>
    );
  }

  return (
    <Box sx={{ p: 3 }}>
      <TabHeader
        title={row.name}
        action={row.self ? <Chip size="small" color="primary" variant="outlined" label="this" /> : undefined}
      />

      <TableContainer component={Paper} sx={{ mb: 3 }}>
        <Table size="small">
          <TableBody>
            <Fact label="Address">
              <Typography sx={{ fontFamily: "monospace" }}>{row.address || "not detected"}</Typography>
            </Fact>
            <Fact label="Role">{row.role}</Fact>
            {row.role === "nested" ? (
              <Fact label="Kind">
                <Typography variant="body2" data-nested-fact>
                  A separate hz — its own records and keys, not a member of this cluster. Declared here as a
                  machine with the URL it answers on; nothing on this page was read from it.
                </Typography>
              </Fact>
            ) : null}
            {row.role === "nested" ? <NestedPullFacts row={row} /> : null}
            {row.role === "standalone" || row.role === "nested" ? null : (
              <Fact label="Config primary">
                <Typography sx={{ fontFamily: "monospace" }}>{row.primaryId || "none marked"}</Typography>
              </Fact>
            )}
            {row.self ? <Fact label="Version">{row.version || "unknown"}</Fact> : null}
            {row.sync ? (
              <Fact label="Last pull">
                {when(row.sync.lastSuccessAt)}
                {row.sync.lastError ? (
                  <Typography variant="body2" sx={{ color: "error.main" }}>
                    last attempt failed: {row.sync.lastError}
                  </Typography>
                ) : null}
              </Fact>
            ) : null}
            {!row.self && row.role !== "nested" ? (
              <Fact label="Sync">
                <Typography variant="body2" sx={{ color: "text.secondary" }}>
                  Not observed from here. Reachability is on{" "}
                  <Link to="/settings" search={{ tab: "ha-fleet" }}>
                    Settings → HA Fleet
                  </Link>
                  .
                </Typography>
              </Fact>
            ) : null}
            <Fact label="Project">
              <ProjectControl row={row} index={index} />
            </Fact>
          </TableBody>
        </Table>
      </TableContainer>

      {row.self ? (
        <>
          <Typography variant="subtitle1" sx={{ fontWeight: 700, mb: 1 }}>
            Addresses
          </Typography>
          <HostsAudit />
        </>
      ) : null}
    </Box>
  );
}

function InstanceRoute() {
  const { instance } = Route.useParams();
  return <InstanceScreen name={instance} />;
}

export const Route = createFileRoute("/instances/$instance")({
  component: InstanceRoute,
});

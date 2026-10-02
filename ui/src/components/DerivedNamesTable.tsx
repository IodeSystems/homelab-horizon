import { Fragment } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from "@mui/material";
import InfoOutlinedIcon from "@mui/icons-material/InfoOutlined";
import { Link } from "@tanstack/react-router";
import type { LocalDNSDerived, ServicePort } from "../api/generated-types";

// The names hz answers because a service declares them — the half of the
// resolver an operator did not type. Grouped by the address they resolve to,
// because "what lives at 192.0.2.10" is the question this table answers.

export type DerivedState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; derived: LocalDNSDerived[] };

export interface DerivedGroup {
  ip: string;
  names: LocalDNSDerived[];
}

/** groupByHost groups derived names by address, addresses in numeric order. */
export function groupByHost(derived: LocalDNSDerived[]): DerivedGroup[] {
  const byIP = new Map<string, LocalDNSDerived[]>();
  for (const d of derived) {
    const list = byIP.get(d.ip);
    if (list) list.push(d);
    else byIP.set(d.ip, [d]);
  }
  return [...byIP.entries()]
    .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }))
    .map(([ip, names]) => ({ ip, names: [...names].sort((x, y) => x.name.localeCompare(y.name)) }));
}

function portLabel(p: ServicePort): string {
  return p.active ? `${p.label} (active)` : p.label;
}

function Ports({ ports }: { ports: ServicePort[] }) {
  if (ports.length === 0) {
    return (
      <Typography variant="caption" color="text.secondary">
        none
      </Typography>
    );
  }
  return (
    <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
      {ports.map((p) => (
        <Tooltip key={`${p.label}-${p.port}`} title={portLabel(p)}>
          <Chip
            size="small"
            variant={p.active ? "filled" : "outlined"}
            data-port={p.port}
            label={`:${p.port} ${portLabel(p)}`}
            sx={{ fontFamily: "monospace" }}
          />
        </Tooltip>
      ))}
    </Box>
  );
}

export function DerivedNamesTable({
  state,
  onOverride,
}: {
  state: DerivedState;
  onOverride: (d: LocalDNSDerived) => void;
}) {
  const count = state.kind === "ready" ? ` (${state.derived.length})` : "";
  return (
    <Box sx={{ mt: 3 }} data-derived-state={state.kind}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 0.5, mb: 1 }}>
        <Typography variant="subtitle2">Derived from services{count}</Typography>
        <Tooltip title="Each name follows its service. Override one with a local record of the same name.">
          <InfoOutlinedIcon fontSize="small" color="action" aria-label="About derived names" />
        </Tooltip>
      </Box>

      {state.kind === "loading" && (
        <Typography variant="body2" color="text.secondary">
          Loading derived names…
        </Typography>
      )}

      {state.kind === "error" && (
        <Alert severity="error">Could not load derived names: {state.message}</Alert>
      )}

      {state.kind === "ready" && state.derived.length === 0 && (
        <Typography variant="body2" color="text.secondary">
          No service declares an internal DNS name.
        </Typography>
      )}

      {state.kind === "ready" && state.derived.length > 0 && (
        <TableContainer component={Paper} variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Service</TableCell>
                <TableCell>Ports</TableCell>
                <TableCell align="right" />
              </TableRow>
            </TableHead>
            <TableBody>
              {groupByHost(state.derived).map((g) => (
                <Fragment key={g.ip}>
                  <TableRow data-host={g.ip} sx={{ bgcolor: "action.hover" }}>
                    <TableCell colSpan={4}>
                      <Typography component="span" variant="body2" sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                        {g.ip}
                      </Typography>
                      <Typography component="span" variant="caption" color="text.secondary" sx={{ ml: 1 }}>
                        {g.names.length} {g.names.length === 1 ? "name" : "names"}
                      </Typography>
                    </TableCell>
                  </TableRow>
                  {g.names.map((d) => (
                    <TableRow key={d.name} hover data-derived-name={d.name} data-derived-host={g.ip}>
                      <TableCell sx={{ fontFamily: "monospace", pl: 4 }}>{d.name}</TableCell>
                      <TableCell>
                        <Link to="/services" search={{} as never} hash={`service-${d.service}`}>
                          {d.service}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Ports ports={d.ports ?? []} />
                      </TableCell>
                      <TableCell align="right">
                        <Button size="small" onClick={() => onOverride(d)}>
                          Override
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </Fragment>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}

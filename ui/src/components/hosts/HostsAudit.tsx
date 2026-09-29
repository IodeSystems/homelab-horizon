/**
 * The address audit — what points at this box, and what breaks if I move it.
 *
 * It used to be the whole `/hosts` screen. It is now one section of
 * `/instances/$instance` for THIS instance (plan/design/ui.md, Decision 1
 * amendment 6: flows over prose — the legend and blurb are gone, the table and
 * the move action stay). It reads GET /api/v1/topology/hosts/view and nothing
 * else.
 *
 * FOUR THINGS THIS SECTION REFUSES TO FLATTEN (unchanged from `/hosts`):
 *
 *   "hz has not said" is not "nothing points at this". A pending or failed read
 *   renders as a banner, never as an empty table — `readHostsSource`.
 *
 *   "Nothing references this host" is not "moving it is free". Records written
 *   @name follow the host; records carrying its address as a plain string do
 *   not, and break. Both are listed, in SEPARATE sections with separate counts —
 *   never merged, because they behave in opposite ways.
 *
 *   A reference shows BOTH halves. `@nas:8080 → 192.168.1.51:8080`, always.
 *
 *   @self is a row, not a footnote — and it is not editable, with the sentence
 *   saying where it IS set. A reason in place of the control, never a silently
 *   removed one.
 */
import { Fragment, useState } from "react";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  IconButton,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import type { HostView } from "../../api/generated-types";
import { ApiError } from "../../api/client";
import { useHostsView, useSetHostIP } from "../../api/hooks";
import { CannotAskBanner, ToneChip } from "../model/ModelBits";
import { EmptyRow } from "../model/FlowBits";
import { KindGroup, OccurrenceSection, RefusalNote } from "./HostBits";
import {
  groupByKind,
  readDependants,
  readOccurrences,
  readHostIdentity,
  readHostsSource,
  readMove,
  readRefusal,
} from "./hosts";

/**
 * The guided address change. It names the consequence before it happens, and
 * dismissal is explicit twice: a labelled Cancel and a visible X.
 */
export function MoveHostDialog({ host, onClose }: { host: HostView; onClose: () => void }) {
  return (
    <Dialog open onClose={onClose} maxWidth="md" fullWidth>
      <MoveHostPanel host={host} onClose={onClose} />
    </Dialog>
  );
}

/**
 * The dialog's contents, separate from the Dialog shell: MUI's Dialog renders
 * through a Portal, which produces nothing under react-dom/server, so the rules
 * inside it could otherwise be checked by nobody.
 */
export function MoveHostPanel({ host, onClose }: { host: HostView; onClose: () => void }) {
  const move = readMove(host);
  const setIP = useSetHostIP();
  const [ip, setIP_] = useState(host.ip);
  const [done, setDone] = useState<string>("");

  const failed = setIP.error;
  const refusal =
    failed instanceof ApiError || failed instanceof Error ? readRefusal(failed.message) : null;

  const submit = () => {
    setIP.mutate(
      { name: host.name, ip: ip.trim() },
      { onSuccess: (resp) => setDone(resp.host?.ip || ip.trim()) },
    );
  };

  return (
    <>
      <DialogTitle sx={{ display: "flex", alignItems: "center", gap: 2 }}>
        <Box sx={{ flex: 1 }}>{move.action}</Box>
        <IconButton onClick={onClose} aria-label="Close this dialog" title="Close">
          <CloseIcon />
        </IconButton>
      </DialogTitle>
      <DialogContent dividers>
        {done ? (
          <Alert severity="success" sx={{ mb: 2 }}>
            @{host.name} is now {done}. {move.followers || "Nothing else moved."} Nothing is
            rendered until the next sync — the Sync button applies it.
          </Alert>
        ) : null}

        <Typography variant="body2" sx={{ mb: 2 }}>
          {move.consequence}
        </Typography>

        <TextField
          label="New address for this host"
          value={ip}
          onChange={(e) => setIP_(e.target.value)}
          size="small"
          fullWidth
          disabled={setIP.isPending}
          helperText={`Currently ${host.ip || "unset"}. An address, not another reference.`}
          sx={{ mb: 2 }}
        />

        {refusal ? (
          <Box sx={{ mb: 2 }}>
            <RefusalNote refusal={refusal} />
          </Box>
        ) : null}

        {host.references.length > 0 ? (
          <>
            <Divider sx={{ mb: 2 }} />
            <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 1 }}>
              These {host.references.length} records will follow it
            </Typography>
            {groupByKind(host.references).map((g) => (
              <KindGroup key={g.kind} group={g} />
            ))}
          </>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={setIP.isPending}>
          {done ? "Close" : "Cancel — change nothing"}
        </Button>
        <Button
          variant="contained"
          onClick={submit}
          disabled={setIP.isPending || !ip.trim() || ip.trim() === host.ip}
        >
          {setIP.isPending ? (
            <CircularProgress size={20} />
          ) : host.references.length > 0 ? (
            `Save — move ${host.references.length} record${host.references.length === 1 ? "" : "s"}`
          ) : (
            "Save the new address"
          )}
        </Button>
      </DialogActions>
    </>
  );
}

const COLUMNS = 5;

/** One host: a summary row, then a full-width row with its two lists. */
function HostRows({ host, onMove }: { host: HostView; onMove: (h: HostView) => void }) {
  const identity = readHostIdentity(host);
  const dependants = readDependants(host);
  const occurrences = readOccurrences(host);
  const move = readMove(host);

  return (
    <>
      <TableRow sx={{ "& > td": { borderBottom: "none" } }}>
        <TableCell>
          <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap" }}>
            <Typography sx={{ fontFamily: "monospace", fontWeight: 700 }}>{identity.title}</Typography>
            <ToneChip
              label={identity.label}
              tone={identity.tone}
              hatched={identity.kind === "nameless"}
              dashed={identity.kind === "nameless"}
            />
          </Box>
        </TableCell>
        <TableCell>
          {host.addressable ? (
            <Typography sx={{ fontFamily: "monospace" }}>{host.ip}</Typography>
          ) : (
            <>
              <Typography variant="body2" sx={{ color: "text.secondary", fontStyle: "italic" }}>
                hz has not detected one
              </Typography>
              <Typography variant="caption" sx={{ color: "text.secondary" }}>
                {host.notAddressableWhy}
              </Typography>
            </>
          )}
        </TableCell>
        <TableCell>
          <ToneChip
            label={dependants.headline}
            tone={dependants.tone}
            hatched={dependants.knowledge === "unreferenced"}
          />
        </TableCell>
        <TableCell>
          <ToneChip
            label={occurrences.headline}
            tone={occurrences.tone}
            hatched={occurrences.knowledge !== "clean"}
            dashed={occurrences.knowledge === "unscanned"}
          />
        </TableCell>
        <TableCell align="right">
          {move.knowledge === "movable" ? (
            <Button variant="outlined" size="small" onClick={() => onMove(host)} sx={{ textTransform: "none" }}>
              {move.action}
            </Button>
          ) : move.knowledge === "elsewhere" ? (
            <Typography variant="caption" sx={{ color: "text.secondary" }}>
              {move.why}
            </Typography>
          ) : null}
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell colSpan={COLUMNS} sx={{ pt: 0 }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
            What points at it
          </Typography>
          {/* Said on every host, at every count: this list is the half that
              FOLLOWS the host, and the other half is below. */}
          <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
            {dependants.literalCaveat}
          </Typography>
          {groupByKind(host.references).map((g) => (
            <KindGroup key={g.kind} group={g} />
          ))}
          {/* THE OTHER LIST. Separate heading, separate count — never appended
              to the references above. */}
          <OccurrenceSection reading={occurrences} records={host.occurrences} />
        </TableCell>
      </TableRow>
    </>
  );
}

/** The table on its own, so a render check can draw one host without a query. */
export function HostsTable({ hosts, onMove }: { hosts: HostView[]; onMove: (h: HostView) => void }) {
  return (
    <TableContainer component={Paper}>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Host</TableCell>
            <TableCell>Address</TableCell>
            <TableCell>Follows it</TableCell>
            <TableCell>Holds it as text</TableCell>
            <TableCell />
          </TableRow>
        </TableHead>
        <TableBody>
          {hosts.map((h) => (
            <Fragment key={h.self ? "@self" : h.name || `ip:${h.ip}`}>
              <HostRows host={h} onMove={onMove} />
            </Fragment>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

export function HostsAudit() {
  const query = useHostsView();
  const source = readHostsSource(query);
  const [moving, setMoving] = useState<HostView | null>(null);

  if (!source.known) {
    return (
      <Box>
        {query.isLoading ? (
          <Box sx={{ display: "flex", alignItems: "center", gap: 2, mb: 2 }}>
            <CircularProgress size={24} />
            <Typography>Asking hz which hosts it declares and what points at them…</Typography>
          </Box>
        ) : null}
        <CannotAskBanner what={source.what} detail={source.detail} />
      </Box>
    );
  }

  const declared = source.hosts.filter((h) => !h.self);

  return (
    <Box>
      <HostsTable hosts={source.hosts} onMove={setMoving} />
      {declared.length === 0 ? (
        <Box sx={{ mt: 2 }}>
          <EmptyRow text="No host is declared besides @self. Declare one on Observability." />
        </Box>
      ) : null}
      {moving ? <MoveHostDialog host={moving} onClose={() => setMoving(null)} /> : null}
    </Box>
  );
}

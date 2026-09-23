/**
 * Hosts — what points at this box, and what breaks if I move it.
 *
 * Host references (`@nas`, `@self`) put a machine's address in exactly one
 * record so every consumer resolves through it. Until now they had no screen:
 * `hz host show` answered the question in a terminal and `TopologyResp.SelfHost`
 * crossed the wire with nothing rendering it. This is that answer as a page.
 *
 * It reads GET /api/v1/topology/hosts/view and nothing else.
 *
 * FOUR THINGS THIS SCREEN REFUSES TO FLATTEN:
 *
 *   "hz has not said" is not "nothing points at this". A pending or failed read
 *   renders as a banner, never as an empty list — `readHostsSource`.
 *
 *   "Nothing references this host" is not "moving it is free". hz can only
 *   enumerate records written @name; a config written before references existed
 *   is entirely literals, and hz cannot find those. Every dependant count on
 *   this page carries that caveat.
 *
 *   A reference shows BOTH halves. `@nas:8080 → 192.168.1.51:8080`, always.
 *
 *   @self is a row, not a footnote — and it is not editable, with the sentence
 *   saying where it IS set. A greyed control with an explanation, never a
 *   removed one.
 *
 * It is NOT the /observability host table, which declares names and labels for
 * scrape purposes and edits the whole list. Different job, different URL. This
 * one is read-first with a single guided edit: the address, which is the one
 * edit a machine move needs.
 */
import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
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
  TextField,
  Typography,
} from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import type { HostView } from "../api/generated-types";
import { ApiError } from "../api/client";
import { useHostsView, useSetHostIP } from "../api/hooks";
import { CannotAskBanner, ScreenHeading, ToneChip } from "../components/model/ModelBits";
import { KindGroup, Note, RefusalNote } from "../components/hosts/HostBits";
import {
  groupByKind,
  kindSummary,
  readDependants,
  readHostIdentity,
  readHostsSource,
  readMove,
  readRefusal,
} from "../components/hosts/hosts";

/**
 * How to read a row, stated on the screen that uses it.
 *
 * Three lines, because the notation is the whole mechanism and an operator who
 * has only ever seen literal addresses has no reason to guess what "@" means.
 */
export function Legend({ literalsUnlisted }: { literalsUnlisted: boolean }) {
  return (
    <Paper sx={{ p: 2, mb: 3 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 1 }}>
        How to read this screen
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1 }}>
        A record can name a host instead of carrying its address:{" "}
        <Box component="span" sx={{ fontFamily: "monospace", fontWeight: 700 }}>
          @nas:8080
        </Box>{" "}
        rather than{" "}
        <Box component="span" sx={{ fontFamily: "monospace" }}>
          192.168.1.51:8080
        </Box>
        . Every such record is listed under its host below, showing what it says
        and what it means right now — both halves, because one alone hides
        either the indirection or the address.
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1 }}>
        <Box component="span" sx={{ fontFamily: "monospace", fontWeight: 700 }}>
          @self
        </Box>{" "}
        is this gateway&apos;s own address. It is not a declaration: each
        instance resolves it to its own, so a peer never inherits this box&apos;s
        address. On a gateway it is usually the busiest reference in the config.
      </Typography>
      {literalsUnlisted ? (
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          <strong>What this screen cannot show you:</strong> records that carry
          an address as a plain string. A literal says nothing about which
          machine it means — that is why references exist — so hz cannot find
          them. A host with no records listed is a host nothing FOLLOWS, which
          is not the same as a host nothing depends on.
        </Typography>
      ) : null}
    </Paper>
  );
}

/**
 * The guided address change.
 *
 * It names the consequence before it happens — how many records follow, of
 * which kinds, each one listed with what it resolves to now — because that is
 * the entire claim the indirection makes, and an operator agreeing to move
 * thirty records should be able to see the thirty.
 *
 * Dismissal is explicit twice: a labelled Cancel and a visible X. Nothing here
 * relies on click-outside or Esc.
 */
export function MoveHostDialog({ host, onClose }: { host: HostView; onClose: () => void }) {
  return (
    <Dialog open onClose={onClose} maxWidth="md" fullWidth>
      <MoveHostPanel host={host} onClose={onClose} />
    </Dialog>
  );
}

/**
 * The dialog's contents, separate from the Dialog shell.
 *
 * Split for one reason: MUI's Dialog renders through a Portal, which produces
 * nothing at all under react-dom/server, so a dialog left whole cannot be
 * rendered offline and its rules — the consequence named before the save, the
 * two ways out, every follower listed — would be checked by nobody. The shell
 * above is then three props with no logic in it.
 */
export function MoveHostPanel({
  host,
  onClose,
}: {
  host: HostView;
  onClose: () => void;
}) {
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
          helperText={`Currently ${host.ip || "unset"}. An address, not another reference — a declaration is where the chain ends.`}
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

export function HostCard({
  host,
  literalsUnlisted,
  onMove,
}: {
  host: HostView;
  literalsUnlisted: boolean;
  onMove: (h: HostView) => void;
}) {
  const identity = readHostIdentity(host);
  const dependants = readDependants(host, literalsUnlisted);
  const move = readMove(host);
  const labels = Object.entries(host.labels ?? {});

  return (
    <Paper sx={{ p: 2, mb: 2 }}>
      <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap", mb: 0.5 }}>
        <Typography variant="h6" sx={{ fontWeight: 700, fontFamily: "monospace" }}>
          {identity.title}
        </Typography>
        <ToneChip
          label={identity.label}
          tone={identity.tone}
          hatched={identity.kind === "nameless"}
          dashed={identity.kind === "nameless"}
        />
        <Box sx={{ flex: 1 }} />
        {move.knowledge === "movable" ? (
          <Button variant="outlined" size="small" onClick={() => onMove(host)}>
            {move.action}
          </Button>
        ) : null}
      </Box>

      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
        {identity.meaning}
      </Typography>

      {/* The address is rendered for EVERY row, editable or not. A field you
          cannot change is greyed with the reason beside it, never removed —
          a removed field is one nobody can even ask about. */}
      <Box sx={{ display: "flex", gap: 4, flexWrap: "wrap", mb: 1 }}>
        <Box>
          <Typography
            variant="caption"
            sx={{ color: "text.secondary", textTransform: "uppercase", letterSpacing: 0.5, display: "block" }}
          >
            address
          </Typography>
          {host.addressable ? (
            <Typography sx={{ fontFamily: "monospace", fontSize: "1.05rem" }}>{host.ip}</Typography>
          ) : (
            <Typography variant="body2" sx={{ color: "text.secondary", fontStyle: "italic" }}>
              hz has not detected one
            </Typography>
          )}
        </Box>
        {labels.length > 0 ? (
          <Box>
            <Typography
              variant="caption"
              sx={{ color: "text.secondary", textTransform: "uppercase", letterSpacing: 0.5, display: "block" }}
            >
              labels
            </Typography>
            <Typography sx={{ fontFamily: "monospace" }}>
              {labels.map(([k, v]) => `${k}=${v}`).join(" · ")}
            </Typography>
          </Box>
        ) : null}
      </Box>

      {host.addressable ? null : (
        <Note title="why there is no address here">{host.notAddressableWhy}</Note>
      )}
      {move.knowledge === "elsewhere" ? (
        <Note title="this address is not changed here">{move.why}</Note>
      ) : null}

      <Divider sx={{ my: 1.5 }} />

      <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap", mb: 0.25 }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
          What points at it
        </Typography>
        <ToneChip
          label={dependants.headline}
          tone={dependants.tone}
          hatched={dependants.knowledge === "unreferenced"}
        />
      </Box>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 0.5 }}>
        {dependants.meaning}
      </Typography>
      {dependants.knowledge === "referenced" ? (
        <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1.5 }}>
          {kindSummary(host.references)}
        </Typography>
      ) : null}
      {/* Said on every card, at both counts. A zero that is not qualified is a
          promise this screen cannot keep. */}
      <Note title="what this list does not include">{dependants.literalCaveat}</Note>

      {groupByKind(host.references).map((g) => (
        <KindGroup key={g.kind} group={g} />
      ))}
    </Paper>
  );
}

export function HostsScreen() {
  const query = useHostsView();
  const source = readHostsSource(query);
  const [moving, setMoving] = useState<HostView | null>(null);

  // ONE gate for every state that is not an answer, so the words come from one
  // place. A spinner beside the still-in-flight case and nothing beside the
  // failed one is the only difference — the sentences are `readHostsSource`'s,
  // which is what stops "hz has not said" being written twice and drifting.
  if (!source.known) {
    return (
      <Box sx={{ p: 3 }}>
        <ScreenHeading title="Hosts" blurb={HOSTS_BLURB} />
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
    <Box sx={{ p: 3 }}>
      <ScreenHeading title="Hosts" blurb={HOSTS_BLURB} />

      <Legend literalsUnlisted={source.literalsUnlisted} />

      {declared.length === 0 ? (
        <Alert severity="info" sx={{ mb: 2 }}>
          No host is declared besides @self. That is an empty list, not a failed
          read — nothing has been declared yet, so every address in this config
          is written out in full. Declare one on the Observability screen and
          records can start being written @name instead.
        </Alert>
      ) : null}

      {source.hosts.map((h) => (
        <HostCard
          key={h.self ? "@self" : h.name || `ip:${h.ip}`}
          host={h}
          literalsUnlisted={source.literalsUnlisted}
          onMove={setMoving}
        />
      ))}

      {moving ? <MoveHostDialog host={moving} onClose={() => setMoving(null)} /> : null}
    </Box>
  );
}

const HOSTS_BLURB =
  "Every address hz holds in one record, and every record that follows it. This answers one question: what points at this box, and what breaks if I move it. It is not the Machines screen — a machine is a box hz manages with an agent; a host here is an address other records resolve through, and the two lists do not have to match.";

export const Route = createFileRoute("/hosts")({
  component: HostsScreen,
});

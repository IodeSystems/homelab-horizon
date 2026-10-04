/**
 * DNS for a nested hz, on both sides of the link (N4h).
 *
 * PARENT (this hz owns the zone): a nested instance's page shows its DNS
 * GRANT — declared here, edited here — and the records this hz publishes for
 * it, each with its publish state. The instance asks; this hz publishes inside
 * the grant and refuses the rest. The last request's refusals are listed with
 * their reasons.
 *
 * CHILD (a parent owns the zone): the self row shows "DNS published by parent
 * <url>" and the parent's last answer per record — published, refused with
 * the reason, or pending.
 *
 * Declared values render plainly; observed ones carry their age (ui.md
 * Decision 3). Empty is one line (amendment 6).
 */
import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import type {
  InstanceDNSRecordStatus,
  InstanceDNSStatusResp,
  InstanceOwnedRecordResp,
  ParentDNSResp,
} from "../../api/generated-types";
import { useSetHZInstance } from "../../api/instanceHooks";
import { parseDomains } from "./InstanceTLS";

function at(unix: number | undefined): string {
  return unix && unix > 0 ? new Date(unix * 1000).toISOString().replace("T", " ").replace(/\.\d+Z$/, "Z") : "";
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <TableRow>
      <TableCell sx={{ color: "text.secondary", width: 160, verticalAlign: "top" }}>{label}</TableCell>
      <TableCell>{children}</TableCell>
    </TableRow>
  );
}

const STATUS_COLOR: Record<string, "success" | "warning" | "error" | "default"> = {
  published: "success",
  pending: "warning",
  refused: "error",
};

function StatusChip({ status }: { status: string }) {
  return <Chip size="small" variant="outlined" label={status} color={STATUS_COLOR[status] ?? "default"} data-dns-status={status} />;
}

/** The child's one line for a record, as its sync output prints it. */
export function parentDNSLine(r: InstanceDNSRecordStatus): string {
  const what = `${r.type} ${r.name} -> ${(r.values ?? []).join(", ")}`;
  switch (r.status) {
    case "published":
      return `${what}: published by parent${r.converted && r.reason ? ` (${r.reason})` : ""}`;
    case "refused":
      return `${what}: refused: ${r.reason ?? ""}`;
    default:
      return `${what}: pending${r.reason ? ` — ${r.reason}` : ""}`;
  }
}

function GrantDialog({ name, grant, open, onClose }: { name: string; grant: string[]; open: boolean; onClose: () => void }) {
  const set = useSetHZInstance();
  const [text, setText] = useState(grant.join("\n"));
  useEffect(() => {
    if (open) {
      setText(grant.join("\n"));
      set.reset();
    }
  }, [open, grant.join(",")]);
  const submit = () => set.mutate({ name, dnsGrant: parseDomains(text) }, { onSuccess: () => onClose() });
  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>
        DNS grant for <code>{name}</code>
      </DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ mb: 2 }}>
          {name} asks this hz to publish DNS; this hz publishes only inside these names and refuses the rest. An entry is an
          exact name or a <code>*.suffix</code> subtree, in a zone this hz owns with a DNS provider. Narrowing it removes the
          records {name} owns outside it.
        </Typography>
        <TextField
          fullWidth
          multiline
          minRows={3}
          label="Grant entries, one per line"
          value={text}
          onChange={(e) => setText(e.target.value)}
          sx={{ "& textarea": { fontFamily: "monospace" } }}
        />
        {set.error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            {set.error.message}
          </Alert>
        ) : null}
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

function OwnedRecords({ name, records }: { name: string; records: InstanceOwnedRecordResp[] }) {
  if (records.length === 0) {
    return (
      <Typography variant="body2" data-instance-dns-records="none" sx={{ color: "text.secondary" }}>
        No DNS published for {name}.
      </Typography>
    );
  }
  return (
    <Table size="small" data-instance-dns-records={records.length}>
      <TableHead>
        <TableRow>
          <TableCell>Name</TableCell>
          <TableCell>Type</TableCell>
          <TableCell>Values</TableCell>
          <TableCell>Status</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {records.map((r) => (
          <TableRow key={`${r.zone}|${r.name}|${r.type}`} data-instance-dns-record={r.name}>
            <TableCell sx={{ fontFamily: "monospace" }}>{r.name}</TableCell>
            <TableCell>{r.type}</TableCell>
            <TableCell sx={{ fontFamily: "monospace" }}>{(r.values ?? []).join(", ")}</TableCell>
            <TableCell>
              <StatusChip status={r.status} />
              {r.reason ? (
                <Typography variant="caption" sx={{ display: "block", color: "text.secondary" }}>
                  {r.reason}
                </Typography>
              ) : null}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/** The parent's facts about a nested instance's DNS: the grant, the records, the last request. */
export function InstanceDNSFacts({ name, dns }: { name: string; dns?: InstanceDNSStatusResp }) {
  const [open, setOpen] = useState(false);
  const grant = dns?.grant ?? [];
  const records = dns?.records ?? [];
  const refused = dns?.lastRefused ?? [];
  return (
    <>
      <Fact label="DNS grant">
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap" }}>
          {grant.length === 0 ? (
            <Typography variant="body2" data-dns-grant="none" sx={{ color: "text.secondary" }}>
              No DNS grant for {name} — this hz publishes nothing it asks for.
            </Typography>
          ) : (
            <Typography variant="body2" data-dns-grant={grant.join(",")} sx={{ fontFamily: "monospace" }}>
              {grant.join(", ")}
            </Typography>
          )}
          <Button
            size="small"
            variant="outlined"
            sx={{ textTransform: "none" }}
            aria-label={grant.length === 0 ? "Set DNS grant" : "Edit DNS grant"}
            onClick={() => setOpen(true)}
          >
            {grant.length === 0 ? "Set grant" : "Edit grant"}
          </Button>
        </Box>
        <GrantDialog name={name} grant={grant} open={open} onClose={() => setOpen(false)} />
      </Fact>
      <Fact label="DNS records">
        <OwnedRecords name={name} records={records} />
      </Fact>
      <Fact label="Last DNS request">
        {dns?.lastRequestAt ? (
          <Typography variant="body2" data-dns-last-request="asked">
            {at(dns.lastRequestAt)}
            {dns.lastPublicIp ? ` · its public IP ${dns.lastPublicIp}` : ""}
          </Typography>
        ) : (
          <Typography variant="body2" data-dns-last-request="not-since" sx={{ color: "text.secondary" }}>
            not since this hz started
          </Typography>
        )}
        {refused.map((r) => (
          <Typography key={`${r.name}|${r.type}`} variant="body2" sx={{ color: "error.main" }} data-dns-refused={r.name}>
            refused {r.type} {r.name}: {r.reason}
          </Typography>
        ))}
      </Fact>
    </>
  );
}

/** The child's state of its parent's DNS, for the page and a test. */
export function parentDNSState(pd: ParentDNSResp): { state: string; text: string } {
  if (!pd.enabled) return { state: "off", text: `this hz asks parent ${pd.parent} for no DNS — ${pd.off ?? ""}` };
  if (pd.unasked) return { state: "unasked", text: `not asked: ${pd.unasked}` };
  if (!pd.lastAnswerAt) {
    return { state: "none", text: `parent ${pd.parent} has not answered yet${pd.lastError ? ` — last attempt: ${pd.lastError}` : ""}` };
  }
  if ((pd.records ?? []).length === 0) return { state: "empty", text: `nothing asked of parent ${pd.parent}: no service here is in a zone it owns` };
  return { state: "answered", text: `DNS published by parent ${pd.parent}, as of ${at(pd.lastAnswerAt)}` };
}

export function ParentDNSFact({ pd }: { pd: ParentDNSResp }) {
  const line = parentDNSState(pd);
  return (
    <Fact label="DNS">
      <Typography
        variant="body2"
        data-parent-dns={line.state}
        sx={{ color: line.state === "answered" ? undefined : "text.secondary" }}
      >
        {line.text}
      </Typography>
      {(pd.records ?? []).map((r) => (
        <Box key={`${r.name}|${r.type}`} sx={{ display: "flex", alignItems: "center", gap: 1, mt: 0.5 }} data-parent-dns-record={r.name}>
          <StatusChip status={r.status} />
          <Typography variant="body2" sx={{ fontFamily: "monospace", color: r.status === "refused" ? "error.main" : undefined }}>
            {parentDNSLine(r)}
          </Typography>
        </Box>
      ))}
      {pd.lastAnswerAt && pd.lastError ? (
        <Typography variant="body2" sx={{ color: "warning.main" }} data-parent-dns-error>
          last attempt {at(pd.lastAttemptAt)}: {pd.lastError} — the answer above is the last one the parent gave
        </Typography>
      ) : null}
    </Fact>
  );
}

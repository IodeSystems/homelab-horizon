/**
 * A nested hz's ONE registered domain (N4j) and the names derived from it.
 *
 *   <domain>            public    the app
 *   kiosk.hz.<domain>   public    the MFA kiosk and the VPN endpoint
 *   hz.<domain>         VPN-only  the child's UI; never published
 *
 * The derivation is the server's (config.HZInstance.TLSDomains): this screen
 * renders the rows it sends, and previews the names in the editor only as a
 * statement of what saving will do. An instance with no domain gets a one-line
 * nudge and a [Set domain] button (ui.md amendment 6: flows over prose).
 */
import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
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
import type { InstanceNameResp } from "../../api/generated-types";
import { useSetHZInstance } from "../../api/instanceHooks";

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <TableRow>
      <TableCell sx={{ color: "text.secondary", width: 160 }}>{label}</TableCell>
      <TableCell>{children}</TableCell>
    </TableRow>
  );
}

/** What saving `domain` will derive — a preview; the server decides. */
export function previewNames(domain: string): { name: string; scope: string }[] {
  const d = domain.trim().toLowerCase().replace(/\.$/, "");
  if (d === "") return [];
  return [
    { name: d, scope: "public" },
    { name: `kiosk.hz.${d}`, scope: "public" },
    { name: `hz.${d}`, scope: "vpn-only" },
  ];
}

/** The record cell's words, for the page and a test. */
export function recordText(n: InstanceNameResp): string {
  switch (n.record) {
    case "never":
      return "never published";
    case "published":
      return "published";
    case "pending":
      return "pending";
    case "none":
      return "not asked for";
    default:
      return n.record;
  }
}

function DomainDialog({ name, domain, open, onClose }: { name: string; domain: string; open: boolean; onClose: () => void }) {
  const set = useSetHZInstance();
  const [text, setText] = useState(domain);
  useEffect(() => {
    if (open) {
      setText(domain);
      set.reset();
    }
  }, [open, domain]);
  const submit = () => set.mutate({ name, domain: text.trim() }, { onSuccess: () => onClose() });
  const preview = previewNames(text);
  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>
        Domain for <code>{name}</code>
      </DialogTitle>
      <DialogContent>
        <TextField
          fullWidth
          autoFocus
          label="Domain"
          placeholder="loadtest.redline.example.com"
          value={text}
          onChange={(e) => setText(e.target.value)}
          sx={{ mt: 1, "& input": { fontFamily: "monospace" } }}
        />
        {preview.length > 0 ? (
          <Typography variant="body2" component="div" sx={{ mt: 2 }}>
            Saving derives these names, one certificate for all three, and the DNS grant{" "}
            <code>{preview[0]!.name}</code>, <code>*.{preview[0]!.name}</code>. Explicit domains and grant are replaced, and
            records outside them are retracted.
            <Box component="ul" sx={{ fontFamily: "monospace", mb: 0 }}>
              {preview.map((p) => (
                <li key={p.name}>
                  {p.name} — {p.scope}
                </li>
              ))}
            </Box>
          </Typography>
        ) : null}
        {set.error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            {set.error.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={set.isPending || text.trim() === ""}>
          {set.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

/** The parent's facts about a nested instance's domain and its derived names. */
export function InstanceDomainFacts({ name, domain, names }: { name: string; domain?: string; names?: InstanceNameResp[] }) {
  const [open, setOpen] = useState(false);
  const d = domain ?? "";
  const rows = names ?? [];
  return (
    <>
      <Fact label="Domain">
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap" }}>
          {d === "" ? (
            <Typography variant="body2" data-instance-domain="none" sx={{ color: "text.secondary" }}>
              No domain for {name} — its names and grant are set by hand.
            </Typography>
          ) : (
            <Typography variant="body2" data-instance-domain={d} sx={{ fontFamily: "monospace" }}>
              {d}
            </Typography>
          )}
          <Button
            size="small"
            variant={d === "" ? "contained" : "outlined"}
            sx={{ textTransform: "none" }}
            aria-label={d === "" ? "Set a domain" : "Edit domain"}
            onClick={() => setOpen(true)}
          >
            {d === "" ? "Set a domain" : "Edit domain"}
          </Button>
        </Box>
        <DomainDialog name={name} domain={d} open={open} onClose={() => setOpen(false)} />
      </Fact>
      {rows.length === 0 ? null : (
        <Fact label="Names">
          <Table size="small" data-instance-names>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Scope</TableCell>
                <TableCell>Public record</TableCell>
                <TableCell>In certificate</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map((n) => (
                <TableRow key={n.name} data-instance-name={n.name} data-name-scope={n.scope}>
                  <TableCell sx={{ fontFamily: "monospace" }}>{n.name}</TableCell>
                  <TableCell>{n.scope === "vpn-only" ? "VPN-only" : "public"}</TableCell>
                  <TableCell data-name-record={n.record} sx={{ color: n.record === "pending" ? "warning.main" : n.record === "never" ? "text.secondary" : undefined }}>
                    {recordText(n)}
                  </TableCell>
                  <TableCell data-name-in-cert={n.inCert ? "yes" : "no"}>{n.inCert ? "yes" : "no"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Fact>
      )}
    </>
  );
}

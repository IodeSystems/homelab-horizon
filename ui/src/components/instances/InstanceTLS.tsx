/**
 * TLS for a nested hz's own hostnames, on both sides of the link.
 *
 * PARENT (this hz owns the zone and issues): a nested instance's page shows
 * its declared domains, the ONE certificate issued for exactly those names
 * (issued / not after / last pulled by the child) and an editor for the
 * domains. The private key is never shown or sent to the browser — the wire
 * type for this screen has no field for it.
 *
 * CHILD (this hz pulls): the self row shows "certificate from parent <url>,
 * not after …". Observed values carry their age (ui.md Decision 3).
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
  TableCell,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import type { InstanceCertStatusResp, ParentCertResp } from "../../api/generated-types";
import { useSetHZInstance } from "../../api/instanceHooks";

function at(unix: number | undefined): string {
  return unix && unix > 0 ? new Date(unix * 1000).toISOString().replace("T", " ").replace(/\.\d+Z$/, "Z") : "";
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <TableRow>
      <TableCell sx={{ color: "text.secondary", width: 160 }}>{label}</TableCell>
      <TableCell>{children}</TableCell>
    </TableRow>
  );
}

/** Domains typed one per line or comma-separated, trimmed, empties dropped. */
export function parseDomains(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((d) => d.trim())
    .filter((d) => d !== "");
}

/** The one-line state of the parent-side certificate, for the page and a test. */
export function certLine(domains: string[], cert: InstanceCertStatusResp | undefined): { state: string; text: string } {
  if (domains.length === 0) return { state: "none", text: "no domains — the instance serves http only" };
  if (!cert) return { state: "unknown", text: "not reported" };
  if (cert.unreadable) return { state: "unreadable", text: `on file but unreadable: ${cert.unreadable}` };
  if (!cert.issued) {
    return cert.lastError
      ? { state: "failed", text: `not issued — last attempt ${at(cert.lastAttemptAt)} failed: ${cert.lastError}` }
      : { state: "pending", text: "not issued yet — this hz requests it in the background" };
  }
  const covers = (cert.sans ?? []).join(", ");
  const stale = cert.sha256 ? "" : " (its names differ from the declared domains — a reissue is pending)";
  return { state: "issued", text: `issued for ${covers}, not after ${at(cert.notAfter)}${stale}` };
}

function DomainsDialog({ name, domains, open, onClose }: { name: string; domains: string[]; open: boolean; onClose: () => void }) {
  const set = useSetHZInstance();
  const [text, setText] = useState(domains.join("\n"));
  useEffect(() => {
    if (open) {
      setText(domains.join("\n"));
      set.reset();
    }
  }, [open, domains.join(",")]);
  const submit = () => set.mutate({ name, domains: parseDomains(text) }, { onSuccess: () => onClose() });
  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>
        TLS domains for <code>{name}</code>
      </DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ mb: 2 }}>
          This hz issues one certificate for exactly these names, through its own zone&apos;s DNS provider. {name} pulls
          the certificate and key with its instance token; the DNS credential stays here.
        </Typography>
        <TextField
          fullWidth
          multiline
          minRows={3}
          label="Domains, one per line"
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

/** The parent's facts about a nested instance's TLS: domains, the certificate, the child's pull. */
export function InstanceCertFacts({ name, domains, cert }: { name: string; domains: string[]; cert?: InstanceCertStatusResp }) {
  const [open, setOpen] = useState(false);
  const line = certLine(domains, cert);
  return (
    <>
      <Fact label="TLS domains">
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap" }}>
          {domains.length === 0 ? (
            <Typography variant="body2" data-tls-domains="none" sx={{ color: "text.secondary" }}>
              No TLS domains for {name}.
            </Typography>
          ) : (
            <Typography variant="body2" data-tls-domains={domains.join(",")} sx={{ fontFamily: "monospace" }}>
              {domains.join(", ")}
            </Typography>
          )}
          <Button
            size="small"
            variant="outlined"
            sx={{ textTransform: "none" }}
            aria-label={domains.length === 0 ? "Set TLS domains" : "Edit TLS domains"}
            onClick={() => setOpen(true)}
          >
            {domains.length === 0 ? "Set domains" : "Edit domains"}
          </Button>
        </Box>
        <DomainsDialog name={name} domains={domains} open={open} onClose={() => setOpen(false)} />
      </Fact>
      {domains.length === 0 ? null : (
        <>
          <Fact label="Certificate">
            <Typography
              variant="body2"
              data-cert-state={line.state}
              sx={{ color: line.state === "failed" || line.state === "unreadable" ? "error.main" : undefined }}
            >
              {line.text}
            </Typography>
            {line.state === "issued" && cert?.lastError ? (
              <Typography variant="body2" sx={{ color: "warning.main" }} data-cert-renewal-failed>
                last renewal attempt {at(cert.lastAttemptAt)} failed: {cert.lastError} — the certificate on file is still served
              </Typography>
            ) : null}
          </Fact>
          <Fact label="Pulled by the child">
            {cert?.lastPulledAt ? (
              <Typography variant="body2" data-cert-pulled="pulled">
                {at(cert.lastPulledAt)}
                {cert.sha256 && cert.lastPulledSha256 !== cert.sha256 ? " — an older certificate; the newer one is not pulled yet" : ""}
              </Typography>
            ) : (
              <Typography variant="body2" data-cert-pulled="not-since" sx={{ color: "text.secondary" }}>
                not since this hz started
              </Typography>
            )}
          </Fact>
        </>
      )}
    </>
  );
}

/** The child's line about the certificate its parent issued. */
export function parentCertLine(pc: ParentCertResp): { state: string; text: string } {
  if (!pc.instance) {
    return { state: "off", text: `no certificate is pulled from parent ${pc.parent} — upstream.instance is not set` };
  }
  if (!pc.installed) {
    return {
      state: "none",
      text: `no certificate from parent ${pc.parent} yet${pc.lastError ? ` — last attempt: ${pc.lastError}` : ""}`,
    };
  }
  const now = Date.now() / 1000;
  const expired = (pc.notAfter ?? 0) > 0 && (pc.notAfter ?? 0) <= now;
  return {
    state: expired ? "expired" : "installed",
    text: `certificate from parent ${pc.parent}, not after ${at(pc.notAfter)}${expired ? " — EXPIRED, still served" : ""}`,
  };
}

export function ParentCertFact({ pc }: { pc: ParentCertResp }) {
  const line = parentCertLine(pc);
  return (
    <Fact label="TLS">
      <Typography
        variant="body2"
        data-parent-cert={line.state}
        sx={{ color: line.state === "expired" ? "error.main" : line.state === "installed" ? undefined : "text.secondary" }}
      >
        {line.text}
      </Typography>
      {line.state === "installed" || line.state === "expired" ? (
        <Typography variant="caption" sx={{ color: "text.secondary", fontFamily: "monospace" }}>
          {(pc.sans ?? []).join(", ")}
        </Typography>
      ) : null}
      {pc.installed && pc.lastError ? (
        <Typography variant="body2" sx={{ color: "warning.main" }}>
          last check {at(pc.lastCheckAt)}: {pc.lastError} — the installed certificate keeps serving
        </Typography>
      ) : null}
    </Fact>
  );
}

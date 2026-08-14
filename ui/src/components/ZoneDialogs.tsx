// Zone create/edit dialogs and the certificate chip.
//
// These live beside the DNS page rather than in Settings: zones used to be
// managed in two places, and the one showing the records — the page people
// actually open — was the one without an add button. It told you to go to
// Settings instead, which is how someone ends up unable to find it at all.
//
// The provider credential form comes with them deliberately. Splitting "add a
// zone" from "give it credentials" would recreate the same two-places problem
// in miniature.
import { useState } from "react";
import {
  Alert,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  Select,
  TextField,
  Tooltip,
} from "@mui/material";
import { useAddZone, useEditZone, useSystemHealth } from "../api/hooks";
import type { Zone } from "../api/types";

export function AddZoneDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const addZone = useAddZone();
  const [form, setForm] = useState({
    name: "",
    zoneId: "",
    providerType: "route53",
    sslEmail: "",
    awsProfile: "",
    awsAccessKeyId: "",
    awsSecretAccessKey: "",
    awsRegion: "",
    namecomUsername: "",
    namecomApiToken: "",
    cloudflareApiToken: "",
  });

  const handleSubmit = () => {
    addZone.mutate(form, {
      onSuccess: () => {
        onClose();
        setForm({
          name: "",
          zoneId: "",
          providerType: "route53",
          sslEmail: "",
          awsProfile: "",
          awsAccessKeyId: "",
          awsSecretAccessKey: "",
          awsRegion: "",
          namecomUsername: "",
          namecomApiToken: "",
          cloudflareApiToken: "",
        });
      },
    });
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Add Zone</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Domain Name"
          value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })}
          placeholder="example.com"
          size="small"
          fullWidth
        />
        <TextField
          label="Zone ID"
          value={form.zoneId}
          onChange={(e) => setForm({ ...form, zoneId: e.target.value })}
          placeholder="Z123ABC..."
          size="small"
          fullWidth
        />
        <Select
          value={form.providerType}
          onChange={(e) => setForm({ ...form, providerType: e.target.value })}
          size="small"
          fullWidth
        >
          <MenuItem value="route53">AWS Route53</MenuItem>
          <MenuItem value="namecom">Name.com</MenuItem>
          <MenuItem value="cloudflare">Cloudflare</MenuItem>
        </Select>

        {form.providerType === "route53" && (
          <>
            <TextField
              label="AWS Profile"
              value={form.awsProfile}
              onChange={(e) => setForm({ ...form, awsProfile: e.target.value })}
              size="small"
              fullWidth
            />
            <TextField
              label="AWS Access Key ID"
              value={form.awsAccessKeyId}
              onChange={(e) => setForm({ ...form, awsAccessKeyId: e.target.value })}
              size="small"
              fullWidth
            />
            <TextField
              label="AWS Secret Access Key"
              value={form.awsSecretAccessKey}
              onChange={(e) => setForm({ ...form, awsSecretAccessKey: e.target.value })}
              size="small"
              fullWidth
              type="password"
            />
            <TextField
              label="AWS Region"
              value={form.awsRegion}
              onChange={(e) => setForm({ ...form, awsRegion: e.target.value })}
              size="small"
              fullWidth
              placeholder="us-east-1"
            />
          </>
        )}
        {form.providerType === "namecom" && (
          <>
            <TextField
              label="Name.com Username"
              value={form.namecomUsername}
              onChange={(e) => setForm({ ...form, namecomUsername: e.target.value })}
              size="small"
              fullWidth
            />
            <TextField
              label="Name.com API Token"
              value={form.namecomApiToken}
              onChange={(e) => setForm({ ...form, namecomApiToken: e.target.value })}
              size="small"
              fullWidth
              type="password"
            />
          </>
        )}
        {form.providerType === "cloudflare" && (
          <TextField
            label="Cloudflare API Token"
            value={form.cloudflareApiToken}
            onChange={(e) => setForm({ ...form, cloudflareApiToken: e.target.value })}
            size="small"
            fullWidth
            type="password"
          />
        )}
        <TextField
          label="SSL Email"
          value={form.sslEmail}
          onChange={(e) => setForm({ ...form, sslEmail: e.target.value })}
          placeholder="admin@example.com"
          size="small"
          fullWidth
          helperText="Leave empty to disable SSL for this zone"
        />
        {addZone.isError && (
          <Alert severity="error">{(addZone.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!form.name || addZone.isPending}
        >
          {addZone.isPending ? <CircularProgress size={20} /> : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function EditZoneDialog({
  zone,
  onClose,
}: {
  zone: Zone | null;
  onClose: () => void;
}) {
  const editZone = useEditZone();
  const [sslEmail, setSSLEmail] = useState(zone?.sslEmail ?? "");
  const [subZones, setSubZones] = useState(zone?.subZones?.join(", ") ?? "");

  // Sync state when zone changes
  const [lastZone, setLastZone] = useState<string | null>(null);
  if (zone && zone.name !== lastZone) {
    setSSLEmail(zone.sslEmail ?? "");
    setSubZones(zone.subZones?.join(", ") ?? "");
    setLastZone(zone.name);
  }

  if (!zone) return null;

  const handleSubmit = () => {
    editZone.mutate(
      {
        originalName: zone.name,
        sslEmail,
        subZones,
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Edit Zone: {zone.name}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="SSL Email"
          value={sslEmail}
          onChange={(e) => setSSLEmail(e.target.value)}
          size="small"
          fullWidth
          helperText="Clear to disable SSL"
        />
        <TextField
          label="Sub-Zones"
          value={subZones}
          onChange={(e) => setSubZones(e.target.value)}
          size="small"
          fullWidth
          placeholder="*, *.vpn"
          helperText="Comma-separated. e.g. *, *.vpn"
        />
        {editZone.isError && (
          <Alert severity="error">{(editZone.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={editZone.isPending}
        >
          {editZone.isPending ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// certForZone finds the LE cert entry (from /system/health) whose domains
// belong to the given zone. Matches if the primary domain equals the zone
// name or ends with ".<zoneName>" — covers both the bare-apex case
// (veliode.com cert for veliode.com zone) and the wildcard-primary case
// (*.vpn.iodesystems.com cert for iodesystems.com zone).
function certForZone(
  zoneName: string,
  domains: Array<{ domain: string; cert_exists: boolean; expiry_info?: string; needs_renewal?: boolean; sans?: string[] }>,
) {
  return domains.find(
    (d) => d.domain === zoneName || d.domain.endsWith("." + zoneName),
  );
}

export function ZoneCertChip({ zoneName }: { zoneName: string }) {
  const { data: health } = useSystemHealth();
  if (!health) return null;
  const leComponent = health.components.find((c) => c.name === "letsencrypt");
  const domains = ((leComponent?.extras as { domains?: Array<{ domain: string; cert_exists: boolean; expiry_info?: string; needs_renewal?: boolean; sans?: string[] }> } | undefined)?.domains) ?? [];
  const cert = certForZone(zoneName, domains);
  if (!cert) {
    // SSL disabled on this zone, or no SubZones configured.
    return <Chip label="—" size="small" variant="outlined" sx={{ opacity: 0.5 }} />;
  }
  if (!cert.cert_exists) {
    return <Chip label="missing" size="small" color="error" />;
  }
  if (cert.needs_renewal) {
    return (
      <Tooltip title={cert.expiry_info || "renew soon"}>
        <Chip label="renew soon" size="small" color="warning" />
      </Tooltip>
    );
  }
  return (
    <Tooltip title={cert.expiry_info || "present"}>
      <Chip label="ok" size="small" color="success" variant="outlined" />
    </Tooltip>
  );
}

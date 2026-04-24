import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControl,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import {
  useAptAudit,
  useCreateWGConfig,
  useEnableHorizon,
  useFixHAProxyLogging,
  useFixIPForwarding,
  useFixMasquerade,
  useFixWGForwardChain,
  useFixWGRules,
  useInstallHorizonUnit,
  useInstallPackage,
  useReloadDNSMasq,
  useStartDNSMasq,
  useSystemHealth,
  useWriteDNSMasqConfig,
} from "../api/hooks";
import type { ComponentHealth, SystemHealth } from "../api/types";

// CheckRow renders one line: label + status chip + optional fix button.
// Keep the shape uniform across all component cards so the dashboard reads
// as a consistent grid.
function CheckRow({
  label,
  ok,
  failingLabel,
  okLabel = "OK",
  fix,
  fixLabel = "Fix",
  fixDisabled,
  fixRunning,
}: {
  label: string;
  ok: boolean;
  failingLabel?: string;
  okLabel?: string;
  fix?: () => void;
  fixLabel?: string;
  fixDisabled?: boolean;
  fixRunning?: boolean;
}) {
  return (
    <Stack
      direction="row"
      alignItems="center"
      spacing={2}
      sx={{ py: 0.75, borderBottom: 1, borderColor: "divider" }}
    >
      <Typography variant="body2" sx={{ flex: 1 }}>
        {label}
      </Typography>
      <Chip
        size="small"
        label={ok ? okLabel : (failingLabel ?? "Missing")}
        color={ok ? "success" : "error"}
        variant={ok ? "outlined" : "filled"}
      />
      {!ok && fix && (
        <Button
          size="small"
          variant="contained"
          onClick={fix}
          disabled={fixDisabled || fixRunning}
        >
          {fixRunning ? <CircularProgress size={16} /> : fixLabel}
        </Button>
      )}
    </Stack>
  );
}

// ComponentCard wraps per-component health into a card with a header status
// chip. Overall status is a rough summary: any failing sub-check → error.
function ComponentCard({
  title,
  component,
  children,
}: {
  title: string;
  component: ComponentHealth | undefined;
  children: React.ReactNode;
}) {
  if (!component) return null;
  const anyError = (component.errors ?? []).length > 0;
  return (
    <Card variant="outlined">
      <CardHeader
        title={
          <Stack direction="row" spacing={1} alignItems="center">
            <Typography variant="h6">{title}</Typography>
            {component.version && (
              <Typography variant="caption" color="text.secondary">
                {component.version}
              </Typography>
            )}
            <Box sx={{ flex: 1 }} />
            <Chip
              size="small"
              label={anyError ? "Issues" : "Healthy"}
              color={anyError ? "error" : "success"}
              variant={anyError ? "filled" : "outlined"}
            />
          </Stack>
        }
        sx={{ pb: 0 }}
      />
      <CardContent>{children}</CardContent>
    </Card>
  );
}

function byName(health: SystemHealth | undefined, name: string): ComponentHealth | undefined {
  return health?.components.find((c) => c.name === name);
}

// SystemLevelCard — host-wide bits that don't fit a single component: IP
// forwarding, horizon's own systemd unit.
function SystemLevelCard({ health }: { health: SystemHealth }) {
  const fixIPF = useFixIPForwarding();
  const installUnit = useInstallHorizonUnit();
  const enableHorizon = useEnableHorizon();

  return (
    <Card variant="outlined">
      <CardHeader title={<Typography variant="h6">System</Typography>} sx={{ pb: 0 }} />
      <CardContent>
        <CheckRow
          label="IP forwarding (sysctl net.ipv4.ip_forward)"
          ok={health.ip_forwarding}
          failingLabel={health.ip_forwarding_error || "Disabled"}
          fix={() => fixIPF.mutate()}
          fixRunning={fixIPF.isPending}
        />
        <CheckRow
          label="horizon systemd unit installed"
          ok={health.horizon_unit_installed}
          okLabel="Installed"
          failingLabel="Missing"
          fix={() => installUnit.mutate()}
          fixRunning={installUnit.isPending}
          fixLabel="Install unit"
        />
        <CheckRow
          label="horizon enabled at boot"
          ok={health.horizon_enabled}
          okLabel="Enabled"
          failingLabel="Disabled"
          fix={() => enableHorizon.mutate()}
          fixDisabled={!health.horizon_unit_installed}
          fixRunning={enableHorizon.isPending}
          fixLabel="Enable"
        />
        <CheckRow
          label="horizon currently running"
          ok={health.horizon_running}
          okLabel="Active"
          failingLabel="Inactive"
        />
      </CardContent>
    </Card>
  );
}

// Fixer buttons share a standard shape: install → create-config → start →
// enable. Disable downstream fixers when their prereq isn't met so the UI
// nudges the admin through the right order.
function WireGuardCard({ health }: { health: SystemHealth }) {
  const wg = byName(health, "wireguard");
  const install = useInstallPackage();
  const createConfig = useCreateWGConfig();
  const fixMasq = useFixMasquerade();
  const fixChain = useFixWGForwardChain();
  const fixRules = useFixWGRules();

  if (!wg) return null;
  const extras = wg.extras ?? {};
  return (
    <ComponentCard title="WireGuard" component={wg}>
      <CheckRow
        label="wg binary installed"
        ok={wg.installed}
        fix={() => install.mutate("wireguard-tools")}
        fixRunning={install.isPending}
        fixLabel="Install"
      />
      <CheckRow
        label="wg0.conf exists"
        ok={wg.config_exists}
        fix={() => createConfig.mutate()}
        fixDisabled={!wg.installed}
        fixRunning={createConfig.isPending}
        fixLabel="Create config"
      />
      <CheckRow
        label="Interface up"
        ok={Boolean(extras.interface_up)}
        okLabel="Up"
        failingLabel="Down"
        fix={() => fixRules.mutate()}
        fixDisabled={!wg.config_exists}
        fixRunning={fixRules.isPending}
        fixLabel="Regen rules + restart"
      />
      <CheckRow
        label="IP forwarding sysctl"
        ok={Boolean(extras.ip_forwarding)}
        okLabel="Enabled"
        failingLabel="Disabled"
      />
      <CheckRow
        label="MASQUERADE rule present"
        ok={Boolean(extras.masquerading)}
        okLabel="Present"
        failingLabel="Missing"
        fix={() => fixMasq.mutate()}
        fixDisabled={!wg.installed}
        fixRunning={fixMasq.isPending}
        fixLabel="Add rule"
      />
      <Box sx={{ mt: 1 }}>
        <Button
          size="small"
          variant="outlined"
          onClick={() => fixChain.mutate()}
          disabled={!wg.installed || fixChain.isPending}
        >
          {fixChain.isPending ? <CircularProgress size={16} /> : "Rebuild WG-FORWARD chain"}
        </Button>
      </Box>
      {wg.errors && wg.errors.length > 0 && (
        <Alert severity="warning" sx={{ mt: 2 }}>
          <Stack spacing={0.5}>
            {wg.errors.map((e, i) => (
              <Typography key={i} variant="body2">
                {e}
              </Typography>
            ))}
          </Stack>
        </Alert>
      )}
    </ComponentCard>
  );
}

function HAProxyCard({ health }: { health: SystemHealth }) {
  const hap = byName(health, "haproxy");
  const install = useInstallPackage();
  const fixLogging = useFixHAProxyLogging();

  if (!hap) return null;
  const extras = hap.extras ?? {};
  const loggingOK = Boolean(extras.logging_apparmor_ok) && Boolean(extras.logging_file_exists);

  return (
    <ComponentCard title="HAProxy" component={hap}>
      <CheckRow
        label="haproxy binary installed"
        ok={hap.installed}
        fix={() => install.mutate("haproxy")}
        fixRunning={install.isPending}
        fixLabel="Install"
      />
      <CheckRow label="Config exists" ok={hap.config_exists} />
      <CheckRow label="Enabled at boot" ok={hap.enabled} okLabel="Enabled" failingLabel="Disabled" />
      <CheckRow label="Running" ok={hap.running} okLabel="Active" failingLabel="Inactive" />
      <CheckRow
        label="Logging (apparmor attach_disconnected + /var/log/haproxy.log)"
        ok={loggingOK}
        okLabel="OK"
        failingLabel="Broken"
        fix={() => fixLogging.mutate()}
        fixDisabled={!hap.installed}
        fixRunning={fixLogging.isPending}
        fixLabel="Fix logging"
      />
      {hap.errors && hap.errors.length > 0 && (
        <Alert severity="warning" sx={{ mt: 2 }}>
          <Stack spacing={0.5}>
            {hap.errors.map((e, i) => (
              <Typography key={i} variant="body2">
                {e}
              </Typography>
            ))}
          </Stack>
        </Alert>
      )}
    </ComponentCard>
  );
}

function DNSMasqCard({ health }: { health: SystemHealth }) {
  const dns = byName(health, "dnsmasq");
  const install = useInstallPackage();
  const writeConfig = useWriteDNSMasqConfig();
  const reload = useReloadDNSMasq();
  const start = useStartDNSMasq();

  if (!dns) return null;
  const extras = dns.extras ?? {};
  // Listen-address drift is the dnsmasq analog of the iptables
  // "LocalInterface changed without reload" case — config file says bind to
  // X, cfg says the interface IP is Y. Reload endpoint regenerates config
  // and restarts dnsmasq so it rebinds on Y. Undefined = drift check didn't
  // run (ConfigExists was false), so we treat it as "not failing."
  const listenAddrOK = extras.listen_address_matches_local_interface !== false;
  return (
    <ComponentCard title="dnsmasq" component={dns}>
      <CheckRow
        label="dnsmasq binary installed"
        ok={dns.installed}
        fix={() => install.mutate("dnsmasq")}
        fixRunning={install.isPending}
        fixLabel="Install"
      />
      <CheckRow
        label="Config exists"
        ok={dns.config_exists}
        fix={() => writeConfig.mutate()}
        fixDisabled={!dns.installed}
        fixRunning={writeConfig.isPending}
        fixLabel="Write config"
      />
      <CheckRow
        label="Enabled at boot"
        ok={dns.enabled}
        okLabel="Enabled"
        failingLabel="Disabled"
      />
      <CheckRow
        label="Running"
        ok={dns.running}
        okLabel="Active"
        failingLabel="Inactive"
        fix={() => start.mutate()}
        fixDisabled={!dns.installed}
        fixRunning={start.isPending}
        fixLabel="Start"
      />
      <CheckRow
        label="Listen address matches LocalInterface"
        ok={listenAddrOK}
        okLabel="Match"
        failingLabel="Drifted"
        fix={() => reload.mutate()}
        fixDisabled={!dns.config_exists}
        fixRunning={reload.isPending}
        fixLabel="Write + reload"
      />
      <Box sx={{ mt: 1 }}>
        <Button
          size="small"
          variant="outlined"
          onClick={() => reload.mutate()}
          disabled={!dns.running || reload.isPending}
        >
          {reload.isPending ? <CircularProgress size={16} /> : "Reload (pick up config changes)"}
        </Button>
      </Box>
      {dns.errors && dns.errors.length > 0 && (
        <Alert severity="warning" sx={{ mt: 2 }}>
          <Stack spacing={0.5}>
            {dns.errors.map((e, i) => (
              <Typography key={i} variant="body2">
                {e}
              </Typography>
            ))}
          </Stack>
        </Alert>
      )}
    </ComponentCard>
  );
}

function LetsEncryptCard({ health }: { health: SystemHealth }) {
  const le = byName(health, "letsencrypt");
  if (!le) return null;
  const extras = le.extras ?? {};
  if (extras.disabled) {
    return (
      <ComponentCard title="Let's Encrypt" component={le}>
        <Alert severity="info">SSL is disabled in config. Enable on the SSL tab to manage certs.</Alert>
      </ComponentCard>
    );
  }
  const domains = (extras.domains ?? []) as Array<{
    domain: string;
    cert_exists: boolean;
    expiry_info?: string;
    provider?: string;
    needs_renewal?: boolean;
  }>;
  return (
    <ComponentCard title="Let's Encrypt" component={le}>
      {domains.length === 0 && (
        <Typography variant="body2" color="text.secondary">
          No SSL domains configured.
        </Typography>
      )}
      {domains.length > 0 && (
        <TableContainer>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Domain</TableCell>
                <TableCell>Provider</TableCell>
                <TableCell>Cert</TableCell>
                <TableCell>Expires</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {domains.map((d) => (
                <TableRow key={d.domain}>
                  <TableCell sx={{ fontFamily: "monospace" }}>{d.domain}</TableCell>
                  <TableCell>{d.provider || "—"}</TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      label={d.cert_exists ? "present" : "missing"}
                      color={d.cert_exists ? "success" : "error"}
                      variant={d.cert_exists ? "outlined" : "filled"}
                    />
                  </TableCell>
                  <TableCell>
                    {d.needs_renewal ? (
                      <Chip size="small" color="warning" label={d.expiry_info || "renew soon"} />
                    ) : (
                      <Typography variant="caption">{d.expiry_info || "—"}</Typography>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: "block" }}>
        Request a new cert from the SSL tab if any row above is missing or due for renewal.
      </Typography>
    </ComponentCard>
  );
}

// AptInstallCard lists the apt-audit log so the admin can see what's been
// installed, when, and whether it succeeded. No install button here —
// per-component cards each have their own install button for the relevant
// package. This card is read-only history.
function AptInstallCard() {
  const { data } = useAptAudit();
  const entries = data?.entries ?? [];
  return (
    <Card variant="outlined">
      <CardHeader title={<Typography variant="h6">apt Install Audit</Typography>} sx={{ pb: 0 }} />
      <CardContent>
        {entries.length === 0 && (
          <Typography variant="body2" color="text.secondary">
            No apt installs run from this UI yet.
          </Typography>
        )}
        {entries.length > 0 && (
          <TableContainer>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Time</TableCell>
                  <TableCell>Package</TableCell>
                  <TableCell>Result</TableCell>
                  <TableCell>Source IP</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {entries.slice(0, 20).map((e, i) => (
                  <TableRow key={i}>
                    <TableCell sx={{ fontFamily: "monospace", fontSize: "0.75rem" }}>
                      {new Date(e.timestamp).toLocaleString()}
                    </TableCell>
                    <TableCell sx={{ fontFamily: "monospace" }}>{e.package}</TableCell>
                    <TableCell>
                      <Chip
                        size="small"
                        label={e.success ? "success" : "failed"}
                        color={e.success ? "success" : "error"}
                        variant={e.success ? "outlined" : "filled"}
                      />
                      {!e.success && e.error && (
                        <Typography variant="caption" color="error" sx={{ display: "block" }}>
                          {e.error}
                        </Typography>
                      )}
                    </TableCell>
                    <TableCell sx={{ fontFamily: "monospace", fontSize: "0.75rem" }}>
                      {e.source_ip || "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </CardContent>
    </Card>
  );
}

// ConfigCard preserves the pre-Phase-0 static config display. Keeps it
// visible without the admin having to hunt across multiple places, and
// is the natural home for things like LocalInterface + VPN admins.
function ConfigCard({
  publicIP,
  localInterface,
  dnsmasqEnabled,
  vpnAdmins,
}: {
  publicIP: string;
  localInterface: string;
  dnsmasqEnabled: boolean;
  vpnAdmins: string[];
}) {
  return (
    <Card variant="outlined">
      <CardHeader title={<Typography variant="h6">Config</Typography>} sx={{ pb: 0 }} />
      <CardContent>
        <Box
          sx={{
            display: "grid",
            gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" },
            gap: 3,
          }}
        >
          <Box>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}
            >
              Public IP
            </Typography>
            <Typography variant="body1" sx={{ fontFamily: "monospace" }}>
              {publicIP || "Auto-detected"}
            </Typography>
          </Box>
          <Box>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}
            >
              Local Interface
            </Typography>
            <Typography variant="body1" sx={{ fontFamily: "monospace" }}>
              {localInterface || "Auto-detected"}
            </Typography>
          </Box>
          <Box>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}
            >
              DNSMasq
            </Typography>
            <Chip
              label={dnsmasqEnabled ? "Enabled" : "Disabled"}
              size="small"
              color={dnsmasqEnabled ? "success" : "default"}
            />
          </Box>
          <Box>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}
            >
              VPN Admins
            </Typography>
            {vpnAdmins.length > 0 ? (
              <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                {vpnAdmins.map((a) => (
                  <Chip key={a} label={a} size="small" variant="outlined" />
                ))}
              </Box>
            ) : (
              <Typography variant="body2" color="text.secondary">
                None configured
              </Typography>
            )}
          </Box>
        </Box>
      </CardContent>
    </Card>
  );
}

// InstallPackageDialog is currently unused — install buttons on each
// component card call /install/package directly with a fixed pkg name.
// Kept as scaffolding for a future "install arbitrary package" admin tool.
export function InstallPackageDialog({
  open,
  packages,
  onClose,
}: {
  open: boolean;
  packages: string[];
  onClose: () => void;
}) {
  const install = useInstallPackage();
  const [pkg, setPkg] = useState(packages[0] || "");
  return (
    <Dialog open={open} onClose={onClose}>
      <DialogTitle>Install package</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ mb: 2 }}>
          apt-get update and install a whitelisted package. The invocation is logged.
        </DialogContentText>
        <FormControl fullWidth size="small">
          <InputLabel>Package</InputLabel>
          <Select label="Package" value={pkg} onChange={(e) => setPkg(e.target.value)}>
            {packages.map((p) => (
              <MenuItem key={p} value={p}>
                {p}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          onClick={() => install.mutate(pkg)}
          disabled={install.isPending}
        >
          {install.isPending ? <CircularProgress size={16} /> : "Install"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function SystemHealthTab({
  publicIP,
  localInterface,
  dnsmasqEnabled,
  vpnAdmins,
}: {
  publicIP: string;
  localInterface: string;
  dnsmasqEnabled: boolean;
  vpnAdmins: string[];
}) {
  const { data: health, isLoading, error } = useSystemHealth();

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", p: 4 }}>
        <CircularProgress />
      </Box>
    );
  }
  if (error) {
    return <Alert severity="error">Failed to load system health: {error.message}</Alert>;
  }
  if (!health) return null;

  return (
    <Stack spacing={2}>
      <Paper sx={{ p: 2, bgcolor: "background.default" }} variant="outlined">
        <Typography variant="body2" color="text.secondary">
          On-host health checks with inline fixers. Downstream service probes live on the{" "}
          <strong>Health Checks</strong> tab; this page is strictly about the machine horizon
          is running on.
        </Typography>
      </Paper>
      <SystemLevelCard health={health} />
      <WireGuardCard health={health} />
      <HAProxyCard health={health} />
      <DNSMasqCard health={health} />
      <LetsEncryptCard health={health} />
      <AptInstallCard />
      <ConfigCard
        publicIP={publicIP}
        localInterface={localInterface}
        dnsmasqEnabled={dnsmasqEnabled}
        vpnAdmins={vpnAdmins}
      />
    </Stack>
  );
}

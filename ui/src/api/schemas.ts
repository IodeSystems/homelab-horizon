/**
 * Zod schemas for API response validation.
 *
 * These schemas validate that API responses match the expected shape at runtime.
 * If the Go backend changes a field name or type, the validation will throw
 * immediately in dev instead of silently producing `undefined`.
 *
 * After tygo generates types, these schemas should be kept in sync with
 * generated-types.ts. A mismatch between the zod schema and the generated
 * type will cause a tsc error.
 */
import { z } from "zod/v4";

// Auth
export const AuthStatusSchema = z.object({
  authenticated: z.boolean(),
  method: z.enum(["cookie", "vpn"]).optional(),
});

export const LoginResponseSchema = z.object({
  ok: z.boolean(),
  error: z.string().optional(),
  invite: z.boolean().optional(),
  redirect: z.string().optional(),
});

// Dashboard
export const PeerSyncStatusSchema = z.object({
  primaryId: z.string(),
  pullCount: z.number(),
  lastPullAt: z.number().optional(),
  lastSuccessAt: z.number().optional(),
  lastApplyAt: z.number().optional(),
  lastError: z.string().optional(),
});

export const DashboardDataSchema = z.object({
  serviceCount: z.number(),
  domainCount: z.number(),
  zoneCount: z.number(),
  peerCount: z.number(),
  haproxyRunning: z.boolean(),
  sslEnabled: z.boolean(),
  version: z.string(),
  checksTotal: z.number(),
  checksHealthy: z.number(),
  checksFailed: z.number(),
  peerSync: PeerSyncStatusSchema.optional(),
});

// Services
const HealthCheckSchema = z
  .object({
    path: z.string(),
  })
  .optional();

const DeployConfigSchema = z
  .object({
    nextBackend: z.string(),
    activeSlot: z.string(),
    balance: z.string(),
  })
  .optional();

const ServiceStatusSchema = z.object({
  internalDNSUp: z.boolean(),
  internalDNSResolved: z.string().optional(),
  externalDNSUp: z.boolean(),
  externalDNSResolved: z.string().optional(),
  proxyUp: z.boolean(),
  proxyError: z.string().optional(),
  proxyState: z.string().optional(),
  proxyNextState: z.string().optional(),
});

export const ServiceSchema = z.object({
  name: z.string(),
  domains: z.array(z.string()),
  internalDNS: z.object({ ip: z.string() }).optional(),
  externalDNS: z
    .object({
      ip: z.string(),
      ips: z.array(z.string()).optional(),
      configuredIPs: z.array(z.string()).optional(),
      ttl: z.number(),
    })
    .optional(),
  proxy: z
    .object({
      backend: z.string(),
      healthCheck: HealthCheckSchema,
      internalOnly: z.boolean(),
      deploy: DeployConfigSchema,
    })
    .optional(),
  integrations: z
    .object({
      metrics: z
        .object({
          enabled: z.boolean(),
          path: z.string().optional(),
          bearer: z.string().optional(),
        })
        .optional(),
    })
    .optional(),
  status: ServiceStatusSchema,
});

export const ServicesSchema = z.array(ServiceSchema);

// Domains
export const DomainAnalysisSchema = z.object({
  domain: z.string(),
  zoneName: z.string(),
  zoneHasSSL: z.boolean(),
  hasZone: z.boolean(),
  serviceName: z.string(),
  hasService: z.boolean(),
  hasInternalDNS: z.boolean(),
  internalIP: z.string(),
  hasExternalDNS: z.boolean(),
  externalIP: z.string(),
  externalIPs: z.array(z.string()).optional(),
  dnsmasqResolvedIP: z.string(),
  remoteResolvedIP: z.string(),
  dnsmasqDNSMatch: z.boolean(),
  remoteDNSMatch: z.boolean(),
  hasProxy: z.boolean(),
  proxyBackend: z.string(),
  internalOnly: z.boolean(),
  hasHealthCheck: z.boolean(),
  healthPath: z.string(),
  hasSSLCoverage: z.boolean(),
  certExists: z.boolean(),
  certExpiry: z.string(),
  certDomain: z.string(),
  canEnableHTTPS: z.boolean(),
  neededSubZone: z.string(),
  neededSubZoneDisplay: z.string(),
  canRequestCert: z.boolean(),
  canSyncDNS: z.boolean(),
  coveredBy: z.string().optional(),
  isRedundant: z.boolean(),
  absorbedDomains: z.array(z.object({ domain: z.string(), service: z.string().optional() })).optional(),
});

export const SSLGapSchema = z.object({
  domain: z.string(),
  zoneName: z.string(),
  subZone: z.string(),
  display: z.string(),
  reason: z.string(),
});

export const ZoneSSLStatusSchema = z.object({
  zoneName: z.string(),
  sslEnabled: z.boolean(),
  configuredDomains: z.array(z.string()),
  actualSANs: z.array(z.string()),
  certExists: z.boolean(),
  certExpiry: z.string(),
  certIssuer: z.string(),
  missingSANs: z.array(z.string()),
  extraSANs: z.array(z.string()),
});

export const DomainsDataSchema = z.object({
  domains: z.array(DomainAnalysisSchema),
  totalCount: z.number(),
  intDNSCount: z.number(),
  extDNSCount: z.number(),
  httpsCount: z.number(),
  proxyCount: z.number(),
  sslGaps: z.array(SSLGapSchema),
  zoneSSLStatuses: z.array(ZoneSSLStatusSchema),
});

// VPN
export const VPNPeerSchema = z.object({
  name: z.string(),
  publicKey: z.string(),
  allowedIPs: z.string(),
  endpoint: z.string(),
  latestHandshake: z.string(),
  transferRx: z.string(),
  transferTx: z.string(),
  online: z.boolean(),
  isAdmin: z.boolean(),
  profile: z.string(),
  mfaEnrolled: z.boolean(),
  mfaSessionActive: z.boolean(),
  mfaSessionExpiry: z.string().optional(),
});

export const VPNPeersSchema = z.array(VPNPeerSchema);

export const AddPeerResponseSchema = z.object({
  ok: z.boolean(),
  config: z.string(),
  qrCode: z.string(),
});

export const PeerConfigResponseSchema = z.object({
  ok: z.boolean(),
  config: z.string(),
  qrCode: z.string(),
});

export const RekeyPeerResponseSchema = z.object({
  ok: z.boolean(),
  config: z.string(),
  qrCode: z.string(),
  shareToken: z.string(),
  shareURL: z.string(),
});

export const ConfigShareSchema = z.object({
  token: z.string(),
  url: z.string(),
  peerName: z.string(),
});

export const ConfigSharesSchema = z.array(ConfigShareSchema);

export const InviteSchema = z.object({
  token: z.string(),
  url: z.string(),
});

export const InvitesSchema = z.array(InviteSchema);

export const CreateInviteResponseSchema = z.object({
  ok: z.boolean(),
  token: z.string(),
  url: z.string(),
});

// Zones
export const ZoneSchema = z.object({
  name: z.string(),
  zoneId: z.string(),
  sslEnabled: z.boolean(),
  sslEmail: z.string(),
  subZones: z.array(z.string()),
  providerType: z.string(),
});

export const ZonesSchema = z.array(ZoneSchema);

// Zone DNS records (live at the provider, tagged with whether HZ manages them)
export const DNSRecordSchema = z.object({
  name: z.string(),
  type: z.string(),
  value: z.string(),
  ttl: z.number(),
  managed: z.boolean(),
});

export const ZoneRecordsResponseSchema = z.object({
  zone: z.string(),
  records: z.array(DNSRecordSchema),
});

// DNS drift — an out-of-band change at a provider that halts all DNS sync
// until an operator reviews and clears it.
export const DNSDriftInfoSchema = z.object({
  zone: z.string(),
  name: z.string(),
  type: z.string(),
  expected: z.array(z.string()),
  live: z.array(z.string()),
  detectedAt: z.number(),
});

export const DNSDriftStatusResponseSchema = z.object({
  blocked: z.boolean(),
  detail: DNSDriftInfoSchema.optional(),
});

// Settings
export const HAProxyStatusSchema = z.object({
  running: z.boolean(),
  configExists: z.boolean(),
  version: z.string(),
  enabled: z.boolean(),
  httpPort: z.number(),
  httpsPort: z.number(),
});

export const SSLStatusSchema = z.object({
  enabled: z.boolean(),
  certDir: z.string(),
  haproxyCertDir: z.string(),
});

export const CheckStatusSchema = z.object({
  name: z.string(),
  type: z.string(),
  target: z.string(),
  status: z.string(),
  last_check: z.string(),
  last_error: z.string().optional(),
  interval: z.number(),
  enabled: z.boolean(),
  auto_gen: z.boolean(),
});

export const SystemConfigSchema = z.object({
  publicIP: z.string(),
  publicIPOverride: z.string().optional(),
  publicIPLastChecked: z.number().optional(),
  publicIPStale: z.boolean(),
  publicIPMaxAge: z.number(),
  localInterface: z.string(),
  dnsmasqEnabled: z.boolean(),
  vpnAdmins: z.array(z.string()),
});

export const PublicIPStatusSchema = z.object({
  publicIP: z.string(),
  publicIPOverride: z.string().optional(),
  publicIPLastChecked: z.number().optional(),
  publicIPStale: z.boolean(),
  publicIPMaxAge: z.number(),
  error: z.string().optional(),
});

export const SettingsDataSchema = z.object({
  zones: z.array(ZoneSchema),
  haproxy: HAProxyStatusSchema,
  ssl: SSLStatusSchema,
  checks: z.array(CheckStatusSchema),
  config: SystemConfigSchema,
});

export const HAProxyConfigPreviewSchema = z.object({
  config: z.string(),
});

// Domain SSL mutations
export const DomainSSLAddResponseSchema = z.object({
  ok: z.boolean(),
  zone: z.string(),
  subZone: z.string(),
});

// Bans
export const BanEntrySchema = z.object({
  ip: z.string(),
  timeout: z.number(),
  createdAt: z.number(),
  expiresAt: z.number(),
  reason: z.string(),
  service: z.string(),
});

export const BanListResponseSchema = z.object({
  bans: z.array(BanEntrySchema),
});

// Check history
export const CheckResultSchema = z.object({
  timestamp: z.string(),
  status: z.string(),
  latency: z.number(),
  error: z.string().optional(),
});

export const CheckHistoryResponseSchema = z.object({
  name: z.string(),
  results: z.array(CheckResultSchema),
});

export const ChecksListSchema = z.array(CheckStatusSchema);

export const FieldChangeSchema = z.object({
  path: z.string(),
  before: z.string().optional(),
  after: z.string().optional(),
});

export const PendingItemSchema = z.object({
  kind: z.string(),
  name: z.string(),
  change: z.string(),
  fields: z.array(FieldChangeSchema).optional(),
});

export const PendingChangesSchema = z.object({
  hasPending: z.boolean(),
  count: z.number(),
  items: z.array(PendingItemSchema),
});

// Observability topology (declared hosts + Prometheus exporters)
export const HostDeclSchema = z.object({
  name: z.string(),
  ip: z.string(),
  labels: z.record(z.string(), z.string()).optional(),
});

export const ExporterSchema = z.object({
  job: z.string(),
  targets: z.array(z.string()).optional(),
  port: z.number().optional(),
  hosts: z.array(z.string()).optional(),
  path: z.string().optional(),
  bearer: z.string().optional(),
  labels: z.record(z.string(), z.string()).optional(),
});

export const ExporterTargetSchema = z.object({
  job: z.string(),
  address: z.string(),
  path: z.string(),
  labels: z.record(z.string(), z.string()).optional(),
  alive: z.boolean(),
});

export const TopologyDataSchema = z.object({
  hosts: z.array(HostDeclSchema),
  exporters: z.array(ExporterSchema),
  targets: z.array(ExporterTargetSchema),
  knownHosts: z.array(z.string()),
});

// Topology scan (discovery) — probes a port/path across known + extra hosts.
export const ScanResultSchema = z.object({
  address: z.string(),
  host: z.string(),
  alive: z.boolean(),
  configured: z.boolean(),
});

export const TopologyScanRespSchema = z.object({
  port: z.number(),
  path: z.string(),
  results: z.array(ScanResultSchema),
  knownHosts: z.array(z.string()),
});

// Service metrics-path scan
export const ServiceScanSlotSchema = z.object({
  slot: z.string().optional(),
  address: z.string(),
  path: z.string(),
  ok: z.boolean(),
});

export const ServiceScanMetricsRespSchema = z.object({
  name: z.string(),
  suggestedPath: z.string().optional(),
  candidates: z.array(z.string()),
  slots: z.array(ServiceScanSlotSchema),
});

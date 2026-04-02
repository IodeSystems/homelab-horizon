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
export const DashboardDataSchema = z.object({
  serviceCount: z.number(),
  domainCount: z.number(),
  zoneCount: z.number(),
  peerCount: z.number(),
  haproxyRunning: z.boolean(),
  sslEnabled: z.boolean(),
  version: z.string(),
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

export const ServiceSchema = z.object({
  name: z.string(),
  domains: z.array(z.string()),
  internalDNS: z.object({ ip: z.string() }).optional(),
  externalDNS: z
    .object({ ip: z.string(), ttl: z.number() })
    .optional(),
  proxy: z
    .object({
      backend: z.string(),
      healthCheck: HealthCheckSchema,
      internalOnly: z.boolean(),
      deploy: DeployConfigSchema,
    })
    .optional(),
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
});

export const VPNPeersSchema = z.array(VPNPeerSchema);

export const AddPeerResponseSchema = z.object({
  ok: z.boolean(),
  config: z.string(),
  qrCode: z.string(),
});

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
  localInterface: z.string(),
  dnsmasqEnabled: z.boolean(),
  vpnAdmins: z.array(z.string()),
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

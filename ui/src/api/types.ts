export interface DashboardData {
  serviceCount: number;
  domainCount: number;
  zoneCount: number;
  peerCount: number;
  haproxyRunning: boolean;
  sslEnabled: boolean;
  version: string;
}

export interface Service {
  name: string;
  domains: string[];
  internalDNS?: { ip: string } | null;
  externalDNS?: { ip: string; ttl: number } | null;
  proxy?: {
    backend: string;
    healthCheck?: { path: string } | null;
    internalOnly: boolean;
    deploy?: {
      nextBackend: string;
      activeSlot: string;
      balance: string;
    } | null;
  } | null;
}

export interface DomainAnalysis {
  domain: string;
  zoneName: string;
  zoneHasSSL: boolean;
  hasZone: boolean;
  serviceName: string;
  hasService: boolean;
  hasInternalDNS: boolean;
  internalIP: string;
  hasExternalDNS: boolean;
  externalIP: string;
  dnsmasqResolvedIP: string;
  remoteResolvedIP: string;
  dnsmasqDNSMatch: boolean;
  remoteDNSMatch: boolean;
  hasProxy: boolean;
  proxyBackend: string;
  internalOnly: boolean;
  hasHealthCheck: boolean;
  healthPath: string;
  hasSSLCoverage: boolean;
  certExists: boolean;
  certExpiry: string;
  certDomain: string;
  canEnableHTTPS: boolean;
  neededSubZone: string;
  neededSubZoneDisplay: string;
  canRequestCert: boolean;
  canSyncDNS: boolean;
}

export interface SSLGap {
  domain: string;
  zoneName: string;
  subZone: string;
  display: string;
  reason: string;
}

export interface ZoneSSLStatus {
  zoneName: string;
  sslEnabled: boolean;
  configuredDomains: string[];
  actualSANs: string[];
  certExists: boolean;
  certExpiry: string;
  certIssuer: string;
  missingSANs: string[];
  extraSANs: string[];
}

export interface DomainsData {
  domains: DomainAnalysis[];
  totalCount: number;
  intDNSCount: number;
  extDNSCount: number;
  httpsCount: number;
  proxyCount: number;
  sslGaps: SSLGap[];
  zoneSSLStatuses: ZoneSSLStatus[];
}

export interface VPNPeer {
  name: string;
  publicKey: string;
  allowedIPs: string;
  endpoint: string;
  latestHandshake: string;
  transferRx: string;
  transferTx: string;
  online: boolean;
  isAdmin: boolean;
}

export interface Zone {
  name: string;
  zoneId: string;
  sslEnabled: boolean;
  sslEmail: string;
  subZones: string[];
  providerType: string;
}

export interface AddPeerResponse {
  ok: boolean;
  config: string;
  qrCode: string;
}

export interface Invite {
  token: string;
  url: string;
}

export interface CreateInviteResponse {
  ok: boolean;
  token: string;
  url: string;
}

export interface HAProxyStatus {
  running: boolean;
  configExists: boolean;
  version: string;
  enabled: boolean;
  httpPort: number;
  httpsPort: number;
}

export interface SSLStatus {
  enabled: boolean;
  certDir: string;
  haproxyCertDir: string;
}

export interface CheckStatus {
  name: string;
  type: string;
  target: string;
  status: string;
  last_check: string;
  last_error?: string;
  interval: number;
  enabled: boolean;
  auto_gen: boolean;
}

export interface SystemConfig {
  publicIP: string;
  localInterface: string;
  dnsmasqEnabled: boolean;
  vpnAdmins: string[];
}

export interface SettingsData {
  zones: Zone[];
  haproxy: HAProxyStatus;
  ssl: SSLStatus;
  checks: CheckStatus[];
  config: SystemConfig;
}

export interface HAProxyConfigPreview {
  config: string;
}

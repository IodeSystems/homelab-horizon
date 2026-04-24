/**
 * Re-exports from generated types.
 *
 * The source of truth for API types is generated-types.ts (produced by tygo
 * from Go structs in internal/apitypes/types.go). This file provides
 * convenient aliases so the rest of the codebase can import familiar names.
 *
 * If a type here doesn't match generated-types.ts, tsc will error.
 */
export type {
  DashboardResponse as DashboardData,
  ServiceResp as Service,
  InternalDNSResp,
  ExternalDNSResp,
  ProxyResp,
  DeployResp,
  HealthCheckResp,
  DomainResp as DomainAnalysis,
  SSLGapResp as SSLGap,
  ZoneSSLResp as ZoneSSLStatus,
  DomainsResponse as DomainsData,
  PeerResp as VPNPeer,
  ZoneResp as Zone,
  AddPeerResponse,
  PeerConfigResponse,
  RekeyPeerResponse,
  ConfigShareResp as ConfigShare,
  InviteResp as Invite,
  CreateInviteResponse,
  HAProxyResp as HAProxyStatus,
  SSLResp as SSLStatus,
  CheckStatusResp as CheckStatus,
  ConfigResp as SystemConfig,
  SettingsResponse as SettingsData,
  HAProxyConfigPreview,
  AuthStatusResponse,
  LoginRequest,
  LoginResponse,
  ServiceRequest,
  DNSSyncResponse,
  DNSSyncAllResponse,
  ToggleAdminResponse,
  TriggerSyncResponse,
  RunCheckResponse,
  BanEntry,
  BanListResponse,
  BanRequest,
  UnbanRequest,
  ServiceIntegration,
  CheckResult,
  CheckHistoryResponse,
  ChecksOverview,
  DomainSSLAddRequest,
  DomainSSLAddResponse,
  DomainSSLRemoveRequest,
  MFAStatusResponse,
  MFAEnrollResponse,
  MFAVerifyRequest,
  MFAVerifyResponse,
  MFASettingsResponse,
  HAStatusResponse,
  HAFleetPeer,
  HACreateJoinTokenRequest,
  HACreateJoinTokenResponse,
  ComponentHealth,
  SystemHealthResponse as SystemHealth,
} from "./generated-types";

// Apt-audit entries aren't in apitypes (internal-only struct in the server
// package). Mirror the JSON shape here.
export interface AptAuditEntry {
  timestamp: string;
  package: string;
  success: boolean;
  error?: string;
  output?: string;
  source_ip?: string;
}

export interface AptAuditResponse {
  entries: AptAuditEntry[];
}

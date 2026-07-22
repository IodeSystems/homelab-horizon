import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch, apiFetchText } from "./client";
import type {
  AddPeerResponse,
  AllCheckHistoryResponse,
  AptAuditResponse,
  BanListResponse,
  CheckHistoryResponse,
  CheckStatus,
  ConfigShare,
  CreateInviteResponse,
  DashboardData,
  DNSDriftStatusResponse,
  DomainsData,
  ZoneRecordsResponse,
  HACreateJoinTokenResponse,
  HAProxyConfigPreview,
  HAStatusResponse,
  Invite,
  IPTablesReport,
  IPTablesRule,
  IPTablesRulesResponse,
  MFAEnrollResponse,
  MFASettingsResponse,
  MFAStatusResponse,
  MFAVerifyResponse,
  PendingChanges,
  PeerConfigResponse,
  RekeyPeerResponse,
  Service,
  ServiceIntegration,
  SettingsData,
  SystemHealth,
  SystemMetrics,
  VPNPeer,
  Zone,
  HostDecl,
  Exporter,
  TopologyData,
  ServiceScanMetricsResp,
  HostPortMapResponse,
  PortRange,
  ScrapeTokenResp,
} from "./types";
import {
  BanListResponseSchema,
  CheckHistoryResponseSchema,
  ChecksListSchema,
  ConfigSharesSchema,
  DashboardDataSchema,
  DNSDriftStatusResponseSchema,
  ServicesSchema,
  DomainsDataSchema,
  PeerConfigResponseSchema,
  RekeyPeerResponseSchema,
  VPNPeersSchema,
  ZonesSchema,
  ZoneRecordsResponseSchema,
  SettingsDataSchema,
  HAProxyConfigPreviewSchema,
  InvitesSchema,
  PendingChangesSchema,
  TopologyDataSchema,
  ServiceScanMetricsRespSchema,
  HostPortMapResponseSchema,
  ScrapeTokenRespSchema,
} from "./schemas";

export function useDashboard() {
  return useQuery({
    queryKey: ["dashboard"],
    queryFn: () =>
      apiFetch<DashboardData>("/dashboard", { schema: DashboardDataSchema }),
  });
}

export function useServices() {
  return useQuery({
    queryKey: ["services"],
    queryFn: () =>
      apiFetch<Service[]>("/services", { schema: ServicesSchema }),
  });
}

export function useDomains() {
  return useQuery({
    queryKey: ["domains"],
    queryFn: () =>
      apiFetch<DomainsData>("/domains", { schema: DomainsDataSchema }),
  });
}

export function useVPNPeers() {
  return useQuery({
    queryKey: ["vpn", "peers"],
    queryFn: () =>
      apiFetch<VPNPeer[]>("/vpn/peers", { schema: VPNPeersSchema }),
  });
}

export function useZones() {
  return useQuery({
    queryKey: ["zones"],
    queryFn: () => apiFetch<Zone[]>("/zones", { schema: ZonesSchema }),
  });
}

export function useServiceIntegration(name: string) {
  return useQuery({
    queryKey: ["services", "integration", name],
    queryFn: () =>
      apiFetch<ServiceIntegration>(`/services/integration?name=${encodeURIComponent(name)}`),
    enabled: !!name,
  });
}

// --- Mutation types ---

export interface ServiceMutationInput {
  originalName?: string;
  name: string;
  domains: string[];
  internalDNS?: { ip: string } | null;
  externalDNS?: { ip: string; ips?: string[]; ttl: number } | null;
  proxy?: {
    backend?: string;
    staticRoot?: string;
    static?: boolean;
    self?: boolean;
    spa?: boolean;
    healthCheck?: { path: string } | null;
    internalOnly: boolean;
    deploy?: { nextBackend: string; balance?: string } | null;
    timeouts?: {
      connectSeconds?: number;
      serverSeconds?: number;
      tunnelSeconds?: number;
    } | null;
  } | null;
  integrations?: {
    metrics?: {
      enabled: boolean;
      path?: string;
      bearer?: string;
    };
  } | null;
}

// --- Service mutations ---

export function useAddService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: ServiceMutationInput) =>
      apiFetch("/services/add", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

export function useEditService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: ServiceMutationInput) =>
      apiFetch("/services/edit", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

export function useDeleteService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/services/delete", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

// --- DNS drift ---
//
// A drift-detected zone halts ALL DNS sync (server-side) until an operator
// reviews the diff and clears it. Normal refetch is enough here — no
// aggressive polling, the banner just needs to reflect the current block.

export function useDNSDriftStatus() {
  return useQuery({
    queryKey: ["dns", "drift"],
    queryFn: () =>
      apiFetch<DNSDriftStatusResponse>("/dns/drift", {
        schema: DNSDriftStatusResponseSchema,
      }),
  });
}

export function useClearDNSDrift() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<{ ok: boolean }>("/dns/drift/clear", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dns", "drift"] });
      qc.invalidateQueries({ queryKey: ["zones", "records"] });
    },
  });
}

// --- Zone mutations ---

export function useAddSubZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { zone: string; subzone: string }) =>
      apiFetch("/zones/subzone", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
      qc.invalidateQueries({ queryKey: ["domains"] });
    },
  });
}

// --- Domain SSL mutations ---

export function useAddDomainSSL() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) =>
      apiFetch("/domains/ssl/add", {
        method: "POST",
        body: JSON.stringify({ domain }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

export function useRemoveDomainSSL() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) =>
      apiFetch("/domains/ssl/remove", {
        method: "POST",
        body: JSON.stringify({ domain }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

// --- SSL mutations ---

export function useRequestCert() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (zone: string) =>
      apiFetch("/ssl/request-cert", {
        method: "POST",
        body: JSON.stringify({ zone }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

// --- Pending changes ---

// Config edits apply locally at once but external DNS/SSL only publish on a
// full Sync. This surfaces what's diverged from the last sync. Polled so a
// second admin's unsynced edits show up here too.
export function usePendingChanges() {
  return useQuery({
    queryKey: ["pending"],
    queryFn: () =>
      apiFetch<PendingChanges>("/sync/pending", {
        schema: PendingChangesSchema,
      }),
    refetchInterval: 10000,
  });
}

// --- Sync mutations ---

export function useTriggerSync() {
  return useMutation({
    mutationFn: () =>
      apiFetch<{ ok: boolean; started: boolean }>("/services/sync", {
        method: "POST",
      }),
  });
}

// --- VPN mutations ---

export function useAddPeer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; extraIPs: string; profile: string }) =>
      apiFetch<AddPeerResponse>("/vpn/peers/add", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useEditPeer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      publicKey: string;
      name: string;
      extraIPs: string;
      profile: string;
    }) =>
      apiFetch("/vpn/peers/edit", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useDeletePeer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (publicKey: string) =>
      apiFetch("/vpn/peers/delete", {
        method: "POST",
        body: JSON.stringify({ publicKey }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useToggleAdmin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch<{ ok: boolean; isAdmin: boolean }>("/vpn/peers/toggle-admin", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useSetPeerProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; profile: string }) =>
      apiFetch("/vpn/peers/set-profile", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useGetPeerConfig(publicKey: string) {
  return useQuery({
    queryKey: ["vpn", "peers", "config", publicKey],
    queryFn: () =>
      apiFetch<PeerConfigResponse>(
        `/vpn/peers/config?publicKey=${encodeURIComponent(publicKey)}`,
        { schema: PeerConfigResponseSchema },
      ),
    enabled: !!publicKey,
  });
}

export function useRekeyPeer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (publicKey: string) =>
      apiFetch<RekeyPeerResponse>("/vpn/peers/rekey", {
        method: "POST",
        body: JSON.stringify({ publicKey }),
        schema: RekeyPeerResponseSchema,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
      qc.invalidateQueries({ queryKey: ["vpn", "config-shares"] });
    },
  });
}

export function useConfigShares() {
  return useQuery({
    queryKey: ["vpn", "config-shares"],
    queryFn: () =>
      apiFetch<ConfigShare[]>("/vpn/config-shares", {
        schema: ConfigSharesSchema,
      }),
  });
}

export function useDeleteConfigShare() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (token: string) =>
      apiFetch("/vpn/config-shares/delete", {
        method: "POST",
        body: JSON.stringify({ token }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "config-shares"] });
    },
  });
}

export function useReloadWG() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch("/vpn/reload", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useInvites() {
  return useQuery({
    queryKey: ["vpn", "invites"],
    queryFn: () =>
      apiFetch<Invite[]>("/vpn/invites", { schema: InvitesSchema }),
  });
}

export function useCreateInvite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<CreateInviteResponse>("/vpn/invites/create", {
        method: "POST",
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "invites"] });
    },
  });
}

export function useDeleteInvite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (token: string) =>
      apiFetch("/vpn/invites/delete", {
        method: "POST",
        body: JSON.stringify({ token }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "invites"] });
    },
  });
}

// --- Settings ---

export function useSettings() {
  return useQuery({
    queryKey: ["settings"],
    queryFn: () =>
      apiFetch<SettingsData>("/settings", { schema: SettingsDataSchema }),
  });
}

// --- Public IP override / refresh ---

export interface PublicIPStatus {
  publicIP: string;
  publicIPOverride?: string;
  publicIPLastChecked?: number;
  publicIPStale: boolean;
  publicIPMaxAge: number;
  error?: string;
}

export function useSetPublicIPOverride() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (override: string) =>
      apiFetch<PublicIPStatus>("/public-ip/override", {
        method: "POST",
        body: JSON.stringify({ override }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useRefreshPublicIP() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<PublicIPStatus>("/public-ip/refresh", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useHAProxyConfigPreview() {
  return useQuery({
    queryKey: ["haproxy", "config-preview"],
    queryFn: () =>
      apiFetch<HAProxyConfigPreview>("/haproxy/config-preview", {
        schema: HAProxyConfigPreviewSchema,
      }),
    enabled: false, // fetch on demand
  });
}

export function useAddZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      name: string;
      zoneId: string;
      providerType: string;
      sslEmail?: string;
      awsProfile?: string;
      awsAccessKeyId?: string;
      awsSecretAccessKey?: string;
      awsRegion?: string;
      namecomUsername?: string;
      namecomApiToken?: string;
      cloudflareApiToken?: string;
    }) =>
      apiFetch("/zones/add", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useEditZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      originalName: string;
      sslEmail: string;
      subZones: string;
    }) =>
      apiFetch("/zones/edit", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

export function useDeleteZone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/zones/delete", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

// --- Zone DNS records ---
//
// Set-based, drift-guarded on the server: a mutation carries `expectedFrom`,
// the values the UI last saw live for that (name, type), and the server
// refuses with 409 if the live set no longer matches. Invalidate onSettled
// (not just onSuccess) so a 409 response also refreshes the list — the next
// attempt needs the fresh live values to build a correct expectedFrom.

export function useZoneRecords(zoneName: string) {
  return useQuery({
    queryKey: ["zones", "records", zoneName],
    queryFn: () =>
      apiFetch<ZoneRecordsResponse>(
        `/zones/records?zone=${encodeURIComponent(zoneName)}`,
        { schema: ZoneRecordsResponseSchema },
      ),
    enabled: !!zoneName,
  });
}

export interface RecordMutationInput {
  zone: string;
  name: string;
  type: string;
  value: string;
  ttl: number;
  expectedFrom: string[];
}

export interface RecordEditInput extends RecordMutationInput {
  oldValue: string;
}

export interface RecordDeleteInput {
  zone: string;
  name: string;
  type: string;
  value: string;
  expectedFrom: string[];
}

export function useAddRecord() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: RecordMutationInput) =>
      apiFetch<{ ok: boolean; values: string[] }>("/zones/records/add", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSettled: (_data, _err, variables) => {
      qc.invalidateQueries({ queryKey: ["zones", "records", variables.zone] });
    },
  });
}

export function useEditRecord() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: RecordEditInput) =>
      apiFetch<{ ok: boolean; values: string[] }>("/zones/records/edit", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSettled: (_data, _err, variables) => {
      qc.invalidateQueries({ queryKey: ["zones", "records", variables.zone] });
    },
  });
}

export function useDeleteRecord() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: RecordDeleteInput) =>
      apiFetch<{ ok: boolean; values: string[] }>("/zones/records/delete", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSettled: (_data, _err, variables) => {
      qc.invalidateQueries({ queryKey: ["zones", "records", variables.zone] });
    },
  });
}

export function useHAProxyWriteConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch("/haproxy/write-config", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
    },
  });
}

export function useHAProxyReload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch("/haproxy/reload", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useAddCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      name: string;
      type: string;
      target: string;
      interval: number;
    }) =>
      apiFetch("/checks/add", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["checks"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useDeleteCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/checks/delete", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["checks"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useToggleCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/checks/toggle", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["checks"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useRunCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch<{ ok: boolean; status: string }>("/checks/run", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      qc.invalidateQueries({ queryKey: ["checks"] });
    },
  });
}

// --- Checks (standalone) ---

export function useChecks() {
  return useQuery({
    queryKey: ["checks"],
    queryFn: () =>
      apiFetch<CheckStatus[]>("/checks", { schema: ChecksListSchema }),
    refetchInterval: 30000,
  });
}

export function useCheckHistory(name: string) {
  return useQuery({
    queryKey: ["checks", "history", name],
    queryFn: () =>
      apiFetch<CheckHistoryResponse>(
        `/checks/history?name=${encodeURIComponent(name)}`,
        { schema: CheckHistoryResponseSchema },
      ),
    enabled: !!name,
  });
}

export function useAllCheckHistory() {
  return useQuery({
    queryKey: ["checks", "history", "all"],
    queryFn: () =>
      apiFetch<AllCheckHistoryResponse>("/checks/history/all"),
    refetchInterval: 30000,
  });
}

// --- Bans ---

export function useBans() {
  return useQuery({
    queryKey: ["bans"],
    queryFn: () =>
      apiFetch<BanListResponse>("/bans", { schema: BanListResponseSchema }),
    refetchInterval: 30000,
  });
}

export function useBanIP() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { ip: string; timeout?: number; reason?: string }) =>
      apiFetch("/bans/add", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["bans"] });
    },
  });
}

export function useUnbanIP() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (ip: string) =>
      apiFetch("/bans/remove", {
        method: "POST",
        body: JSON.stringify({ ip }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["bans"] });
    },
  });
}

// --- MFA ---

export function useMFAStatus() {
  return useQuery({
    queryKey: ["mfa", "status"],
    queryFn: () => apiFetch<MFAStatusResponse>("/mfa/status"),
    retry: false,
  });
}

export function useMFAEnroll() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<MFAEnrollResponse>("/mfa/enroll", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mfa", "status"] });
    },
  });
}

export function useMFAVerify() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { code: string; duration: string }) =>
      apiFetch<MFAVerifyResponse>("/mfa/verify", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mfa", "status"] });
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useMFASettings() {
  return useQuery({
    queryKey: ["mfa", "settings"],
    queryFn: () => apiFetch<MFASettingsResponse>("/mfa/settings"),
  });
}

export function useUpdateMFASettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { enabled: boolean; durations: string[] }) =>
      apiFetch("/mfa/settings", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mfa", "settings"] });
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useMFAReset() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/mfa/reset", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useMFAGrantSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; duration: string }) =>
      apiFetch("/mfa/grant-session", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useMFARevokeSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/mfa/revoke-session", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

// --- HA Fleet ---

export function useHAStatus() {
  return useQuery({
    queryKey: ["ha", "status"],
    queryFn: () => apiFetch<HAStatusResponse>("/ha/status"),
    refetchInterval: 30000,
  });
}

export function useCreateJoinToken() {
  return useMutation({
    mutationFn: (input: {
      peerId: string;
      topology: string;
      remoteEndpoint?: string;
      vpnRange?: string;
    }) =>
      apiFetch<HACreateJoinTokenResponse>("/ha/create-join-token", {
        method: "POST",
        body: JSON.stringify(input),
      }),
  });
}

// --- System Health dashboard (Phase 0) ---

// Poll every 15s so chip states stay fresh while the admin is actively
// clicking fixers. Not so fast that the endpoint's systemctl shells out
// become a load concern.
export function useSystemHealth() {
  return useQuery({
    queryKey: ["system", "health"],
    queryFn: () => apiFetch<SystemHealth>("/system/health"),
    refetchInterval: 15000,
  });
}

export function useAptAudit() {
  return useQuery({
    queryKey: ["system", "apt-audit"],
    queryFn: () => apiFetch<AptAuditResponse>("/system/apt-audit"),
  });
}

// Live host metrics — polled fast (2s) so the live charts feel real-time.
// Backend is stateless; UI keeps the rolling window in component state.
export function useSystemMetrics() {
  return useQuery({
    queryKey: ["system", "metrics"],
    queryFn: () => apiFetch<SystemMetrics>("/system/metrics"),
    refetchInterval: 2000,
    refetchIntervalInBackground: false,
  });
}

// Generic fixer hook — every /api/v1/system/fix/* and similar POST-with-no-body
// endpoint shares the same mutation shape. Rather than hand-write one hook per
// endpoint we take the path as input. Invalidates system/health so chips
// update after a successful fix.
function useSystemFix(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => apiFetch("/" + path, { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["system", "health"] });
    },
  });
}

export const useFixIPForwarding = () => useSystemFix("system/fix/ip-forwarding");
export const useFixMasquerade = () => useSystemFix("system/fix/masquerade");
export const useFixWGForwardChain = () => useSystemFix("system/fix/wg-forward-chain");
export const useFixWGRules = () => useSystemFix("system/fix/wg-rules");
export const useCreateWGConfig = () => useSystemFix("wg/create-config");
export const useInstallHorizonUnit = () => useSystemFix("system/install/horizon-unit");
export const useEnableHorizon = () => useSystemFix("system/enable/horizon");
export const useFixHAProxyLogging = () => useSystemFix("haproxy/fix-logging");
export const useWriteDNSMasqConfig = () => useSystemFix("dnsmasq/write-config");
export const useReloadDNSMasq = () => useSystemFix("dnsmasq/reload");
export const useStartDNSMasq = () => useSystemFix("dnsmasq/start");
export const useFixDNSMasqInterfaces = () => useSystemFix("dnsmasq/fix-interfaces");

// Package install — distinct from the generic fixer because it takes a body.
export function useInstallPackage() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (pkg: string) =>
      apiFetch<{ ok: boolean; package: string; output: string }>(
        "/system/install/package",
        {
          method: "POST",
          body: JSON.stringify({ package: pkg }),
        },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["system", "health"] });
      qc.invalidateQueries({ queryKey: ["system", "apt-audit"] });
    },
  });
}

// --- IPTables rule inventory (Phase 5) ---

export function useIPTablesRules() {
  return useQuery({
    queryKey: ["iptables", "rules"],
    queryFn: () => apiFetch<IPTablesRulesResponse>("/iptables/rules"),
    refetchInterval: 15000,
  });
}

export function useBlessIPTablesRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (canonical: string) =>
      apiFetch("/iptables/bless", {
        method: "POST",
        body: JSON.stringify({ canonical }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["iptables", "rules"] });
    },
  });
}

export function useUnblessIPTablesRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (canonical: string) =>
      apiFetch("/iptables/unbless", {
        method: "POST",
        body: JSON.stringify({ canonical }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["iptables", "rules"] });
    },
  });
}

export function useRemoveIPTablesRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (rule: IPTablesRule) =>
      apiFetch("/iptables/remove", {
        method: "POST",
        body: JSON.stringify(rule),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["iptables", "rules"] });
    },
  });
}

// --- Observability topology ---
//
// Read-modify-write, mirroring services: load the whole TopologyResp, mutate
// the hosts/exporters array client-side, PUT the whole array back.

export function useTopology() {
  return useQuery({
    queryKey: ["topology"],
    queryFn: () =>
      apiFetch<TopologyData>("/topology", { schema: TopologyDataSchema }),
  });
}

export function useSaveTopologyHosts() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (hosts: HostDecl[]) =>
      apiFetch("/topology/hosts", {
        method: "PUT",
        body: JSON.stringify({ hosts }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["topology"] });
    },
  });
}

export function useSaveTopologyExporters() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (exporters: Exporter[]) =>
      apiFetch("/topology/exporters", {
        method: "PUT",
        body: JSON.stringify({ exporters }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["topology"] });
    },
  });
}

// scrape.yaml / setup.sh are served outside /api/v1 as raw text, so they use
// apiFetchText (raw fetch) rather than the JSON apiFetch wrapper.
export function useScrapeYaml() {
  return useQuery({
    queryKey: ["topology", "scrape-yaml"],
    queryFn: () => apiFetchText("/integration/prometheus/scrape.yaml"),
  });
}

export function useSetupScript() {
  return useQuery({
    queryKey: ["topology", "setup-script"],
    queryFn: () => apiFetchText("/integration/prometheus/setup.sh"),
  });
}

// Read-only scrape token gating scrape.yaml/targets.json for unauthenticated
// pullers (e.g. a Prometheus box running setup.sh's refresh timer).
export function useScrapeToken() {
  return useQuery({
    queryKey: ["scrape-token"],
    queryFn: () =>
      apiFetch<ScrapeTokenResp>("/integration/scrape-token", {
        schema: ScrapeTokenRespSchema,
      }),
  });
}

export function useRotateScrapeToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<ScrapeTokenResp>("/integration/scrape-token", {
        method: "POST",
        schema: ScrapeTokenRespSchema,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scrape-token"] });
      qc.invalidateQueries({ queryKey: ["topology", "setup-script"] });
    },
  });
}

// Service metrics-path scan — probes a service's backend slot(s) for a
// working Prometheus metrics path.
export function useScanServiceMetrics() {
  return useMutation({
    mutationFn: (input: { name: string }) =>
      apiFetch<ServiceScanMetricsResp>("/services/scan-metrics", {
        method: "POST",
        body: JSON.stringify(input),
        schema: ServiceScanMetricsRespSchema,
      }),
  });
}

// --- Ports (reservations + allocation exclusions) ---
//
// Read-modify-write for the custom exclusion list, mirroring topology: load
// the whole PortExclusionsResp, mutate the custom array client-side, PUT it
// back whole. Builtin is server-constant and never sent.

export function usePorts() {
  return useQuery({
    queryKey: ["ports"],
    queryFn: () =>
      apiFetch<HostPortMapResponse>("/ports", { schema: HostPortMapResponseSchema }),
  });
}

export function useSaveCustomExclusions() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (custom: PortRange[]) =>
      apiFetch("/ports/exclusions", {
        method: "PUT",
        body: JSON.stringify({ custom }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ports"] });
    },
  });
}

export function useReconcileIPTables() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<IPTablesReport>("/iptables/reconcile", { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["iptables", "rules"] });
      qc.invalidateQueries({ queryKey: ["ha", "status"] });
    },
  });
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type {
  AddPeerResponse,
  AptAuditResponse,
  BanListResponse,
  CheckHistoryResponse,
  CheckStatus,
  ConfigShare,
  CreateInviteResponse,
  DashboardData,
  DomainsData,
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
  PeerConfigResponse,
  RekeyPeerResponse,
  Service,
  ServiceIntegration,
  SettingsData,
  SystemHealth,
  VPNPeer,
  Zone,
} from "./types";
import {
  BanListResponseSchema,
  CheckHistoryResponseSchema,
  ChecksListSchema,
  ConfigSharesSchema,
  DashboardDataSchema,
  ServicesSchema,
  DomainsDataSchema,
  PeerConfigResponseSchema,
  RekeyPeerResponseSchema,
  VPNPeersSchema,
  ZonesSchema,
  SettingsDataSchema,
  HAProxyConfigPreviewSchema,
  InvitesSchema,
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
    backend: string;
    healthCheck?: { path: string } | null;
    internalOnly: boolean;
    deploy?: { nextBackend: string; balance?: string } | null;
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
    },
  });
}

// --- DNS mutations ---

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
    },
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
      qc.invalidateQueries({ queryKey: ["dashboard"] });
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

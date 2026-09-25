import type {
  OIDCSettingsResp,
  OIDCSettingsReq,
  OIDCDiscoverResp,
  CMRegistrationResp,
  CMApproveReq,
  CMDenyReq,
  CMConfigResp,
  CMResolveResp,
  CMPromotionGateResp,
  CMCurrentKeyResp,
  AgentObservedResponse,
  ProjectResp,
  EnvironmentResp,
  MachineResp,
  MachineProjectionResp,
  VersionDriftResponse,
  HostsViewResp,
  HostShowResp,
} from "./generated-types";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { startAuthentication, startRegistration } from "@simplewebauthn/browser";
import { apiFetch, apiFetchText, ApiError } from "./client";
import type {
  AddPeerResponse,
  BucketedHistoryResponse,
  AptAuditResponse,
  BanListResponse,
  CheckHistoryResponse,
  CheckStatus,
  ProbeDiagnosisData,
  RemoteProbe,
  RemoteProbeRequest,
  RemoteProbeTest,
  RemoteProbeToken,
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
  IPTablesRulesResponse,
  MFAEnrollResponse,
  MFASettingsResponse,
  MFAStatusResponse,
  MFAVerifyResponse,
  PasskeyBeginResponse,
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
  ServiceDeleteOrphan,
  ServiceDeletePreviewResponse,
} from "./types";
import {
  BanListResponseSchema,
  CheckHistoryResponseSchema,
  ChecksListSchema,
  ProbeDiagnosisDataSchema,
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
  ServiceDeletePreviewResponseSchema,
  RemoteProbeListSchema,
  RemoteProbeTestSchema,
  RemoteProbeTokenSchema,
  AgentObservedResponseSchema,
} from "./schemas";

// The fleet as the machines last described it — the drift screen's only read.
//
// Polled rather than left to go stale on screen: every value it renders is an
// observation with an age beside it, and an age that stops advancing while the
// tab is open is the exact lie the screen exists to prevent. Sixty seconds is
// the agent's default cadence (handlers_agent_observed.go).
export function useAgentObserved() {
  return useQuery({
    queryKey: ["agent", "observed"],
    queryFn: () =>
      apiFetch<AgentObservedResponse>("/agent/observed", {
        schema: AgentObservedResponseSchema,
      }),
    refetchInterval: 60_000,
  });
}

// The model: projects, environments, machines, instances, and one machine's
// projection. Five reads behind three read-only screens.
//
// NOT POLLED, unlike the drift screen. Every value these carry is DECLARED —
// hz asserts it and it changes when somebody edits the config, not on a
// cadence — except the instance rows, which carry their own age and are read
// through the same `presentObservation` the drift screen uses. A declared
// value does not go stale on screen, so refetching it on a timer would buy
// nothing and spend a request per minute per open tab.

/** The project tree, each node's feed already resolved with its provenance. */
export function useProjects() {
  return useQuery({
    queryKey: ["projects"],
    queryFn: () => apiFetch<ProjectResp[]>("/projects"),
  });
}

/** Every declared rung, across every project. Name and posture are separate. */
export function useEnvironments() {
  return useQuery({
    queryKey: ["environments"],
    queryFn: () => apiFetch<EnvironmentResp[]>("/environments"),
  });
}

/** The declared machines: identity and segment membership. No project. */
export function useMachines() {
  return useQuery({
    queryKey: ["machines"],
    queryFn: () => apiFetch<MachineResp[]>("/machines"),
  });
}

/**
 * Every approved instance, with the version its rung declares beside the one
 * it last reported and the age of that reading.
 *
 * THE FAILURE OF THIS QUERY IS INFORMATION, not an empty list. hz answers 503
 * when it has no identity store at all, and the screens read `isError` as "hz
 * cannot say what is placed" rather than as "nothing is placed" — the two are
 * different facts and conflating them is the bug these screens exist to avoid.
 * So `retry: false`: a screen waiting through three silent retries shows the
 * wrong one of those two for as long as it waits.
 */
export function useVersionDrift() {
  return useQuery({
    queryKey: ["cm", "version-drift"],
    queryFn: () => apiFetch<VersionDriftResponse>("/cm/version-drift"),
    retry: false,
  });
}

/**
 * What hz says one machine should look like — the pure projection.
 *
 * A machine hz does not declare is a normal 200 carrying a gap that says so,
 * so this hook has no "not found" branch and must not grow one.
 */
export function useMachineProjection(machine: string) {
  return useQuery({
    queryKey: ["machines", "projection", machine],
    queryFn: () =>
      apiFetch<MachineProjectionResp>(
        `/machines/projection?machine=${encodeURIComponent(machine)}`,
      ),
    enabled: machine !== "",
  });
}

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
  // Round-tripped on every edit: the server assigns this from the request,
  // so omitting it would quietly un-park a reserved slot.
  dormant?: boolean;
  dormantReason?: string;
  // Full-replace on edit, like dormant: omitting them removes the service's
  // port forwards.
  forwards?: {
    proto: string;
    port: number;
    backend: string;
    name?: string;
    description?: string;
  }[];
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
    backendProto?: string;
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

// Deleting a service strands state it doesn't own: the zone SubZone giving its
// domain HTTPS (a cert SAN plus an http->https redirect for a host with no
// backend) and the record published at the DNS provider (the DNS sync is
// upsert-only, so nothing retracts it). useServiceDeletePreview asks the server
// what a delete would leave behind so the dialog can make the operator choose.
export function useServiceDeletePreview(name: string | null) {
  return useQuery({
    queryKey: ["services", "delete-preview", name],
    enabled: name !== null,
    // The answer depends on live config, and the dialog is short-lived — never
    // show a stale orphan set for a decision this hard to walk back.
    staleTime: 0,
    gcTime: 0,
    queryFn: () =>
      apiFetch<ServiceDeletePreviewResponse>("/services/delete/preview", {
        method: "POST",
        body: JSON.stringify({ name }),
        schema: ServiceDeletePreviewResponseSchema,
      }),
  });
}

export function orphansNeedingDecision(
  orphans: ServiceDeleteOrphan[] | undefined,
): ServiceDeleteOrphan[] {
  return (orphans ?? []).filter((o) => o.action === "delete");
}

export function useDeleteService() {
  const qc = useQueryClient();
  return useMutation({
    // Orphans are retracted only after the service is gone: with nothing owning
    // the domain, dropping its SubZone can't strip coverage from a live service.
    // A failure here leaves the service deleted, so the error says as much
    // rather than implying the whole operation rolled back.
    mutationFn: async ({
      name,
      orphans,
    }: {
      name: string;
      orphans: ServiceDeleteOrphan[];
    }) => {
      await apiFetch("/services/delete", {
        method: "POST",
        body: JSON.stringify({ name }),
      });

      const failed: string[] = [];
      for (const o of orphans) {
        try {
          if (o.kind === "https") {
            await apiFetch("/domains/ssl/remove", {
              method: "POST",
              body: JSON.stringify({ domain: o.domain, force: true }),
            });
          } else if (o.kind === "external-dns") {
            // A round-robin set is one record per value, so each needs its own
            // delete. No values means drop the whole (name, type) set.
            for (const value of o.values?.length ? o.values : [""]) {
              await apiFetch("/zones/records/delete", {
                method: "POST",
                body: JSON.stringify({
                  zone: o.zone,
                  name: o.domain,
                  type: o.recordType,
                  value,
                }),
              });
            }
          }
        } catch {
          failed.push(o.domain);
        }
      }
      if (failed.length > 0) {
        throw new Error(
          `Service deleted, but ${failed.length} orphan(s) could not be retracted: ${failed.join(", ")}`,
        );
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["domains"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
      qc.invalidateQueries({ queryKey: ["zones"] });
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

// Withdrawing a pending deletion. Nothing is restored — if the record is still
// live it just stops being retracted, and since the delete already dropped it
// from Zone.Records hz no longer claims it, so it reclassifies as observed.
export function useCancelTombstone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      zone: string;
      name: string;
      type: string;
      value: string;
    }) =>
      apiFetch("/zones/tombstones/cancel", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSettled: (_d, _e, variables) => {
      qc.invalidateQueries({ queryKey: ["zones", "records", variables.zone] });
      qc.invalidateQueries({ queryKey: ["zones"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
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

// --- The edge diagnosis ---
//
// useChecks answers "did this probe pass". This answers "why not, and whose
// device has to change" — the ladder over a name's whole result set rather
// than one probe's verdict. Same cadence as the rows it sits above, so the two
// never disagree on screen.

export function useProbeDiagnosis() {
  return useQuery({
    queryKey: ["probe-diagnosis"],
    queryFn: () =>
      apiFetch<ProbeDiagnosisData>("/checks/diagnosis", {
        schema: ProbeDiagnosisDataSchema,
      }),
    refetchInterval: 30000,
  });
}

// --- Outside-in vantages (hz-probe agents) ---
//
// hz polls these; they never dial hz. The token is write-only across the API,
// so a vantage's response says whether one is set, never what it is.

export function useRemotes() {
  return useQuery({
    queryKey: ["remotes"],
    queryFn: () =>
      apiFetch<RemoteProbe[]>("/checks/remotes", {
        schema: RemoteProbeListSchema,
      }),
    refetchInterval: 30000,
  });
}

// invalidateRemotes refreshes the vantage list and the check rows it feeds.
function invalidateRemotes(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["remotes"] });
  qc.invalidateQueries({ queryKey: ["checks"] });
  qc.invalidateQueries({ queryKey: ["settings"] });
}

export function useAddRemote() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: RemoteProbeRequest) =>
      apiFetch("/checks/remotes/add", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => invalidateRemotes(qc),
  });
}

export function useUpdateRemote() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: RemoteProbeRequest) =>
      apiFetch("/checks/remotes/update", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => invalidateRemotes(qc),
  });
}

export function useDeleteRemote() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/checks/remotes/delete", {
        method: "POST",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => invalidateRemotes(qc),
  });
}

// useTestRemote polls an agent once without saving anything, so a wrong token
// or an unreachable host fails in the dialog rather than silently in the poll
// loop ten minutes later.
export function useTestRemote() {
  return useMutation({
    mutationFn: (input: RemoteProbeRequest) =>
      apiFetch<RemoteProbeTest>("/checks/remotes/test", {
        method: "POST",
        body: JSON.stringify(input),
        schema: RemoteProbeTestSchema,
      }),
  });
}

// useMintRemoteToken asks hz for a token for a vantage that does not exist
// yet, so the install command can carry it. hz generating it is what removes
// the copy-back step: by the time the agent runs, hz already holds it.
export function useMintRemoteToken() {
  return useMutation({
    mutationFn: () =>
      apiFetch<RemoteProbeToken>("/checks/remotes/token", {
        method: "POST",
        schema: RemoteProbeTokenSchema,
      }),
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

// useAllCheckHistory fetches every check's history, bucketed and run-length
// encoded by the server. The raw form was a quarter of a megabyte every
// thirty seconds once a couple of vantages were configured.
export function useAllCheckHistory(buckets = 120) {
  return useQuery({
    queryKey: ["checks", "history", "all", buckets],
    queryFn: () =>
      apiFetch<BucketedHistoryResponse>(`/checks/history/all?buckets=${buckets}`),
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

// --- Passkeys (WebAuthn) ---
//
// Each ceremony is two round trips with the browser in the middle: begin gives
// us options carrying a challenge, the authenticator signs it, finish verifies.
// The options blob is handed to @simplewebauthn/browser untouched.

async function passkeyCeremony<T>(
  kind: "register" | "assert",
  extra: Record<string, unknown>,
): Promise<T> {
  const begin = await apiFetch<PasskeyBeginResponse>(
    `/mfa/passkey/${kind}/begin`,
    { method: "POST" },
  );
  const credential =
    kind === "register"
      ? await startRegistration({ optionsJSON: begin.options.publicKey })
      : await startAuthentication({ optionsJSON: begin.options.publicKey });

  return apiFetch<T>(`/mfa/passkey/${kind}/finish`, {
    method: "POST",
    body: JSON.stringify({
      ceremonyId: begin.ceremonyId,
      credential,
      ...extra,
    }),
  });
}

export function usePasskeyRegister() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { label: string }) =>
      passkeyCeremony<{ ok: boolean }>("register", { label: input.label }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mfa", "status"] });
    },
  });
}

export function usePasskeyAssert() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { duration: string }) =>
      passkeyCeremony<MFAVerifyResponse>("assert", { duration: input.duration }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mfa", "status"] });
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function usePasskeyDelete() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (credentialId: string) =>
      apiFetch<{ ok: boolean }>("/mfa/passkey/delete", {
        method: "POST",
        body: JSON.stringify({ credentialId }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mfa", "status"] });
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
    mutationFn: (input: {
      enabled: boolean;
      durations: string[];
      scope?: string;
      force?: boolean;
      // Omitted leaves it unchanged; 0 turns it off.
      inactivityMinutes?: number;
    }) =>
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

export function useDisableAdminToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { force?: boolean }) =>
      apiFetch<{ ok: boolean; recovery: string }>("/admin-token/disable", {
        method: "POST",
        body: JSON.stringify({ disabled: true, ...input }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["settings"] });
    },
  });
}

export function useMFAException() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; duration: string; reason: string }) =>
      apiFetch("/mfa/exception", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mfa", "settings"] });
      qc.invalidateQueries({ queryKey: ["vpn", "peers"] });
    },
  });
}

export function useMFAExceptionRevoke() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch("/mfa/exception/revoke", {
        method: "POST",
        body: JSON.stringify({ name }),
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
export const useFixLogRetention = () => useSystemFix("system/fix/log-retention");
// There is no useCreateWGConfig and no useFixHAProxyLogging. Both endpoints
// are gone: each did its work by piping an hz-built shell string through
// systemd-run to escape hz's own sandbox, and both moved to a CLI verb on the
// binary that owns the file (plan/design/privilege-audit.md §7 A) —
// `sudo hz-agent wg-create-config`, `sudo homelab-horizon fix-haproxy-logging`.
// The health card still DIAGNOSES both and names the command, which is the same
// shape the unit rows and the dependency rows already have.
export const useWriteDNSMasqConfig = () => useSystemFix("dnsmasq/write-config");
export const useReloadDNSMasq = () => useSystemFix("dnsmasq/reload");
export const useStartDNSMasq = () => useSystemFix("dnsmasq/start");
export const useFixDNSMasqInterfaces = () => useSystemFix("dnsmasq/fix-interfaces");

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

/**
 * Every declared host plus "@self", each with the records that resolve through
 * it and what those records resolve to now. The /hosts screen's one read.
 *
 * Separate from useTopology on purpose: that query is the exporter editor's
 * read-modify-write buffer (raw hosts + probed targets), and this one is the
 * derived "what points at this box" answer. Sharing a cache key would make a
 * host edit invalidate a probe sweep and vice versa.
 */
export function useHostsView() {
  return useQuery({
    queryKey: ["topology", "hosts-view"],
    queryFn: () => apiFetch<HostsViewResp>("/topology/hosts/view"),
  });
}

/**
 * Repoint one declared host. THE one-edit move: every record written @name
 * follows it in a single config write, which is what the indirection is for.
 *
 * Invalidates the topology reads rather than patching them — the resolved
 * value of every dependant changed, and re-reading is the only way to be sure
 * the screen shows what hz now holds.
 */
export function useSetHostIP() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; ip: string }) =>
      apiFetch<HostShowResp>("/topology/hosts/set", {
        method: "PUT",
        body: JSON.stringify(input),
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

// Replace the scrape-exclusion list (IPs/CIDRs never scraped). Whole-list PUT.
export function useSaveScrapeExclusions() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (scrapeExclusions: string[]) =>
      apiFetch("/topology/scrape-exclusions", {
        method: "PUT",
        body: JSON.stringify({ scrapeExclusions }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["topology"] });
    },
  });
}

// Force a synchronous exporter re-probe (rather than waiting for the 60s
// background loop). Returns the refreshed topology; seed the cache with it.
export function useReprobeExporters() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<TopologyData>("/topology/reprobe", {
        method: "POST",
        schema: TopologyDataSchema,
      }),
    onSuccess: (data) => {
      qc.setQueryData(["topology"], data);
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

// --- Local DNS records (split horizon) ---

export interface LocalDNSRecord {
  name: string;
  ip: string;
  wildcard?: boolean;
  comment?: string;
  // Set when this record overrides one derived from a service.
  shadowsDerived?: string;
}

interface LocalDNSResponse {
  records: LocalDNSRecord[];
  derived?: LocalDNSRecord[];
  enabled: boolean;
  servedAt?: string;
}

export function useLocalDNS() {
  return useQuery({
    queryKey: ["dns", "local"],
    queryFn: () => apiFetch<LocalDNSResponse>("/dns/local"),
  });
}

export function useSetLocalDNSRecord() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (record: LocalDNSRecord) =>
      apiFetch<{ ok: boolean }>("/dns/local", {
        method: "POST",
        body: JSON.stringify(record),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dns", "local"] }),
  });
}

export function useDeleteLocalDNSRecord() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch<{ ok: boolean }>(`/dns/local?name=${encodeURIComponent(name)}`, {
        method: "DELETE",
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dns", "local"] }),
  });
}

export function useLocalDNSDomain() {
  return useQuery({
    queryKey: ["dns", "local", "domain"],
    queryFn: () => apiFetch<{ domain: string }>("/dns/local/domain"),
  });
}

export function useSetLocalDNSDomain() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) =>
      apiFetch<{ ok: boolean }>("/dns/local/domain", {
        method: "POST",
        body: JSON.stringify({ domain }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dns", "local"] });
    },
  });
}

// --- single sign-on settings -------------------------------------------------
//
// The GET never returns the client secret — only whether one is stored — so a
// blank secret field on save means "keep it", not "clear it".

export function useOIDCSettings() {
  return useQuery({
    queryKey: ["oidc-settings"],
    queryFn: () => apiFetch<OIDCSettingsResp>("/settings/oidc"),
  });
}

export function useSaveOIDCSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: OIDCSettingsReq) =>
      apiFetch("/settings/oidc", { method: "PUT", body: JSON.stringify(body) }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["oidc-settings"] });
      // The login page offers SSO based on this, so its status is stale now.
      qc.invalidateQueries({ queryKey: ["oidc-status"] });
    },
  });
}

export function useDiscoverOIDC() {
  return useMutation({
    mutationFn: (body: { issuer: string }) =>
      apiFetch<OIDCDiscoverResp>("/settings/oidc/discover", {
        method: "POST",
        body: JSON.stringify(body),
      }),
  });
}

// --- Config manager -------------------------------------------------------
//
// Every one of these returns metadata. None returns a config value, because
// there are none to return: every value is sealed and hz holds no key. The UI
// shows key names, bindings, ranges, sequences, lineage and state.
//
// Decrypting happens in the `hz` CLI, never here. A browser served by hz cannot
// defend against hz — edit one line of this bundle and a pasted key is
// exfiltrated before it is ever used — so the ceremony lives in a locally
// installed binary the server does not control at the moment of use. Do not add
// a hook that accepts an environment key.

export function useCMRegistrations(state?: string) {
  return useQuery({
    queryKey: ["cm-registrations", state ?? "all"],
    queryFn: () =>
      apiFetch<CMRegistrationResp[]>(
        "/cm/registrations" + (state ? `?state=${encodeURIComponent(state)}` : ""),
      ),
    refetchInterval: 15000,
  });
}

// The public key an approver wraps to. Served separately from the queue on
// purpose: the fingerprint shown to an operator must be derived from these
// bytes, not reported alongside them by the same party that chose them.
export function useCMMachinePublicKey(registrationID: string | null) {
  return useQuery({
    queryKey: ["cm-public-key", registrationID],
    queryFn: () =>
      apiFetch<{ publicKey: string }>(
        `/cm/registrations/${encodeURIComponent(registrationID!)}/public-key`,
      ),
    enabled: !!registrationID,
  });
}

function invalidateCM(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["cm-registrations"] });
  qc.invalidateQueries({ queryKey: ["cm-current-key"] });
}

// Approval requires a wrapped blob minted by the CLI. This hook relays it; it
// cannot produce one, which is the point.
export function useCMApprove() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: CMApproveReq }) =>
      apiFetch<CMRegistrationResp>(
        `/cm/registrations/${encodeURIComponent(id)}/approve`,
        { method: "POST", body: JSON.stringify(body) },
      ),
    onSuccess: () => invalidateCM(qc),
  });
}

export function useCMDeny() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: CMDenyReq }) =>
      apiFetch<CMRegistrationResp>(
        `/cm/registrations/${encodeURIComponent(id)}/deny`,
        { method: "POST", body: JSON.stringify(body) },
      ),
    onSuccess: () => invalidateCM(qc),
  });
}

export function useCMConfigs(
  project: string,
  env: string,
  app: string,
  role: string,
) {
  return useQuery({
    queryKey: ["cm-configs", project, env, app, role],
    queryFn: () =>
      apiFetch<CMConfigResp[]>(
        `/cm/configs?project=${encodeURIComponent(project)}&env=${encodeURIComponent(env)}&app=${encodeURIComponent(app)}&role=${encodeURIComponent(role)}`,
      ),
    enabled: !!project && !!env && !!app && !!role,
  });
}

export function useCMConfig(id: string | null) {
  return useQuery({
    queryKey: ["cm-config", id],
    queryFn: () => apiFetch<CMConfigResp>(`/cm/configs/${encodeURIComponent(id!)}`),
    enabled: !!id,
  });
}

// Resolution inspection. Shadowed is the whole reason this exists: several
// open-ended configs at one address is the normal shape of supersession, so
// there are always candidates the winner passed over, and silent resolution is
// only acceptable when you can ask what it resolved to.
export function useCMResolve(
  project: string,
  env: string,
  app: string,
  role: string,
  version: string,
) {
  return useQuery({
    queryKey: ["cm-resolve", project, env, app, role, version],
    queryFn: () =>
      apiFetch<CMResolveResp>(
        `/cm/resolve?project=${encodeURIComponent(project)}&env=${encodeURIComponent(env)}&app=${encodeURIComponent(app)}` +
          `&role=${encodeURIComponent(role)}&version=${encodeURIComponent(version)}`,
      ),
    enabled: !!project && !!env && !!app && !!role && !!version,
  });
}

// The gate reads no values — it asks whether a key is bound in the target,
// never what it holds, which is why it still works when hz can read nothing.
export function useCMPromotionGate(configID: string | null, targetEnv: string) {
  return useQuery({
    queryKey: ["cm-gate", configID, targetEnv],
    queryFn: () =>
      apiFetch<CMPromotionGateResp>(
        `/cm/promote/gate?config=${encodeURIComponent(configID!)}&target=${encodeURIComponent(targetEnv)}`,
      ),
    enabled: !!configID && !!targetEnv,
  });
}

// The advisory current-key pointer. A 404 means nobody has announced one, which
// is a different fact from "the current key is X" and must be shown as such —
// a client told the wrong one seals under whatever its filesystem offers.
export function useCMCurrentKey(
  project: string,
  env: string,
  app: string,
  role: string,
) {
  return useQuery({
    queryKey: ["cm-current-key", project, env, app, role],
    queryFn: async () => {
      try {
        return await apiFetch<CMCurrentKeyResp>(
          `/cm/current-key?project=${encodeURIComponent(project)}&env=${encodeURIComponent(env)}&app=${encodeURIComponent(app)}&role=${encodeURIComponent(role)}`,
        );
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    enabled: !!project && !!env && !!app && !!role,
  });
}

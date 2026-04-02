import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type {
  AddPeerResponse,
  CreateInviteResponse,
  DashboardData,
  DomainsData,
  HAProxyConfigPreview,
  Invite,
  Service,
  SettingsData,
  VPNPeer,
  Zone,
} from "./types";
import {
  DashboardDataSchema,
  ServicesSchema,
  DomainsDataSchema,
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

// --- Mutation types ---

export interface ServiceMutationInput {
  originalName?: string;
  name: string;
  domains: string[];
  internalDNS?: { ip: string } | null;
  externalDNS?: { ip: string; ttl: number } | null;
  proxy?: {
    backend: string;
    healthCheck?: { path: string } | null;
    internalOnly: boolean;
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

export function useSyncDNS() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) =>
      apiFetch<{ ok: boolean; changed: boolean }>("/dns/sync", {
        method: "POST",
        body: JSON.stringify({ domain }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["domains"] });
    },
  });
}

export function useSyncAllDNS() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<{ ok: boolean; updated: number; failed: number }>(
        "/dns/sync-all",
        { method: "POST" },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["domains"] });
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
      qc.invalidateQueries({ queryKey: ["domains"] });
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
    mutationFn: (input: { name: string; extraIPs: string }) =>
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
    },
  });
}

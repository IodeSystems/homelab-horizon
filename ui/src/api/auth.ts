import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { startAuthentication, startRegistration } from "@simplewebauthn/browser";
import type {
  PublicKeyCredentialCreationOptionsJSON,
  PublicKeyCredentialRequestOptionsJSON,
} from "@simplewebauthn/browser";
import { apiFetch } from "./client";

interface AuthStatus {
  authenticated: boolean;
  method?: "cookie" | "vpn" | "user";
  // Set when the caller is a real account rather than the shared token.
  username?: string;
  role?: string;
  // True while no account exists: the login page offers to create the first
  // one instead of asking for a password nobody has set.
  needsBootstrap?: boolean;
  // False when the identity store did not open. The UI must not offer a login
  // it cannot honour.
  usersAvailable?: boolean;
  // Multi-instance HA fleet metadata (empty for single-instance)
  peerId?: string;
  configPrimary?: boolean;
  primaryId?: string;
}

interface LoginResponse {
  ok: boolean;
  error?: string;
  invite?: boolean;
  redirect?: string;
  // The password was right but the account has a second factor. Continue with
  // pendingId rather than starting over.
  mfaRequired?: boolean;
  pendingId?: string;
  factors?: string[];
  // The password was right but has expired; it must be changed before a
  // session is issued.
  passwordExpired?: boolean;
}

export interface User {
  id: string;
  username: string;
  email?: string;
  role: string;
  disabled?: boolean;
  createdAt?: string;
  lastLogin?: string;
}

interface UsersResponse {
  users: User[];
  canDisableAdminToken: boolean;
}

export function useAuthStatus() {
  return useQuery({
    queryKey: ["auth", "status"],
    queryFn: () => apiFetch<AuthStatus>("/auth/status"),
    retry: false,
    staleTime: 30_000,
  });
}

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (creds: { token?: string; username?: string; password?: string }) =>
      apiFetch<LoginResponse>("/auth/login", {
        method: "POST",
        body: JSON.stringify(creds),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["auth"] });
    },
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<{ ok: boolean }>("/auth/logout", { method: "POST" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["auth"] });
    },
  });
}

export function useUsers() {
  return useQuery({
    queryKey: ["users"],
    queryFn: () => apiFetch<UsersResponse>("/users"),
  });
}

export function useCreateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      username: string;
      email?: string;
      role?: string;
      password?: string;
    }) =>
      apiFetch<User>("/users", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["users"] });
      queryClient.invalidateQueries({ queryKey: ["auth"] });
    },
  });
}

export function useSetPassword() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      userId?: string;
      currentPassword?: string;
      password: string;
    }) =>
      apiFetch<{ ok: boolean }>("/users/password", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["users"] });
    },
  });
}

export function useSetUserDisabled() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { userId: string; disabled: boolean }) =>
      apiFetch<{ ok: boolean }>("/users/disable", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["users"] });
    },
  });
}

// --- Account second factors ---

export interface AccountFactor {
  id: string;
  kind: string;
  label?: string;
  createdAt?: string;
  lastUsed?: string;
  cloneWarning?: boolean;
}

interface AccountFactorsResponse {
  factors: AccountFactor[];
  passkeysAvailable: boolean;
  passkeysUnavailableReason?: string;
}

interface TOTPEnrollResponse {
  provisioningUri: string;
  secret: string;
}

interface PasskeyBegin {
  ceremonyId: string;
  options: { publicKey: PublicKeyCredentialCreationOptionsJSON };
}

export function useAccountFactors() {
  return useQuery({
    queryKey: ["account", "factors"],
    queryFn: () => apiFetch<AccountFactorsResponse>("/account/factors"),
  });
}

export function useTOTPEnroll() {
  return useMutation({
    mutationFn: () =>
      apiFetch<TOTPEnrollResponse>("/account/totp/enroll", { method: "POST" }),
  });
}

export function useTOTPConfirm() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (code: string) =>
      apiFetch<{ ok: boolean }>("/account/totp/confirm", {
        method: "POST",
        body: JSON.stringify({ code }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["account", "factors"] }),
  });
}

export function useRemoveFactor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<{ ok: boolean }>(`/account/factors?id=${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["account", "factors"] }),
  });
}

// Enrolling a passkey on the signed-in account. Two round trips with the
// authenticator in the middle; the options blob passes through untouched.
export function useAccountPasskeyRegister() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (label: string) => {
      const begin = await apiFetch<PasskeyBegin>(
        "/account/passkey/register/begin",
        { method: "POST" },
      );
      const credential = await startRegistration({
        optionsJSON: begin.options.publicKey,
      });
      return apiFetch<{ ok: boolean }>("/account/passkey/register/finish", {
        method: "POST",
        body: JSON.stringify({ ceremonyId: begin.ceremonyId, credential, label }),
      });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["account", "factors"] }),
  });
}

// Completing a login that stopped for a second factor.
export function useLoginTOTP() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { pendingId: string; code: string }) =>
      apiFetch<LoginResponse>("/auth/login/totp", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["auth"] }),
  });
}

export function useLoginPasskey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (pendingId: string) => {
      const begin = await apiFetch<PasskeyBegin>("/auth/login/passkey/begin", {
        method: "POST",
        body: JSON.stringify({ pendingId }),
      });
      const credential = await startAuthentication({
        optionsJSON: begin.options
          .publicKey as unknown as PublicKeyCredentialRequestOptionsJSON,
      });
      return apiFetch<LoginResponse>("/auth/login/passkey/finish", {
        method: "POST",
        body: JSON.stringify({
          pendingId,
          ceremonyId: begin.ceremonyId,
          credential,
        }),
      });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["auth"] }),
  });
}

interface OIDCStatus {
  enabled: boolean;
  name: string;
  reason?: string;
}

export function useOIDCStatus() {
  return useQuery({
    queryKey: ["auth", "oidc"],
    queryFn: () => apiFetch<OIDCStatus>("/auth/oidc/status"),
    retry: false,
    staleTime: 60_000,
  });
}

export interface AccountPolicy {
  idleMinutes: number;
  maxFailedAttempts: number;
  lockoutMinutes: number;
  passwordMaxAgeDays: number;
  passwordHistory: number;
  minPasswordLength?: number;
}

export function usePolicy() {
  return useQuery({
    queryKey: ["policy"],
    queryFn: () => apiFetch<AccountPolicy>("/policy"),
  });
}

export function useSavePolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: AccountPolicy) =>
      apiFetch<{ ok: boolean }>("/policy", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["policy"] }),
  });
}

// Completing a login that stopped because the password expired.
export function useLoginChangePassword() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      pendingId: string;
      currentPassword: string;
      password: string;
    }) =>
      apiFetch<{ ok: boolean }>("/auth/login/change-password", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["auth"] }),
  });
}

// --- PCI checklist ---

export type PCIRemediation = {
  kind: "fix" | "decision" | "manual";
  label?: string;
  warning?: string;
  hint?: string;
};

export type PCIControl = {
  name: string;
  requirement: string;
  title: string;
  ok: boolean;
  wants: string;
  detail?: string;
  applicable: boolean;
  remediation?: PCIRemediation;
};

export type SAQLevel = "" | "a" | "a-ep" | "d";

export function usePCIControls() {
  return useQuery({
    queryKey: ["pci-controls"],
    queryFn: () =>
      apiFetch<{
        controls: PCIControl[];
        unmet: number;
        disclaimer: string;
        saqLevel: SAQLevel;
      }>("/pci/controls"),
  });
}

export function useSetSAQLevel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (level: SAQLevel) =>
      apiFetch<{ ok: boolean }>("/pci/level", {
        method: "PUT",
        body: JSON.stringify({ level }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["pci-controls"] }),
  });
}

// Disabling the shared admin token. Its own hook rather than a generic
// "apply fix" call, because the endpoint has its own refusals — it will not
// leave the box with no way in — and those messages should reach the operator
// unchanged.
export function useDisableAdminToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      // disabled:true is required — the endpoint refuses to re-enable, because
      // the token is exactly what an attacker holding it would use to turn
      // itself back on. Re-enabling is a console restart.
      apiFetch<{ ok: boolean; recovery: string }>("/admin-token/disable", {
        method: "POST",
        body: JSON.stringify({ disabled: true }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["pci-controls"] }),
  });
}

export function useFixLogRetention() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiFetch<{ ok: boolean }>("/system/fix/log-retention", { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["pci-controls"] }),
  });
}

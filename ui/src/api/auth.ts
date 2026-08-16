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

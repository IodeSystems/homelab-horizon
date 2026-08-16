import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
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

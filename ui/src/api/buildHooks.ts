/**
 * The build registry (`internal/server/handlers_api_builds.go`): builds hz
 * numbered, one build's log, and the build agents allowed to start them.
 */
import type {
  BuildAgentReq,
  BuildAgentRemovedResp,
  BuildAgentResp,
  BuildAgentTokenResp,
  BuildResp,
} from "./generated-types";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, apiFetch } from "./client";

/** A project's builds, newest first; "" is every project. */
export function useBuilds(project: string) {
  return useQuery({
    queryKey: ["builds", project],
    queryFn: () =>
      apiFetch<BuildResp[]>(project ? `/builds?project=${encodeURIComponent(project)}` : "/builds"),
    retry: false,
  });
}

/** One build by its number — an empty list is "no such build", not a failure. */
export function useBuildByNumber(project: string, number: number) {
  return useQuery({
    queryKey: ["builds", project, "number", number],
    queryFn: () =>
      apiFetch<BuildResp[]>(`/builds?project=${encodeURIComponent(project)}&number=${number}`),
    retry: false,
    // A running build's state moves; re-ask while it runs.
    refetchInterval: (q) => (q.state.data?.[0]?.result === "running" ? 3000 : false),
  });
}

/** What one GET of a log answered. */
export interface LogTail {
  text: string;
  size: number;
  truncated: boolean;
}

/** How much of a log's end the build page shows. */
export const LOG_TAIL_BYTES = 64 * 1024;

/**
 * The last LOG_TAIL_BYTES of a build's log, re-asked every 3s while the build
 * runs. A 410 (deleted by retention) is an error with the server's sentence.
 */
export function useBuildLogTail(id: number, running: boolean) {
  return useQuery({
    queryKey: ["builds", "log", id],
    queryFn: async (): Promise<LogTail> => {
      const res = await fetch(`/api/v1/builds/${id}/log?tail=${LOG_TAIL_BYTES}`, { credentials: "include" });
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: res.statusText }));
        throw new ApiError(res.status, body.error || res.statusText);
      }
      return {
        text: await res.text(),
        size: Number(res.headers.get("X-Log-Size") ?? "0"),
        truncated: res.headers.get("X-Log-Truncated") === "true",
      };
    },
    retry: false,
    enabled: id > 0,
    refetchInterval: running ? 3000 : false,
  });
}

/** The build agents: name, project, host label. Never a token. */
export function useBuildAgents() {
  return useQuery({
    queryKey: ["build-agents"],
    queryFn: () => apiFetch<BuildAgentResp[]>("/build-agents"),
    retry: false,
  });
}

/** Register (or rotate) a build agent — the answer is the ONLY time its token is shown. */
export function useMintBuildAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: BuildAgentReq) =>
      apiFetch<BuildAgentTokenResp>("/build-agents", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["build-agents"] }),
  });
}

/** Revoke a build agent's token. */
export function useRemoveBuildAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch<BuildAgentRemovedResp>(`/build-agents/${encodeURIComponent(name)}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["build-agents"] }),
  });
}


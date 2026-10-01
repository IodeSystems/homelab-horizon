/**
 * The tested staging -> prod release, from the project Overview: what each rung
 * last REPORTED running (`GET /deploys/latest`), the promotion record
 * (`GET /promotions`), and the evidence-gated promote
 * (`POST /environments/promote`). Server: `internal/server/handlers_api_deploys.go`.
 */
import type {
  DeployReportResp,
  ProjectLinesResp,
  PromoteReq,
  PromoteResp,
  PromotionResp,
} from "./generated-types";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";

/** The newest report of every rung that has one. A rung absent here never reported. */
export function useLatestDeploys() {
  return useQuery({
    queryKey: ["deploys", "latest"],
    queryFn: () => apiFetch<DeployReportResp[]>("/deploys/latest"),
    retry: false,
  });
}

/**
 * The promotion record, newest first, every project — the Overview filters it
 * to the projects its scope control shows, as it does the rungs.
 */
export function usePromotions() {
  return useQuery({
    queryKey: ["promotions"],
    queryFn: () => apiFetch<PromotionResp[]>("/promotions?limit=50"),
    retry: false,
  });
}

/**
 * Promote. A refusal (409) carries the server's reason as the error message,
 * and the dialog shows it verbatim — the gate's words are the instruction.
 */
export function usePromote() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: PromoteReq) =>
      apiFetch<PromoteResp>("/environments/promote", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["environments"] });
      qc.invalidateQueries({ queryKey: ["promotions"] });
      qc.invalidateQueries({ queryKey: ["lines"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

/**
 * One project's release lines (`GET /projects/lines`): per rung, the supported
 * lines and why, each line's kept backup, the next promotion's restore-test
 * status, and the retired lines. Server: `internal/server/handlers_api_lines.go`.
 */
export function useProjectLines(project: string) {
  return useQuery({
    queryKey: ["lines", project],
    queryFn: () => apiFetch<ProjectLinesResp>(`/projects/lines?project=${encodeURIComponent(project)}`),
    retry: false,
    enabled: project !== "",
  });
}

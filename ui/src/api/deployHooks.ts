/**
 * The tested staging -> prod release, from the project Overview: what each rung
 * last REPORTED running (`GET /deploys/latest`), the promotion record
 * (`GET /promotions`), and the evidence-gated promote
 * (`POST /environments/promote`). Server: `internal/server/handlers_api_deploys.go`.
 */
import type {
  ApplyReq,
  ApplyResp,
  ArtifactResp,
  HoldReq,
  HoldStateResp,
  InstanceTokenReq,
  InstanceTokenResp,
  RungDeployStateResp,
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

/**
 * N4a — each declared rung's newest APPLY and hold (`GET /deploys/applied`).
 * A promotion approves; an apply is what the rung runs, and what a nested hz
 * pulls. Server: `internal/server/handlers_api_apply.go`.
 */
export function useAppliedState() {
  return useQuery({
    queryKey: ["deploys", "applied"],
    queryFn: () => apiFetch<RungDeployStateResp[]>("/deploys/applied"),
    retry: false,
  });
}

/** Every artifact record, so a promotion can say its artifact was deleted. */
export function useArtifacts() {
  return useQuery({
    queryKey: ["artifacts"],
    queryFn: () => apiFetch<ArtifactResp[]>("/artifacts"),
    retry: false,
  });
}

function invalidateRelease(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["deploys", "applied"] });
  qc.invalidateQueries({ queryKey: ["promotions"] });
  qc.invalidateQueries({ queryKey: ["artifacts"] });
}

/** Apply the rung's newest promotion. A refusal carries the server's reason. */
export function useApply() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: ApplyReq) =>
      apiFetch<ApplyResp>("/environments/apply", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: () => invalidateRelease(qc),
  });
}

/** Hold (reason required) or unhold a rung. */
export function useHold(on: boolean) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: HoldReq) =>
      apiFetch<HoldStateResp>(on ? "/environments/hold" : "/environments/unhold", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    onSuccess: () => invalidateRelease(qc),
  });
}

/** Mint a nested hz's instance token — the answer is the ONLY time it is shown. */
export function useMintInstanceToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: InstanceTokenReq) =>
      apiFetch<InstanceTokenResp>("/machines/hz-token", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["instances"] });
      qc.invalidateQueries({ queryKey: ["machines"] });
    },
  });
}

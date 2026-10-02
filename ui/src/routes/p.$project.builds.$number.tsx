/**
 * `/p/$project/builds/$number` — one build: its facts, its steps, the tail of
 * its log (re-asked while it runs), and its artifact. This is the `build_url`
 * hz answers a start with.
 *
 * A build is OWNED by its project (it carries `project`), so a detail route
 * under the project is the one address it has — not the derived-surface case
 * `projectRoutes.ts` forbids.
 */
import { createFileRoute, Link } from "@tanstack/react-router";
import { Box, Typography } from "@mui/material";
import { useBuildByNumber, useBuildLogTail } from "../api/buildHooks";
import { BuildFacts, BuildSteps, LogTailView, sourceOf } from "../components/model/BuildBits";
import { EmptyRow, TabHeader } from "../components/model/FlowBits";
import { useProjectContext } from "../components/model/ProjectBits";

function BuildPage() {
  const { resolution } = useProjectContext();
  const { number } = Route.useParams();
  const name = resolution.found ? resolution.route.name : "";
  const n = Number(number);
  const q = useBuildByNumber(name, Number.isInteger(n) && n > 0 ? n : 0);
  const build = q.data?.[0];
  const log = useBuildLogTail(build?.id ?? 0, build?.result === "running");
  if (!resolution.found) return null;
  const back = (
    <Link to="/p/$project/builds" params={{ project: name }}>
      All builds
    </Link>
  );
  if (q.data !== undefined && !build) {
    return (
      <Box>
        <TabHeader title={`Build #${number} in ${name}`} action={back} />
        <EmptyRow text={`hz numbered no build #${number} in ${name}.`} />
      </Box>
    );
  }
  return (
    <Box>
      <TabHeader title={`Build #${number} in ${name}`} action={back} />
      {build ? (
        <>
          <BuildFacts build={build} />
          <Typography variant="subtitle2" sx={{ mb: 1 }}>
            Steps
          </Typography>
          <BuildSteps steps={build.steps} />
          <Typography variant="subtitle2" sx={{ mb: 1 }}>
            Log
          </Typography>
          <LogTailView build={build} source={sourceOf(log)} />
        </>
      ) : q.error ? (
        <EmptyRow text={`hz could not be asked for build #${number}: ${q.error.message}`} />
      ) : (
        <Typography sx={{ color: "text.secondary" }}>Asking hz for build #{number}…</Typography>
      )}
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/builds/$number")({
  component: BuildPage,
});

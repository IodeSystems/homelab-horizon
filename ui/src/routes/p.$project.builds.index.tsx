/**
 * `/p/$project/builds` — the builds hz numbered for this project, newest first
 * (`GET /api/v1/builds?project=`), and the build agents registered for it,
 * grouped by host. A build's number, version and artifact come from hz; its
 * page is `/p/$project/builds/$number`. A build agent is not a machine: it
 * never shows on the project's Machines or Instances tabs.
 */
import { createFileRoute } from "@tanstack/react-router";
import { Box } from "@mui/material";
import { useBuildAgents, useBuilds } from "../api/buildHooks";
import { BuildAgentsSection, BuildsTable, sourceOf } from "../components/model/BuildBits";
import { TabHeader } from "../components/model/FlowBits";
import { useProjectContext } from "../components/model/ProjectBits";

function ProjectBuilds() {
  const { resolution } = useProjectContext();
  const name = resolution.found ? resolution.route.name : "";
  const q = useBuilds(name);
  const agents = useBuildAgents();
  if (!resolution.found) return null;
  return (
    <Box>
      <TabHeader title={`Builds in ${name}`} />
      <BuildsTable source={sourceOf(q)} emptyText={`No builds in ${name} — a build agent starts one.`} />
      <Box sx={{ mt: 3 }}>
        <BuildAgentsSection source={sourceOf(agents)} projects={[name]} project={name} />
      </Box>
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/builds/")({
  component: ProjectBuilds,
});

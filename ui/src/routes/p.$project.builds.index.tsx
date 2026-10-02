/**
 * `/p/$project/builds` — the builds hz numbered for this project, newest first
 * (`GET /api/v1/builds?project=`). A build's number, version and artifact come
 * from hz; its page is `/p/$project/builds/$number`.
 */
import { createFileRoute } from "@tanstack/react-router";
import { Box } from "@mui/material";
import { useBuilds } from "../api/buildHooks";
import { BuildsTable, sourceOf } from "../components/model/BuildBits";
import { TabHeader } from "../components/model/FlowBits";
import { useProjectContext } from "../components/model/ProjectBits";

function ProjectBuilds() {
  const { resolution } = useProjectContext();
  const name = resolution.found ? resolution.route.name : "";
  const q = useBuilds(name);
  if (!resolution.found) return null;
  return (
    <Box>
      <TabHeader title={`Builds in ${name}`} />
      <BuildsTable source={sourceOf(q)} emptyText={`No builds in ${name} — a builder starts one.`} />
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/builds/")({
  component: ProjectBuilds,
});

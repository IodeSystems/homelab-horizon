/**
 * `/builds` — every build hz numbered, every project, newest first. The same
 * surface as `/p/$project/builds`, unscoped, so the Builds tab is there at
 * every scope like the other tabs.
 */
import { createFileRoute } from "@tanstack/react-router";
import { Box } from "@mui/material";
import { useBuilds } from "../api/buildHooks";
import { BuildsTable, sourceOf } from "../components/model/BuildBits";
import { TabHeader } from "../components/model/FlowBits";

function AllBuilds() {
  const q = useBuilds("");
  return (
    <Box>
      <TabHeader title="Builds" />
      <BuildsTable source={sourceOf(q)} showProject emptyText="No builds — a build agent starts one." />
    </Box>
  );
}

export const Route = createFileRoute("/builds")({
  component: AllBuilds,
});

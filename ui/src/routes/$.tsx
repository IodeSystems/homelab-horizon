/**
 * `/$` — every path no other route claims.
 *
 * Two jobs, both answers rather than a 404:
 *
 * 1. **An old project link.** Amendment 4 put projects at the URL root —
 *    `/acme-co.storefront/segments`. Amendment 5 moved them to
 *    `/p/<bare name>/<tab>` and renamed `segments` to `network`.
 *    `legacyTarget` decides where an old address lives now, and the reader is
 *    sent there.
 * 2. **A typo.** `/setings` names no project and no screen. The answer is
 *    "there is no such page, here are the projects", with every one a link.
 */
import { createFileRoute, Navigate } from "@tanstack/react-router";
import { Box, CircularProgress, Typography } from "@mui/material";
import { useMemo } from "react";
import { useProjects } from "../api/hooks";
import { buildProjectIndex, legacyTarget, tabTarget } from "../components/model/projectRoutes.ts";
import { NoSuchProject } from "../components/model/ProjectBits";

function Unclaimed() {
  const { _splat: splat = "" } = Route.useParams();
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);

  if (projects.isLoading) {
    return (
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 4 }}>
        <CircularProgress size={24} />
        <Typography>Asking hz which projects it declares…</Typography>
      </Box>
    );
  }

  const legacy = legacyTarget(index, splat);
  if (legacy) {
    const t = tabTarget(legacy.tab, legacy.project);
    return <Navigate to={t.to as "/p/$project"} params={t.params as never} search={{} as never} replace />;
  }

  return (
    <NoSuchProject
      index={index}
      headline={`There is no page at "/${splat}".`}
      meaning="No screen has that address, and it is not an old project link hz can follow. Projects live under /p/<name>; they are listed below."
    />
  );
}

export const Route = createFileRoute("/$")({
  component: Unclaimed,
});

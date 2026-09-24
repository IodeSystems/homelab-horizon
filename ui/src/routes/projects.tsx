/**
 * `/projects` — the index. Every project, as a link to its own screen.
 *
 * THIS SCREEN NO LONGER HOLDS A SELECTION. It used to keep the chosen project
 * in `useState<string>("")` and fall back to the first row when that project
 * disappeared, which meant there was no URL for "the redline project" at all:
 * two people looking at "the projects screen" were not looking at the same
 * screen, and neither could send the other a link to what they saw.
 *
 * Selecting is a NAVIGATION now. `/$project` renders the project; this list
 * renders the same at every width, and on a phone it is the whole screen, with
 * hardware Back returning to it because going there was a real navigation
 * rather than a state change.
 *
 * The tree is still content, not navigation — Decision 1's surviving point. It
 * is one level deep and eight nodes wide on the live estate, and `flattenTree`
 * already renders it as a depth-ordered flat list rather than a nested widget.
 * What changed is only that each row is now a link with a destination.
 */
import { createFileRoute, Link } from "@tanstack/react-router";
import { Alert, Box, CircularProgress, Paper, Typography } from "@mui/material";
import { useProjects, useServices } from "../api/hooks";
import { buildProjectIndex } from "../components/model/projectRoutes.ts";
import { ProjectPickList } from "../components/model/ProjectBits";
import { ScreenHeading } from "../components/model/ModelBits";

function ProjectsIndex() {
  const projects = useProjects();
  const services = useServices();

  if (projects.isLoading) {
    return (
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 4 }}>
        <CircularProgress size={24} />
        <Typography>Asking hz what it declares…</Typography>
      </Box>
    );
  }

  if (projects.error) {
    const err = projects.error;
    return (
      <Box sx={{ p: 3 }}>
        <Alert severity="error">
          hz could not be asked what it declares:{" "}
          {err instanceof Error ? err.message : String(err)}. Nothing on this screen is a fact about
          a project — the failure is between this browser and hz.
        </Alert>
      </Box>
    );
  }

  const index = buildProjectIndex(projects.data ?? []);
  const unassigned = (services.data ?? []).filter((s) => !s.project);

  return (
    <Box>
      <ScreenHeading
        title="Projects"
        blurb="The org layout hz declares. Pick one to see the feed it installs from, every rung it declares with its posture and version, where those rungs run, and the services and domains it owns. Nothing here is editable — this is hz's own record, read back."
      />

      <ProjectPickList index={index} title="Every project, newest path first" />

      {index.ambiguousParams.length > 0 ? (
        <Alert severity="warning" sx={{ mt: 2 }}>
          {index.ambiguousParams.length} path{index.ambiguousParams.length === 1 ? "" : "s"} name
          more than one project ({index.ambiguousParams.join(", ")}). A project name may contain a
          dot, so two projects can end up addressing the same way — hz will not guess between them.
          Each is still reachable by its own name; renaming one with <code>hz project</code> clears
          the clash.
        </Alert>
      ) : null}

      <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          Services with no project{services.data ? ` — ${unassigned.length}` : ""}
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          A service may name no project at all. It is explicitly legal and permanent, not a
          misconfiguration — every service in a config that predates the tree is in that state.
          Nothing under a project can list one, because a project screen selects rows by the project
          they name, so they live on <Link to="/services">Services</Link>, which lists every service
          assigned or not.
        </Typography>
      </Paper>

      <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          What is NOT under a project
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          Machines, drift, hosts, DNS zones, VPN peers, IP bans, checks, ports, observability and
          settings are gateway-wide and stay in the left-hand nav. A machine record is{" "}
          <code>{"{Name, Segments, Note}"}</code> and carries no project deliberately: an environment
          never modifies a machine, it is a coordinate of an instance. The gateway box hosting
          instances from several projects has one page, under Machines, and its diff lives there.
        </Typography>
      </Paper>
    </Box>
  );
}

export const Route = createFileRoute("/projects")({
  component: ProjectsIndex,
});

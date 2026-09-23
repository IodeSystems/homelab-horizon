/**
 * Projects — the tree, what each rung declares, and where it actually runs.
 *
 * The job (plan/ui-redesign.md): browse the org layout; read a rung's posture,
 * lineage and declared version; find its placement.
 *
 * Read-only. The tree is CONTENT here, not navigation — it is one level deep
 * and seven nodes wide, and a machine has no project at all, so hanging the
 * nav off it would leave the most important machine with no URL.
 *
 * Four states from plan/example-projection.md §4 are the reason this screen is
 * shaped the way it is, and each is rendered as a sentence rather than as an
 * empty cell:
 *
 *   ONE ENVIRONMENT is the common case, not an unfinished ladder.
 *   NAME ≠ POSTURE: two fields, two meanings, never merged into one column.
 *   NO DECLARED VERSION means NO PACKAGE AT ALL, not "latest".
 *   NO MACHINE is the ordinary early state of a rung — and is different again
 *   from hz being unable to say, which is why the placement column has three
 *   answers and not two.
 */
import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  CircularProgress,
  Divider,
  List,
  ListItemButton,
  ListItemText,
  Paper,
  Typography,
} from "@mui/material";
import { useEnvironments, useProjects, useVersionDrift } from "../api/hooks";
import type { EnvironmentResp, InstanceVersion, ProjectResp } from "../api/generated-types";
import {
  flattenTree,
  readFeed,
  readInstanceSource,
  readPlacement,
  readRung,
  type TreeNode,
} from "../components/model/model";
import {
  CannotAskBanner,
  Declared,
  ScreenHeading,
  ToneChip,
} from "../components/model/ModelBits";

/** Posture colours the ladder, never the name. The name is plain text. */
function postureTone(posture: string) {
  switch (posture) {
    case "prod":
      return "fault" as const;
    case "staging":
      return "late" as const;
    case "dev":
      return "neutral" as const;
    default:
      return "hatched" as const;
  }
}

function Tree({
  nodes,
  selected,
  onSelect,
}: {
  nodes: TreeNode[];
  selected: string;
  onSelect: (name: string) => void;
}) {
  return (
    <Paper sx={{ p: 1, minWidth: 240 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700, px: 1, py: 1 }}>
        The tree
      </Typography>
      <List dense disablePadding>
        {nodes.map((n) => (
          <ListItemButton
            key={n.project.name}
            selected={n.project.name === selected}
            onClick={() => onSelect(n.project.name)}
            sx={{ pl: 1 + n.depth * 2 }}
          >
            <ListItemText
              primary={n.project.name}
              secondary={
                n.depth === 0 && n.children.length > 0
                  ? `${n.children.length} below`
                  : n.project.parent
                    ? `under ${n.project.parent}`
                    : "root"
              }
              slotProps={{ primary: { sx: { fontFamily: "monospace" } } }}
            />
          </ListItemButton>
        ))}
      </List>
    </Paper>
  );
}

/**
 * One rung, whole. A table row would force the four answers into four cells
 * and each of them needs a sentence, so each rung is its own panel.
 */
function Rung({
  env,
  instances,
}: {
  env: EnvironmentResp;
  instances: InstanceVersion[] | null;
}) {
  const rung = readRung(env);
  const placement = readPlacement(env, instances);
  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 1.5, bgcolor: "transparent" }}>
      <Box sx={{ display: "flex", gap: 1.5, alignItems: "center", flexWrap: "wrap", mb: 1 }}>
        <Typography sx={{ fontFamily: "monospace", fontSize: "1.05rem", fontWeight: 700 }}>
          {env.name}
        </Typography>
        <ToneChip label={`posture: ${env.posture || "none"}`} tone={postureTone(env.posture)} />
        {rung.postureRank < 0 ? (
          <Typography variant="caption" sx={{ color: "warning.main" }}>
            This posture is not one of dev · staging · prod, so hz cannot rank it and every promotion into
            it reads as upward.
          </Typography>
        ) : null}
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
        {rung.nameNote}
      </Typography>

      <Box sx={{ display: "flex", gap: 4, flexWrap: "wrap" }}>
        <Box sx={{ minWidth: 200 }}>
          <Declared
            label="promoted from"
            value={env.from}
            absentNote="No promotion edge. Nothing feeds this rung; it is where a version is declared, not where one arrives."
          />
        </Box>
        <Box sx={{ minWidth: 200 }}>
          <Declared label="declares version" value={env.version} absentNote="none declared" />
          <Typography variant="caption" sx={{ color: "text.secondary" }}>
            {rung.versionNote}
          </Typography>
        </Box>
        <Box sx={{ minWidth: 260 }}>
          <Typography
            variant="caption"
            sx={{ color: "text.secondary", textTransform: "uppercase", letterSpacing: 0.5 }}
          >
            placement
          </Typography>
          <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap", mb: 0.5 }}>
            <ToneChip
              label={placement.headline}
              tone={placement.tone}
              hatched={placement.knowledge === "unknown"}
              dashed={placement.knowledge === "unknown"}
            />
          </Box>
          <Typography variant="caption" sx={{ color: "text.secondary" }}>
            {placement.meaning}
          </Typography>
        </Box>
      </Box>
    </Paper>
  );
}

function ProjectPanel({
  project,
  environments,
  instances,
  childProjects,
}: {
  project: ProjectResp;
  environments: EnvironmentResp[];
  instances: InstanceVersion[] | null;
  childProjects: string[];
}) {
  const feed = readFeed(project);
  const rungs = environments.filter((e) => e.project === project.name);
  const services = project.services ?? [];

  return (
    <Box sx={{ flex: 1, minWidth: 0 }}>
      <Paper sx={{ p: 2, mb: 2 }}>
        <Typography variant="h5" sx={{ fontWeight: 700, fontFamily: "monospace" }}>
          {project.name}
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
          {project.parent
            ? `A child of ${project.parent}. It inherits the package feed and nothing else — not a version, not a machine, not a service.`
            : "A root project. It inherits nothing; what it declares cascades down."}
          {childProjects.length > 0
            ? ` ${childProjects.length} project${childProjects.length === 1 ? "" : "s"} sit${childProjects.length === 1 ? "s" : ""} under it: ${childProjects.join(", ")}.`
            : " Nothing sits under it."}
        </Typography>

        <Divider sx={{ my: 1.5 }} />

        <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap", mb: 0.5 }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
            Package feed
          </Typography>
          <ToneChip label={feed.headline} tone={feed.tone} />
        </Box>
        <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
          {feed.meaning}
        </Typography>
        {project.resolvedFeed ? (
          <Box sx={{ display: "flex", gap: 3, flexWrap: "wrap" }}>
            <Declared label="url" value={project.resolvedFeed.url} absentNote="no url" />
            <Declared label="suite" value={project.resolvedFeed.suite} absentNote="no suite" />
            <Declared label="component" value={project.resolvedFeed.component} absentNote="no component" />
            <Declared
              label="signing key"
              value={project.resolvedFeed.keyId}
              absentNote="no key id — the feed is not pinned to a signing key"
            />
          </Box>
        ) : null}
      </Paper>

      <Paper sx={{ p: 2, mb: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
          Environments
        </Typography>
        {rungs.length === 0 ? (
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            This project declares no environment. Legal — a project is declared before anything moves into
            it — and nothing can be placed here until one exists, because an instance&apos;s address names a
            rung.
          </Typography>
        ) : (
          <>
            <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
              {rungs.length === 1
                ? "One environment is the common case, not an unfinished ladder. Nothing here is missing."
                : `${rungs.length} rungs. The promotion edges below are the ladder; posture, not name, is what hz compares them by.`}
            </Typography>
            {[...rungs]
              .sort((a, b) => a.name.localeCompare(b.name))
              .map((e) => (
                <Rung key={e.name} env={e} instances={instances} />
              ))}
          </>
        )}
      </Paper>

      <Paper sx={{ p: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
          Services assigned here
        </Typography>
        {services.length === 0 ? (
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            No service names this project. That is an ordinary state — a project is declared before anything
            moves into it, and a root may never hold a service at all.
          </Typography>
        ) : (
          <Typography sx={{ fontFamily: "monospace" }}>{services.join(" · ")}</Typography>
        )}
      </Paper>
    </Box>
  );
}

/**
 * Services naming no project.
 *
 * EXPLICITLY LEGAL AND PERMANENT, sorted last, and labelled as such rather
 * than flagged: `ValidateProjects` skips a service naming no project, and that
 * is the state every service in an existing config is in.
 *
 * This screen cannot list them — `/api/v1/projects` returns a service list per
 * project and nothing carries the unassigned ones — so it says that, instead
 * of implying by silence that there are none.
 */
function UnassignedNote() {
  return (
    <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
        Services with no project
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        A service may name no project at all. It is explicitly legal and permanent, not a misconfiguration —
        every service in a config that predates the tree is in that state. They are not listed here because
        this screen reads the tree, and a service outside it is not a row in the tree; Services lists every
        service, assigned or not.
      </Typography>
    </Paper>
  );
}

function ProjectsScreen() {
  const projects = useProjects();
  const environments = useEnvironments();
  const drift = useVersionDrift();
  const [selected, setSelected] = useState<string>("");

  if (projects.isLoading || environments.isLoading) {
    return (
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 4 }}>
        <CircularProgress size={24} />
        <Typography>Asking hz what it declares…</Typography>
      </Box>
    );
  }

  if (projects.error || environments.error) {
    const err = projects.error ?? environments.error;
    return (
      <Box sx={{ p: 3 }}>
        <Alert severity="error">
          hz could not be asked what it declares: {err instanceof Error ? err.message : String(err)}. Nothing
          on this screen is a fact about a project — the failure is between this browser and hz.
        </Alert>
      </Box>
    );
  }

  const tree = flattenTree(projects.data ?? []);
  const envs = environments.data ?? [];
  // null, deliberately, and not []: hz has not answered, which is a different
  // answer from "nothing is placed". A PENDING read counts as not-answered too
  // — `?? []` there would caption every prod rung "no machine yet" for as long
  // as the read takes. Every placement reading below branches on the null.
  const source = readInstanceSource(drift, "The placement column on every rung below");
  const instances: InstanceVersion[] | null = source.known ? source.instances : null;
  // `undefined` only when the tree is empty, which is its own rendered state
  // below. A project the operator selected and that has since gone falls back
  // to the first row rather than to a blank panel.
  const current: TreeNode | undefined = tree.find((n) => n.project.name === selected) ?? tree[0];

  return (
    <Box sx={{ p: 3 }}>
      <ScreenHeading
        title="Projects"
        blurb="What hz declares: the project tree, the feed each project installs from, every rung's posture and declared version, and where those rungs actually run. Nothing here is editable — this is hz's own record, read back."
      />

      {source.known ? null : <CannotAskBanner what={source.what} detail={source.detail} />}

      {source.known && source.unadmitted > 0 ? (
        <Alert severity="info" sx={{ mb: 2 }}>
          {source.unadmitted} registered address{source.unadmitted === 1 ? " is" : "es are"} left out of the
          placement column because nobody has approved {source.unadmitted === 1 ? "it" : "them"} yet. A
          pending address is one a machine has asked for and no admin has granted; it is waiting in Config →
          Approvals, not missing.
        </Alert>
      ) : null}

      {current === undefined ? (
        <Alert severity="info">
          hz declares no project. This is an empty tree, not a failed read — nothing has been declared yet,
          and until a project exists there is no rung for an instance to name.
        </Alert>
      ) : (
        <Box sx={{ display: "flex", gap: 2, alignItems: "flex-start", flexWrap: "wrap" }}>
          <Tree nodes={tree} selected={current.project.name} onSelect={setSelected} />
          <ProjectPanel
            project={current.project}
            environments={envs}
            instances={instances}
            childProjects={current.children}
          />
        </Box>
      )}

      <UnassignedNote />
    </Box>
  );
}

export const Route = createFileRoute("/projects")({
  component: ProjectsScreen,
});

/**
 * `/$project` — what one project declares.
 *
 * The overview that `/projects` used to render into a detail panel beside a
 * picker held in `useState`. It is a route now, so "the storefront project" has
 * an address: it can be linked, bookmarked, and put in a ticket.
 *
 * Four states from plan/design/example-projection.md §4 shape this screen, and each is
 * a sentence rather than an empty cell:
 *
 *   ONE ENVIRONMENT is the common case, not an unfinished ladder.
 *   NAME ≠ POSTURE: two fields, two meanings, never merged into one column.
 *   NO DECLARED VERSION means NO PACKAGE AT ALL, not "latest".
 *   NO MACHINE is the ordinary early state of a rung — and is different again
 *   from hz being unable to say, which is why placement has three answers.
 *
 * A fifth, which this screen is the only one to hit: NO ENVIRONMENT AT ALL.
 * Three of the eight live projects are in that state and it is not an error —
 * a project is declared before anything moves into it.
 */
import { createFileRoute } from "@tanstack/react-router";
import { Alert, Box, Divider, Paper, Typography } from "@mui/material";
import { useEnvironments, useServices, useVersionDrift } from "../api/hooks";
import type { EnvironmentResp, InstanceVersion } from "../api/generated-types";
import {
  readFeed,
  readInstanceSource,
  readPlacement,
  readRung,
} from "../components/model/model";
import { inScope } from "../components/model/projectRoutes.ts";
import {
  CannotAskBanner,
  Declared,
  ToneChip,
} from "../components/model/ModelBits";
import {
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

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

/**
 * One rung, whole. A table row would force the four answers into four cells
 * and each of them needs a sentence, so each rung is its own panel.
 */
function Rung({
  env,
  instances,
  showProject,
}: {
  env: EnvironmentResp;
  instances: InstanceVersion[] | null;
  /** The rung's own project, rendered because the list may span the subtree. */
  showProject: React.ReactNode;
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
        {showProject}
        {rung.postureRank < 0 ? (
          <Typography variant="caption" sx={{ color: "warning.main" }}>
            This posture is not one of dev · staging · prod, so hz cannot rank it and every promotion
            into it reads as upward.
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

function ProjectOverview() {
  const { index, resolution, scope, param } = useProjectContext();
  const environments = useEnvironments();
  const services = useServices();
  const drift = useVersionDrift();

  // The layout above renders the no-such-project screen and no <Outlet/>, so
  // this component only ever runs with a resolved project. The guard is here
  // because the type says it can be otherwise and a cast would be a lie.
  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);

  const rungs = (environments.data ?? []).filter((e) => inScope(reading.names, e.project));
  const mine = (services.data ?? []).filter((s) => inScope(reading.names, s.project));

  // null, deliberately, and not []: hz has not answered, which is a different
  // answer from "nothing is placed". A PENDING read counts as not-answered too
  // — `?? []` there would caption every prod rung "no machine yet" for as long
  // as the read takes.
  const source = readInstanceSource(drift, "The placement column on every rung below");
  const instances: InstanceVersion[] | null = source.known ? source.instances : null;
  // The record itself travels on the route, so the feed is read from what hz
  // actually sent rather than re-derived from the tree.
  const feed = readFeed(route.project);
  const resolvedFeed = route.project.resolvedFeed;

  return (
    <Box>
      <ScopeControl reading={reading} to="/$project" param={param} />

      {source.known ? null : <CannotAskBanner what={source.what} detail={source.detail} />}

      {source.known && source.unadmitted > 0 ? (
        <Alert severity="info" sx={{ mb: 2 }}>
          {source.unadmitted} registered address{source.unadmitted === 1 ? " is" : "es are"} left out
          of the placement column because nobody has approved{" "}
          {source.unadmitted === 1 ? "it" : "them"} yet. A pending address is one a machine has asked
          for and no admin has granted; it is waiting in this project&apos;s Config tab, not missing.
        </Alert>
      ) : null}

      <Paper sx={{ p: 2, mb: 2 }}>
        <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap", mb: 0.5 }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
            Package feed
          </Typography>
          <ToneChip label={feed.headline} tone={feed.tone} />
        </Box>
        <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
          {feed.meaning}
        </Typography>
        {resolvedFeed ? (
          <Box sx={{ display: "flex", gap: 3, flexWrap: "wrap" }}>
            <Declared label="url" value={resolvedFeed.url} absentNote="no url" />
            <Declared label="suite" value={resolvedFeed.suite} absentNote="no suite" />
            <Declared label="component" value={resolvedFeed.component} absentNote="no component" />
            <Declared
              label="signing key"
              value={resolvedFeed.keyId}
              absentNote="no key id — the feed is not pinned to a signing key"
            />
          </Box>
        ) : null}
      </Paper>

      <Paper sx={{ p: 2, mb: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
          Environments
        </Typography>
        {environments.isLoading ? (
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            Asking hz what rungs it declares…
          </Typography>
        ) : rungs.length === 0 ? (
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {reading.scope === "own"
              ? `${route.name} declares no environment. Legal — a project is declared before anything moves into it — and nothing can be placed here until one exists, because an instance's address names a rung.`
              : `Neither ${route.name} nor anything below it declares an environment. Legal — a project is declared before anything moves into it — and nothing can be placed here until one exists, because an instance's address names a rung.`}
          </Typography>
        ) : (
          <>
            <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
              {rungs.length === 1
                ? "One environment is the common case, not an unfinished ladder. Nothing here is missing."
                : `${rungs.length} rungs. The promotion edges below are the ladder; posture, not name, is what hz compares them by.`}
            </Typography>
            {[...rungs]
              .sort((a, b) => a.project.localeCompare(b.project) || a.name.localeCompare(b.name))
              .map((e) => (
                <Rung
                  key={`${e.project}/${e.name}`}
                  env={e}
                  instances={instances}
                  showProject={<LocationCell index={index} project={e.project} />}
                />
              ))}
          </>
        )}
      </Paper>

      <Paper sx={{ p: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
          Services assigned here
        </Typography>
        <Divider sx={{ my: 1 }} />
        {services.isLoading ? (
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            Asking hz which services name this project…
          </Typography>
        ) : mine.length === 0 ? (
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            No service names {reading.scope === "own" ? route.name : "this project or anything below it"}.
            That is an ordinary state — a project is declared before anything moves into it, and a
            root may never hold a service at all.
          </Typography>
        ) : (
          <Box sx={{ display: "flex", flexDirection: "column", gap: 1 }}>
            {mine.map((s) => (
              <Box key={s.name} sx={{ display: "flex", gap: 2, alignItems: "baseline", flexWrap: "wrap" }}>
                <Typography sx={{ fontFamily: "monospace", minWidth: 180 }}>{s.name}</Typography>
                <LocationCell index={index} project={s.project} />
                <Typography variant="caption" sx={{ color: "text.secondary" }}>
                  {s.domains.join(" · ")}
                </Typography>
              </Box>
            ))}
          </Box>
        )}
      </Paper>
    </Box>
  );
}

export const Route = createFileRoute("/$project/")({
  component: ProjectOverview,
});

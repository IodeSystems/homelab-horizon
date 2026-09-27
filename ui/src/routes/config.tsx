/**
 * `/config` — the Config tab with no project selected.
 *
 * (plan/design/ui.md, Decision 1, amendment 5 — reverses Decision L.) Config is
 * one of the six tabs, so it exists at every scope. The two panels that LIST
 * registrations show every project's: `/api/v1/cm/registrations` already
 * returns all of them (filtered only by state), and each row names its project.
 *
 * The three panels that take an ADDRESS — configs, resolve, promote — need a
 * project, because an address is `<project>/<env>/<app>/<role>`. The operator's
 * pick: a project picker here, rather than leaving them out. The pick is a
 * search param (`?project=`), not `useState`, so the prefilled screen has an
 * address of its own (Seam 7).
 *
 * Everything here is METADATA; hz holds no key. See `p.$project.config.tsx`
 * for why nothing on this page accepts one.
 */
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Box, MenuItem, Paper, TextField, Typography } from "@mui/material";
import { useMemo } from "react";
import { useProjects } from "../api/hooks";
import { CMApprovals } from "../components/CMApprovals";
import { CMMachines } from "../components/CMMachines";
import { CMConfigs } from "../components/CMConfigs";
import { CMResolve } from "../components/CMResolve";
import { CMPromote } from "../components/CMPromote";
import { buildProjectIndex } from "../components/model/projectRoutes.ts";

interface ConfigSearch {
  project?: string;
}

function UnscopedConfig() {
  const { project = "" } = Route.useSearch();
  const navigate = useNavigate({ from: "/config" });
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const address = { project, env: "", app: "", role: "" };

  return (
    <Box>
      <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
        Config
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
        Registrations across every project, then blessed configs, resolution and promotion for one
        address. Values are sealed and hz cannot read them — use <code>hz config</code> to decrypt
        or to approve.
      </Typography>

      <CMApprovals />
      <CMMachines />

      <Paper variant="outlined" sx={{ p: 2, mb: 2, bgcolor: "transparent" }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          Which project&apos;s addresses
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
          An address starts with its project, so the three panels below need one. Picking it here
          prefills them; the rest of the address is typed in each panel.
        </Typography>
        <TextField
          select
          size="small"
          label="Project"
          value={index.byName.has(project) ? project : ""}
          onChange={(e) =>
            navigate({ search: e.target.value ? { project: e.target.value } : {}, replace: true })
          }
          sx={{ minWidth: 240 }}
        >
          <MenuItem value="">
            <em>none picked</em>
          </MenuItem>
          {index.routes.map((r) => (
            <MenuItem key={r.name} value={r.name} sx={{ fontFamily: "monospace", pl: 2 + r.depth * 2 }}>
              {r.name}
            </MenuItem>
          ))}
        </TextField>
      </Paper>

      {/* Keyed by the pick: each panel holds its address in state seeded from
          `initialAddress`, so a new pick has to be a new panel. */}
      <Box key={project}>
        <CMConfigs initialAddress={address} />
        <CMResolve initialAddress={address} />
        <CMPromote initialAddress={address} />
      </Box>
    </Box>
  );
}

export const Route = createFileRoute("/config")({
  component: UnscopedConfig,
  validateSearch: (search: Record<string, unknown>): ConfigSearch =>
    typeof search.project === "string" && search.project !== "" ? { project: search.project } : {},
});

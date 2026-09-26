/**
 * `/$project/config` — the config manager, at the project that owns the address.
 *
 * `CMRegistrationResp` carries `Project`, and a config address is literally
 * `<project>/<env>/<app>/<role>` — so this surface was always per-project and
 * the flat `/config` was, as the redesign put it, "an enclosure for a feature
 * with nowhere to live". It lives here now; `/config` redirects so old
 * bookmarks land somewhere true.
 *
 * Everything here is still METADATA. hz holds no key, so there is no config
 * value to show and none of these panels can show one: key names, bindings,
 * ranges, sequences, lineage and state. Decrypting anything is `hz config
 * show`, in the CLI, which is where the keys live.
 *
 * Nothing on this page accepts a key, and nothing on it does crypto. The
 * approval ceremony is in the `hz` CLI on purpose — a browser served by hz
 * cannot defend against hz, because a compromised hz would serve a bundle that
 * exfiltrates a pasted key before using it, and every mitigation this page could
 * implement would run in that same attacker-authored code. If a future change
 * adds a key field here, that reasoning is what it is undoing.
 */
import { createFileRoute } from "@tanstack/react-router";
import { Box, Typography } from "@mui/material";
import { CMApprovals } from "../components/CMApprovals";
import { CMMachines } from "../components/CMMachines";
import { CMConfigs } from "../components/CMConfigs";
import { CMResolve } from "../components/CMResolve";
import { CMPromote } from "../components/CMPromote";
import {
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectConfig() {
  const { resolution, scope, param } = useProjectContext();

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);
  const address = { project: route.name, env: "", app: "", role: "" };

  return (
    <Box>
      <Typography variant="h6" sx={{ fontWeight: 700, mb: 0.5 }}>
        Config in {route.name}
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
        Registrations, blessed configs and promotion for addresses under{" "}
        <code>{route.name}/&lt;environment&gt;/&lt;app&gt;/&lt;role&gt;</code>. Values are sealed and
        hz cannot read them — use <code>hz config</code> to decrypt or to approve.
      </Typography>

      <ScopeControl reading={reading} to="/$project/config" param={param} />

      {/* The two panels that LIST registrations are scoped to the projects the
          URL selects. The three that take an address are prefilled with this
          project and let you complete the rest of the tuple, because an
          address is four parts and only the first is decided by the URL. */}
      <CMApprovals projects={reading.names} />
      <CMMachines projects={reading.names} />
      <CMConfigs initialAddress={address} />
      <CMResolve initialAddress={address} />
      <CMPromote initialAddress={address} />
    </Box>
  );
}

export const Route = createFileRoute("/$project/config")({
  component: ProjectConfig,
});

/**
 * `/config` — kept as a redirect, deliberately not deleted.
 *
 * The five-tab shell that used to live here was "an enclosure for a feature
 * with nowhere to live": everything in it is addressed as
 * `(project, environment, app, role)` and `CMRegistrationResp` carries
 * `Project`, so it belongs on the project. It moved to `/$project/config`.
 *
 * This route stays because bookmarks and pasted links exist. A deleted route
 * would fall through to `/$project` and render "there is no project named
 * config", which is true and useless; landing on the project list is the one
 * answer that gets the reader where they were going.
 */
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/config")({
  beforeLoad: () => {
    throw redirect({ to: "/projects" });
  },
});

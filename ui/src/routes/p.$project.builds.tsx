/**
 * `/p/$project/builds` — the layout the Builds list (`p.$project.builds.index.tsx`)
 * and one build's page (`p.$project.builds.$number.tsx`) sit inside.
 *
 * IT RENDERS ONLY AN `<Outlet/>`, for the reason `p.$project.tsx` gives: a
 * layout without one swallows its children silently.
 */
import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/p/$project/builds")({
  component: Outlet,
});

/**
 * `/segments` — a redirect, kept for bookmarks and pasted links.
 *
 * plan/design/ui.md, Decision 1 amendment 5: the unscoped screens are
 * addressed by the tab they are, and `/network` is where this one lives now.
 */
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/segments")({
  beforeLoad: () => {
    throw redirect({ to: "/network" });
  },
});

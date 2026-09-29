/**
 * `/hosts` — a redirect, kept for bookmarks and pasted links.
 *
 * The address audit that lived here is now the detail of THIS instance on
 * `/instances/$instance` (`components/hosts/HostsAudit.tsx`); the list the
 * gateway group links to is `/instances`.
 */
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/hosts")({
  beforeLoad: () => {
    throw redirect({ to: "/instances" });
  },
});

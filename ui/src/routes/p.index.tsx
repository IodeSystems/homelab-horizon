/**
 * `/p` with no project — the unscoped Overview is the answer, so go there.
 */
import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/p/")({
  beforeLoad: () => {
    throw redirect({ to: "/" });
  },
});

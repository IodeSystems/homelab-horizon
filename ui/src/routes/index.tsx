/**
 * Overview — "is anything waiting on me?" (plan/design/ui.md, "The screens").
 *
 * plan/plan.md, Tier 2: "Dashboard → the ranked queue. Needs no new backend
 * record: `rankFleet` in the drift screen already implements the ordering.
 * The current Dashboard is four numbers that never change and never need
 * you." This route is that replacement.
 *
 * Five reads hz already serves are merged into one ranked queue
 * (`../components/overview/queue.ts`):
 *
 *   - the fleet's own report (`useAgentObserved`, via `rankFleet` — unchanged
 *     from the drift screen)
 *   - registrations nobody has approved or denied yet (`useCMRegistrations`)
 *   - a halted DNS sync (`useDNSDriftStatus`)
 *   - a failing or warning health check (`useChecks`)
 *   - an edit that has not been through Sync yet (`usePendingChanges`)
 *
 * CLAUDE.md #2 governs the whole page: each of the five is asked
 * independently, and "hz has not answered yet" is never presented as "hz
 * answered and found nothing" — see `queue.ts`'s `Answer<T>` and
 * `overviewHeadline`, which refuses to say "nothing is waiting" while any
 * source is still unknown.
 *
 * What used to be here — four inventory counts (services, domains, zones,
 * VPN peers) and a checks progress bar — is gone rather than demoted. They
 * counted things that do not change on their own and are one click away on
 * their own screens; the checks bar in particular is now redundant with the
 * "reporting a fault" tier above, which names the failing check instead of a
 * fraction. What stayed, demoted below the queue: HAProxy/SSL, because a
 * proxy that stopped running is a live fault on THIS box, and the peer-sync
 * tile, because it already carries its own age (last attempt / last success)
 * rather than a count.
 */
import { createFileRoute } from "@tanstack/react-router";
import { Alert, Box, Paper, Typography } from "@mui/material";
import CheckCircleIcon from "@mui/icons-material/CheckCircle";
import CancelIcon from "@mui/icons-material/Cancel";
import InfoIcon from "@mui/icons-material/Info";
import SyncIcon from "@mui/icons-material/Sync";
import SyncProblemIcon from "@mui/icons-material/SyncProblem";
import {
  useAgentObserved,
  useChecks,
  useCMRegistrations,
  useDashboard,
  useDNSDriftStatus,
  usePendingChanges,
} from "../api/hooks";
import type { PeerSyncStatus } from "../api/generated-types";
import { answerFrom, buildOverview } from "../components/overview/queue";
import { OverviewQueue } from "../components/overview/OverviewQueue";

function StatusDot({ active }: { active: boolean }) {
  return (
    <Box
      sx={{
        width: 10,
        height: 10,
        borderRadius: "50%",
        bgcolor: active ? "success.main" : "error.main",
        display: "inline-block",
        mr: 1,
      }}
    />
  );
}

function formatRelative(unixSeconds: number | undefined): string {
  if (!unixSeconds) return "never";
  const diffSec = Math.floor(Date.now() / 1000) - unixSeconds;
  if (diffSec < 0) return "just now";
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  return `${Math.floor(diffSec / 86400)}d ago`;
}

function PeerSyncTile({ status }: { status: PeerSyncStatus }) {
  const healthy = !status.lastError;
  return (
    <Paper sx={{ p: 3, mb: 3 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, mb: 1 }}>
        {healthy ? (
          <SyncIcon sx={{ color: "success.main" }} />
        ) : (
          <SyncProblemIcon sx={{ color: "error.main" }} />
        )}
        <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
          Config sync from {status.primaryId}
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ ml: "auto" }}>
          {status.pullCount} pull{status.pullCount === 1 ? "" : "s"}
        </Typography>
      </Box>
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr 1fr", sm: "1fr 1fr 1fr" },
          gap: 1,
          mb: status.lastError ? 1 : 0,
        }}
      >
        <Box>
          <Typography variant="caption" color="text.secondary">
            Last attempt
          </Typography>
          <Typography variant="body2">{formatRelative(status.lastPullAt)}</Typography>
        </Box>
        <Box>
          <Typography variant="caption" color="text.secondary">
            Last success
          </Typography>
          <Typography variant="body2">{formatRelative(status.lastSuccessAt)}</Typography>
        </Box>
        <Box>
          <Typography variant="caption" color="text.secondary">
            Last applied
          </Typography>
          <Typography variant="body2">{formatRelative(status.lastApplyAt)}</Typography>
        </Box>
      </Box>
      {status.lastError && (
        <Alert severity="error" sx={{ mt: 1 }}>
          {status.lastError}
        </Alert>
      )}
    </Paper>
  );
}

/**
 * HAProxy/SSL/version — hz's own live status, demoted under the queue rather
 * than cut outright. Unlike the inventory counts this replaced, a stopped
 * HAProxy is a fault on THIS box today, not a count that never moves; it
 * stays small because it is not part of the five merged sources above (no
 * hook here answers "who does this affect" the way the queue's rows do).
 */
function GatewayStatusStrip() {
  const { data, isLoading, error } = useDashboard();

  if (isLoading || error || !data) {
    // Not part of the ranked queue's own unknown/empty/items contract — this
    // strip is a demoted extra, so its own failure is a quiet line, not a
    // banner competing with the queue above for attention.
    return (
      <Typography variant="caption" sx={{ color: "text.secondary" }}>
        {error ? "hz's own status could not be read." : "Reading hz's own status…"}
      </Typography>
    );
  }

  return (
    <Box>
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" },
          gap: 2,
          mb: 2,
        }}
      >
        <Paper sx={{ p: 2, display: "flex", alignItems: "center", gap: 2 }}>
          {data.haproxyRunning ? (
            <CheckCircleIcon sx={{ fontSize: 24, color: "success.main" }} />
          ) : (
            <CancelIcon sx={{ fontSize: 24, color: "error.main" }} />
          )}
          <Box>
            <Typography variant="subtitle2" sx={{ fontWeight: 600 }}>
              HAProxy
            </Typography>
            <Typography variant="body2" color="text.secondary">
              <StatusDot active={data.haproxyRunning} />
              {data.haproxyRunning ? "Running" : "Stopped"}
            </Typography>
          </Box>
        </Paper>

        <Paper sx={{ p: 2, display: "flex", alignItems: "center", gap: 2 }}>
          {data.sslEnabled ? (
            <CheckCircleIcon sx={{ fontSize: 24, color: "success.main" }} />
          ) : (
            <CancelIcon sx={{ fontSize: 24, color: "error.main" }} />
          )}
          <Box>
            <Typography variant="subtitle2" sx={{ fontWeight: 600 }}>
              SSL / HTTPS
            </Typography>
            <Typography variant="body2" color="text.secondary">
              <StatusDot active={data.sslEnabled} />
              {data.sslEnabled ? "Enabled" : "Disabled"}
            </Typography>
          </Box>
        </Paper>
      </Box>

      {data.peerSync && <PeerSyncTile status={data.peerSync} />}

      <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
        <InfoIcon fontSize="small" sx={{ color: "text.secondary" }} />
        <Typography variant="body2" color="text.secondary">
          Version: {data.version || "unknown"}
        </Typography>
      </Box>
    </Box>
  );
}

function OverviewPage() {
  const fleet = useAgentObserved();
  const cmPending = useCMRegistrations("pending");
  const dnsDrift = useDNSDriftStatus();
  const checks = useChecks();
  const pendingChanges = usePendingChanges();

  const result = buildOverview({
    // useAgentObserved answers the whole envelope (machines + hz's clock);
    // the queue only ranks the rows, so the envelope is unwrapped here rather
    // than teaching queue.ts hz's transport shape.
    fleet: answerFrom(
      { ...fleet, data: fleet.data?.machines },
      "the fleet",
    ),
    cmPending: answerFrom(cmPending, "pending registrations"),
    dnsDrift: answerFrom(dnsDrift, "DNS drift"),
    checks: answerFrom(checks, "health checks"),
    pendingChanges: answerFrom(pendingChanges, "pending changes"),
  });

  return (
    <Box>
      <Typography variant="h5" sx={{ mb: 3, fontWeight: 600 }}>
        Overview
      </Typography>

      <OverviewQueue result={result} />

      <Typography
        variant="overline"
        sx={{ display: "block", color: "text.secondary", mt: 4, mb: 1 }}
      >
        hz's own status
      </Typography>
      <GatewayStatusStrip />
    </Box>
  );
}

export const Route = createFileRoute("/")({
  component: OverviewPage,
});

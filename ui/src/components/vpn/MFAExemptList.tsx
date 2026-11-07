import { Box, Chip, Typography } from "@mui/material";
import type { MFAExemptResp } from "../../api/generated-types";

/** What each exemption means, in one line. Keys are config.MFAExempt*. */
export const EXEMPT_LABEL: Record<string, string> = {
  infra: "infra — a machine, vpn-only; no MFA in either scope",
  upstream: "upstream — a nested hz; reaches only hz's API",
  exception: "exception — time-limited",
  admin: "VPN admin — exempt while scope is admins-exempt",
  "session-forever": "a person's MFA session that never expires",
  session: "a person with a live MFA session",
};

/**
 * Every VPN client that skips MFA right now, and why — read from the same
 * decision the jail uses (config.MFAExemption), so it lists exactly the
 * clients the jail spares. Live sessions are listed apart: that is how the
 * jail is passed, not a bypass.
 */
export function MFAExemptList({ exempt, inactive }: { exempt: MFAExemptResp[]; inactive: string[] }) {
  const standing = exempt.filter((e) => e.why !== "session");
  const sessions = exempt.filter((e) => e.why === "session");
  return (
    <Box data-mfa-exempt sx={{ mt: 2 }}>
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        VPN clients without MFA
      </Typography>
      {standing.length === 0 ? (
        <Typography variant="caption" color="text.secondary">
          None: every client must pass MFA.
        </Typography>
      ) : (
        standing.map((e) => (
          <Box key={e.name} data-exempt-row={e.name} sx={{ mb: 1 }}>
            <Typography variant="body2">
              <strong>{e.name}</strong>{" "}
              <Chip size="small" label={e.why} color={e.why === "session-forever" ? "warning" : "default"} />{" "}
              {EXEMPT_LABEL[e.why] ?? e.why}
            </Typography>
            {e.reason && (
              <Typography variant="caption" color="text.secondary">
                {e.reason}
                {e.setBy ? ` · set by ${e.setBy}` : ""}
                {e.setAt ? ` · ${new Date(e.setAt).toLocaleString()}` : ""}
              </Typography>
            )}
          </Box>
        ))
      )}
      {inactive.map((n) => (
        <Typography key={n} data-infra-inactive={n} variant="body2" color="warning.main">
          <strong>{n}</strong> is tagged infra but is not vpn-only, so the tag is not honoured and it is
          jailed. Set its profile back to vpn-only, or tag it user.
        </Typography>
      ))}
      {sessions.length > 0 && (
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1 }}>
          Live MFA sessions: {sessions.map((e) => e.name).join(", ")}
        </Typography>
      )}
    </Box>
  );
}

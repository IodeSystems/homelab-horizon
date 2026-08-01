import { createFileRoute, Link, useParams } from "@tanstack/react-router";
import { Alert, Box, Breadcrumbs, CircularProgress, Typography } from "@mui/material";
import { ZoneRecordsTable } from "../components/ZoneRecords";
import SyncButton from "../components/SyncButton";
import { useZones } from "../api/hooks";

export const Route = createFileRoute("/dns/$zone")({
  component: DNSZonePage,
});

// One zone per page, deliberately: records are edited against the live provider
// set, and having two zones' records on screen at once makes it far too easy to
// act on the wrong one.
function DNSZonePage() {
  const { zone } = useParams({ from: "/dns/$zone" });
  const { data: zones, isLoading: zonesLoading } = useZones();
  const zoneCfg = zones?.find((z) => z.name === zone);

  // Reading records means calling the provider, so a zone without one can never
  // load. Say that outright instead of firing a request that 400s and leaves a
  // spinner up through react-query's retries.
  const noProvider = !zonesLoading && zoneCfg && !zoneCfg.providerType;
  const unknownZone = !zonesLoading && zones && !zoneCfg;

  return (
    <Box>
      <Breadcrumbs sx={{ mb: 1 }}>
        <Link to="/dns" style={{ color: "inherit" }}>
          DNS
        </Link>
        <Typography color="text.primary">{zone}</Typography>
      </Breadcrumbs>

      <Box sx={{ display: "flex", alignItems: "center", mb: 1 }}>
        <Typography variant="h4" sx={{ flexGrow: 1 }}>
          {zone}
        </Typography>
        <SyncButton />
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Every record live at the provider, labelled by who owns it. hz only
        rewrites what it owns.
      </Typography>

      {zonesLoading ? (
        <Box sx={{ display: "flex", justifyContent: "center", py: 4 }}>
          <CircularProgress size={24} />
        </Box>
      ) : unknownZone ? (
        <Alert severity="error">
          No zone named <strong>{zone}</strong> is configured.
        </Alert>
      ) : noProvider ? (
        <Alert severity="info">
          This zone has no DNS provider configured, so hz cannot read or publish
          its records. Add one in Settings.
        </Alert>
      ) : (
        <ZoneRecordsTable zoneName={zone} />
      )}
    </Box>
  );
}

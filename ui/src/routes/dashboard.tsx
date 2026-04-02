import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  CircularProgress,
  Paper,
  Typography,
} from "@mui/material";
import DnsIcon from "@mui/icons-material/Dns";
import LanguageIcon from "@mui/icons-material/Language";
import PublicIcon from "@mui/icons-material/Public";
import PeopleIcon from "@mui/icons-material/People";
import MonitorHeartIcon from "@mui/icons-material/MonitorHeart";
import CheckCircleIcon from "@mui/icons-material/CheckCircle";
import CancelIcon from "@mui/icons-material/Cancel";
import InfoIcon from "@mui/icons-material/Info";
import { useDashboard } from "../api/hooks";

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

function StatCard({
  icon,
  value,
  label,
}: {
  icon: React.ReactNode;
  value: number | string;
  label: string;
}) {
  return (
    <Paper
      sx={{
        p: 3,
        display: "flex",
        alignItems: "center",
        gap: 2,
      }}
    >
      <Box sx={{ color: "primary.main", display: "flex" }}>{icon}</Box>
      <Box>
        <Typography variant="h4" sx={{ fontWeight: 700, lineHeight: 1 }}>
          {value}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          {label}
        </Typography>
      </Box>
    </Paper>
  );
}

function DashboardPage() {
  const { data, isLoading, error } = useDashboard();

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">Failed to load dashboard: {error.message}</Alert>;
  }

  if (!data) return null;

  return (
    <Box>
      <Typography variant="h5" sx={{ mb: 3, fontWeight: 600 }}>
        Dashboard
      </Typography>

      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr", lg: "repeat(4, 1fr)" },
          gap: 2,
          mb: 3,
        }}
      >
        <StatCard
          icon={<DnsIcon sx={{ fontSize: 36 }} />}
          value={data.serviceCount}
          label="Services"
        />
        <StatCard
          icon={<LanguageIcon sx={{ fontSize: 36 }} />}
          value={data.domainCount}
          label="Domains"
        />
        <StatCard
          icon={<PublicIcon sx={{ fontSize: 36 }} />}
          value={data.zoneCount}
          label="Zones"
        />
        <StatCard
          icon={<PeopleIcon sx={{ fontSize: 36 }} />}
          value={data.peerCount}
          label="VPN Peers"
        />
      </Box>

      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" },
          gap: 2,
          mb: 3,
        }}
      >
        <Paper sx={{ p: 3, display: "flex", alignItems: "center", gap: 2 }}>
          {data.haproxyRunning ? (
            <CheckCircleIcon sx={{ fontSize: 28, color: "success.main" }} />
          ) : (
            <CancelIcon sx={{ fontSize: 28, color: "error.main" }} />
          )}
          <Box>
            <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
              HAProxy
            </Typography>
            <Typography variant="body2" color="text.secondary">
              <StatusDot active={data.haproxyRunning} />
              {data.haproxyRunning ? "Running" : "Stopped"}
            </Typography>
          </Box>
        </Paper>

        <Paper sx={{ p: 3, display: "flex", alignItems: "center", gap: 2 }}>
          {data.sslEnabled ? (
            <CheckCircleIcon sx={{ fontSize: 28, color: "success.main" }} />
          ) : (
            <CancelIcon sx={{ fontSize: 28, color: "error.main" }} />
          )}
          <Box>
            <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
              SSL / HTTPS
            </Typography>
            <Typography variant="body2" color="text.secondary">
              <StatusDot active={data.sslEnabled} />
              {data.sslEnabled ? "Enabled" : "Disabled"}
            </Typography>
          </Box>
        </Paper>
      </Box>

      {data.checksTotal > 0 && (
        <Paper sx={{ p: 3, mb: 3 }}>
          <Box sx={{ display: "flex", alignItems: "center", gap: 1, mb: 1.5 }}>
            <MonitorHeartIcon sx={{ color: "primary.main" }} />
            <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
              Health Checks
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ ml: "auto" }}>
              {data.checksHealthy} healthy / {data.checksFailed} failed / {data.checksTotal} total
            </Typography>
          </Box>
          <Box
            sx={{
              display: "flex",
              height: 12,
              borderRadius: 1,
              overflow: "hidden",
              bgcolor: "action.hover",
            }}
          >
            {data.checksHealthy > 0 && (
              <Box
                sx={{
                  width: `${(data.checksHealthy / data.checksTotal) * 100}%`,
                  bgcolor: "success.main",
                  transition: "width 0.3s",
                }}
              />
            )}
            {data.checksFailed > 0 && (
              <Box
                sx={{
                  width: `${(data.checksFailed / data.checksTotal) * 100}%`,
                  bgcolor: "error.main",
                  transition: "width 0.3s",
                }}
              />
            )}
          </Box>
        </Paper>
      )}

      <Paper sx={{ p: 3 }}>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <InfoIcon sx={{ color: "text.secondary" }} />
          <Typography variant="body2" color="text.secondary">
            Version: {data.version || "unknown"}
          </Typography>
        </Box>
      </Paper>
    </Box>
  );
}

export const Route = createFileRoute("/dashboard")({
  component: DashboardPage,
});

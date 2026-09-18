import { useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import AltRouteIcon from "@mui/icons-material/AltRouteOutlined";
import { useCMResolve } from "../api/hooks";
import type { CMConfigResp } from "../api/generated-types";
import {
  AddressPicker,
  BindingChip,
  CopyBox,
  addressComplete,
  addressLabel,
  emptyCMAddress,
  rangeLabel,
  whenLabel,
  type CMAddress,
} from "./CMConfigs";

// Resolution inspection: what a box at a given version would get at an address.
//
// Resolution is computed and never stored, so this is the only place the
// question can be asked before a box answers it by restarting. Showing the
// winner alone would make it a lookup; the point is the shadowed candidates —
// overlapping ranges are the normal shape of supersession, and silent
// resolution is acceptable only because you can ask what it resolved to.
//
// As everywhere in this surface: key names, bindings and ranges. No values.

function ConfigCard({
  config,
  version,
  winner,
}: {
  config: CMConfigResp;
  version: string;
  winner: boolean;
}) {
  const values = config.values ?? [];
  return (
    <Box
      sx={{
        p: 1.5,
        border: 1,
        borderRadius: 1,
        borderColor: winner ? "success.main" : "divider",
        bgcolor: winner ? "action.hover" : "transparent",
        opacity: winner ? 1 : 0.85,
      }}
    >
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
        <Chip
          size="small"
          color={winner ? "success" : "default"}
          label={winner ? "resolves" : "shadowed"}
          sx={{ height: 20, fontSize: "0.7rem" }}
        />
        <Chip
          size="small"
          label={`seq ${config.sequence}`}
          variant="outlined"
          sx={{ height: 20, fontSize: "0.7rem", fontFamily: "monospace" }}
        />
        <Typography variant="body2" sx={{ fontFamily: "monospace", fontWeight: 600 }}>
          {rangeLabel(config)}
        </Typography>
        {!config.maxVer && (
          <Tooltip title="Open-ended: it covers every release above its minimum until a higher sequence supersedes it.">
            <Chip
              size="small"
              variant="outlined"
              label="open-ended"
              sx={{ height: 20, fontSize: "0.7rem" }}
            />
          </Tooltip>
        )}
      </Box>

      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.5 }}>
        contains {version} · blessed by {config.createdBy || "unknown"} on{" "}
        {whenLabel(config.createdAt)}
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ display: "block", fontFamily: "monospace", wordBreak: "break-all" }}
      >
        {config.id}
      </Typography>

      {/* Only the winner comes back with values. The shadowed candidates are
          headers on purpose: the overlap must be inspectable, not every loser's
          ciphertext carried along with it. */}
      {winner && values.length > 0 && (
        <Box sx={{ mt: 1, display: "flex", gap: 0.75, flexWrap: "wrap" }}>
          {values.map((v) => (
            <Box
              key={v.key}
              sx={{ display: "flex", alignItems: "center", gap: 0.5, flexWrap: "wrap" }}
            >
              <Typography
                variant="caption"
                sx={{
                  fontFamily: "monospace",
                  fontWeight: 600,
                  textDecoration: !v.sealed && v.tombstonedAt ? "line-through" : "none",
                }}
              >
                {v.key}
              </Typography>
              <BindingChip binding={v.binding} />
              {!v.sealed && v.tombstonedAt && (
                <Chip
                  size="small"
                  color="error"
                  label="destroyed"
                  sx={{ height: 18, fontSize: "0.65rem" }}
                />
              )}
            </Box>
          ))}
        </Box>
      )}
      {winner && values.length === 0 && (
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.5, fontStyle: "italic" }}>
          It carries no keys. A box would start with nothing from the config
          manager.
        </Typography>
      )}
    </Box>
  );
}

export function CMResolve({
  initialAddress,
  initialVersion,
}: {
  initialAddress?: CMAddress;
  initialVersion?: string;
}) {
  const [address, setAddress] = useState<CMAddress>(initialAddress ?? emptyCMAddress);
  const [version, setVersion] = useState(initialVersion ?? "");

  const ready = addressComplete(address) && version.trim() !== "";
  const { data, isLoading, error } = useCMResolve(
    address.env.trim(),
    address.app.trim(),
    address.role.trim(),
    version.trim(),
  );

  const shadowed = data?.shadowed ?? [];

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      <CardContent>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <AltRouteIcon fontSize="small" color="action" />
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            What would a box get
          </Typography>
        </Box>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: 680 }}>
          Resolution is computed at every start and stored nowhere, so this is
          where you ask it in advance. It answers with the config that wins and
          the ones it passed over.
        </Typography>

        <Box sx={{ mt: 2 }}>
          <AddressPicker value={address} onChange={setAddress} />
        </Box>
        <TextField
          label="Version"
          size="small"
          placeholder="1.2.0"
          helperText="The release the box is running. Config follows the app backwards, so a rollback picks up the config whose range still covers it."
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          sx={{ mt: 2, maxWidth: 360 }}
        />

        {!ready ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            Give an address and a version.
          </Typography>
        ) : error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            Could not resolve: {error.message}
          </Alert>
        ) : isLoading ? (
          <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
            <CircularProgress size={20} />
          </Box>
        ) : data?.error ? (
          // Zero matches is a named failure, not an empty state. A release
          // above every maxVer at an address resolves to nothing, and the box
          // fails at its next restart — which for an unattended service can be
          // years after the config that would have covered it was closed.
          <Alert severity="error" sx={{ mt: 2 }}>
            <AlertTitle>Nothing covers {version.trim()}</AlertTitle>
            <Typography variant="body2">{data.error}</Typography>
            <Typography variant="body2" sx={{ mt: 1 }}>
              A box at this version gets no config and fails at its next start —
              not now, but whenever it next restarts, which for an unattended
              service can be a long way from here. Bless a config whose range
              covers {version.trim()} before anything runs it.
            </Typography>
          </Alert>
        ) : data?.winner ? (
          <Box sx={{ mt: 2 }}>
            <Typography variant="body2" sx={{ fontWeight: 600 }}>
              A box running {version.trim()} at{" "}
              <Box component="span" sx={{ fontFamily: "monospace" }}>
                {addressLabel(address)}
              </Box>{" "}
              gets sequence {data.winner.sequence}.
            </Typography>
            {/* Why it won, stated rather than implied: highest sequence among
                the candidates whose range contains the version. Sequence is
                assigned at blessing and never recomputed, so "last" cannot
                change under a config nobody touched. */}
            <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
              {shadowed.length === 0
                ? `It is the only config whose range contains ${version.trim()}.`
                : `It is the highest sequence among the ${shadowed.length + 1} configs whose ranges contain ${version.trim()}. Sequence is assigned at blessing and never recomputed, so this cannot change without a new config.`}
            </Typography>

            <Stack spacing={1}>
              <ConfigCard config={data.winner} version={version.trim()} winner />
              {shadowed.length > 0 && (
                <Box sx={{ mt: 1 }}>
                  <Typography variant="body2" sx={{ fontWeight: 600 }}>
                    Shadowed: {shadowed.length} config{shadowed.length === 1 ? "" : "s"} also
                    contain {version.trim()}
                  </Typography>
                  <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
                    Overlap is normal — it is what lets an older release keep its
                    own config. These lose on sequence, not on range.
                  </Typography>
                  <Stack spacing={1}>
                    {shadowed.map((c) => (
                      <ConfigCard key={c.id} config={c} version={version.trim()} winner={false} />
                    ))}
                  </Stack>
                </Box>
              )}
            </Stack>

            <Box sx={{ mt: 2 }}>
              <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 0.5 }}>
                The values themselves are sealed and hz has no key. To read what
                the box would actually get:
              </Typography>
              <CopyBox text={`hz cm show ${data.winner.id}`} />
            </Box>
          </Box>
        ) : null}
      </CardContent>
    </Card>
  );
}

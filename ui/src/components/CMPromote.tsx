import { useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import UpgradeIcon from "@mui/icons-material/UpgradeOutlined";
import { useCMConfigs, useCMPromotionGate, useCMRegistrations } from "../api/hooks";
import {
  AddressPicker,
  CopyBox,
  addressComplete,
  addressLabel,
  emptyCMAddress,
  rangeLabel,
  type CMAddress,
} from "./CMConfigs";

// The promotion gate.
//
// hz can compute this without reading a single value: it asks whether a key is
// BOUND in the target, never what it holds. That is why the gate survives hz
// holding no keys, and it is why promotion is a gate rather than a copy.
//
// This component cannot perform a promotion, and that is not a missing feature.
// Re-sealing needs the source key to open the value and the target key to seal
// it, and neither is in a browser — nor should be, since the browser is served
// by the party the design distrusts. So the gate runs here and the promotion
// runs in the CLI.

export function CMPromote({
  initialAddress,
  initialConfigID,
}: {
  initialAddress?: CMAddress;
  initialConfigID?: string;
}) {
  const [address, setAddress] = useState<CMAddress>(initialAddress ?? emptyCMAddress);
  const [configID, setConfigID] = useState(initialConfigID ?? "");
  const [targetEnv, setTargetEnv] = useState("");

  const configs = useCMConfigs(
    address.env.trim(),
    address.app.trim(),
    address.role.trim(),
  );
  const rows = [...(configs.data ?? [])].sort((a, b) => b.sequence - a.sequence);

  // Environments hz has seen a machine register in. Not the set of environments
  // that exist — a target can be blessed before anything runs there — so these
  // are suggestions, not a list to choose from.
  const pending = useCMRegistrations("pending");
  const approved = useCMRegistrations("approved");
  const envs = [
    ...new Set(
      [...(approved.data ?? []), ...(pending.data ?? [])].map((r) => r.environment),
    ),
  ]
    .filter((e) => e && e !== address.env.trim())
    .sort();

  const gate = useCMPromotionGate(configID.trim() || null, targetEnv.trim());
  const blocked = gate.data?.blocked ?? [];
  const promotes = gate.data?.promotes ?? [];
  const ready = configID.trim() !== "" && targetEnv.trim() !== "";

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      <CardContent>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <UpgradeIcon fontSize="small" color="action" />
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Promotion gate
          </Typography>
        </Box>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: 680 }}>
          Promotion is a diff and a gate, not a copy. Invariant keys carry to the
          target by re-sealing under its key; environment-bound keys never carry
          and must already be bound there. This checks both before anyone runs it.
        </Typography>

        <Box sx={{ mt: 2 }}>
          <AddressPicker
            value={address}
            onChange={(a) => {
              setAddress(a);
              setConfigID("");
            }}
          />
        </Box>

        <Stack direction={{ xs: "column", sm: "row" }} spacing={2} sx={{ mt: 2 }}>
          <FormControl size="small" sx={{ minWidth: 280, flex: 1 }} disabled={!addressComplete(address)}>
            <InputLabel id="cm-promote-config">Config to promote</InputLabel>
            <Select
              labelId="cm-promote-config"
              label="Config to promote"
              value={rows.some((c) => c.id === configID) ? configID : ""}
              onChange={(e) => setConfigID(e.target.value)}
            >
              {rows.map((c) => (
                <MenuItem key={c.id} value={c.id}>
                  seq {c.sequence} · {rangeLabel(c)}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          <TextField
            label="Config id"
            size="small"
            sx={{ flex: 1 }}
            helperText="Picked above, or pasted from hz cm ls."
            value={configID}
            onChange={(e) => setConfigID(e.target.value)}
          />
        </Stack>

        <Box sx={{ mt: 2 }}>
          <TextField
            label="Target environment"
            size="small"
            sx={{ maxWidth: 360 }}
            helperText="Where it would land. The app and role stay the same."
            value={targetEnv}
            onChange={(e) => setTargetEnv(e.target.value)}
          />
          {envs.length > 0 && (
            <Box sx={{ display: "flex", alignItems: "center", gap: 0.75, flexWrap: "wrap", mt: 1 }}>
              <Typography variant="caption" color="text.secondary">
                Seen:
              </Typography>
              {envs.map((e) => (
                <Chip
                  key={e}
                  size="small"
                  label={e}
                  variant={e === targetEnv.trim() ? "filled" : "outlined"}
                  color={e === targetEnv.trim() ? "primary" : "default"}
                  onClick={() => setTargetEnv(e)}
                  sx={{ height: 22, fontSize: "0.7rem", fontFamily: "monospace" }}
                />
              ))}
            </Box>
          )}
        </Box>

        {!ready ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            Pick a config and a target environment.
          </Typography>
        ) : gate.error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            Could not run the gate: {gate.error.message}
          </Alert>
        ) : gate.isLoading ? (
          <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
            <CircularProgress size={20} />
          </Box>
        ) : gate.data ? (
          <Box sx={{ mt: 2 }}>
            {/* Blocked is the whole point. An environment-bound key the target
                has no value for is not a warning to scroll past: the promotion
                does not happen, and the reason it does not happen is here. */}
            {blocked.length > 0 && (
              <Alert severity="error" sx={{ mb: 2 }}>
                <AlertTitle>
                  {blocked.length} key{blocked.length === 1 ? " is" : "s are"} not bound
                  in {gate.data.targetEnv}
                </AlertTitle>
                <Typography variant="body2">
                  These are environment-bound. They never promote — a database
                  URL or a production password is born in the environment that
                  uses it — so {gate.data.targetEnv} must hold its own value for
                  each before this config can land.
                </Typography>
                <Box sx={{ display: "flex", gap: 0.75, flexWrap: "wrap", mt: 1 }}>
                  {blocked.map((k) => (
                    <Chip
                      key={k}
                      size="small"
                      color="error"
                      label={k}
                      sx={{ height: 22, fontSize: "0.72rem", fontFamily: "monospace" }}
                    />
                  ))}
                </Box>
                <Typography variant="body2" sx={{ mt: 1.5 }}>
                  Bind them where the target's key is, then run the gate again:
                </Typography>
                <Box sx={{ mt: 0.5 }}>
                  <CopyBox
                    text={`hz cm push ${gate.data.targetEnv}/${address.app.trim() || "<app>"}/${address.role.trim() || "<role>"}`}
                  />
                </Box>
              </Alert>
            )}

            <Typography variant="body2" sx={{ fontWeight: 600 }}>
              {promotes.length} invariant key{promotes.length === 1 ? "" : "s"} would carry
            </Typography>
            <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
              Opened under {address.env.trim() || "the source"}'s key and re-sealed
              under {gate.data.targetEnv}'s, each recording this config as its
              source so the target can answer where the value came from.
            </Typography>
            {promotes.length === 0 ? (
              <Typography variant="body2" color="text.secondary" sx={{ fontStyle: "italic" }}>
                None. Every key on this config is environment-bound, so there is
                nothing for a promotion to carry.
              </Typography>
            ) : (
              <Box sx={{ display: "flex", gap: 0.75, flexWrap: "wrap" }}>
                {promotes.map((k) => (
                  <Chip
                    key={k}
                    size="small"
                    variant="outlined"
                    label={k}
                    sx={{ height: 22, fontSize: "0.72rem", fontFamily: "monospace" }}
                  />
                ))}
              </Box>
            )}

            {gate.data.ok && (
              <Box sx={{ mt: 2 }}>
                <Alert severity="success" sx={{ mb: 1 }}>
                  Nothing is blocked. {gate.data.targetEnv} has a value bound for
                  every environment-bound key on this config.
                </Alert>
                {/* The UI cannot do this, and says so rather than offering a
                    button that would have to ask for keys. */}
                <Typography variant="body2" sx={{ fontWeight: 600 }}>
                  Run the promotion where the keys are
                </Typography>
                <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 0.5 }}>
                  Each value is opened under the source key and re-sealed under
                  the target's, so a promotion needs both. This page holds
                  neither and never will: hz serves it, and hz is the party that
                  must not see plaintext. The CLI does the decrypt/re-seal cycle
                  locally and hz sees one ciphertext read and another written.
                </Typography>
                <CopyBox
                  text={`hz cm promote ${gate.data.sourceConfigId} --to=${gate.data.targetEnv}`}
                />
                <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.5 }}>
                  Add <code>--dry-run</code> to see what it would write first.
                </Typography>
              </Box>
            )}

            <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 2 }}>
              Gate run for{" "}
              <Box component="span" sx={{ fontFamily: "monospace" }}>
                {gate.data.sourceConfigId}
              </Box>
              {addressComplete(address) ? ` at ${addressLabel(address)}` : ""} → {gate.data.targetEnv}.
              hz checked only which keys are bound, never what any of them holds.
            </Typography>
          </Box>
        ) : null}
      </CardContent>
    </Card>
  );
}

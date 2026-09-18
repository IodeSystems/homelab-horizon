import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { Box, Tab, Tabs, Typography } from "@mui/material";
import { CMApprovals } from "../components/CMApprovals";
import { CMMachines } from "../components/CMMachines";
import { CMConfigs } from "../components/CMConfigs";
import { CMResolve } from "../components/CMResolve";
import { CMPromote } from "../components/CMPromote";

// The config manager's admin surface.
//
// Everything here is METADATA. hz holds no key, so there is no config value to
// show and none of these tabs can show one: key names, bindings, ranges,
// sequences, lineage and state. Decrypting anything is `hz cm show`, in the
// CLI, which is where the keys live.
//
// Nothing on this page accepts a key, and nothing on it does crypto. The
// approval ceremony is in the `hz` CLI on purpose — a browser served by hz
// cannot defend against hz, because a compromised hz would serve a bundle that
// exfiltrates a pasted key before using it, and every mitigation this page
// could implement would run in that same attacker-authored code. If a future
// change adds a key field here, that reasoning is what it is undoing.
function ConfigPage() {
  const [tab, setTab] = useState(0);

  return (
    <Box>
      <Typography variant="h4" sx={{ mb: 1 }}>
        Config
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Registrations, blessed configs and promotion. Values are sealed and hz
        cannot read them — use <code>hz cm</code> to decrypt or to approve.
      </Typography>

      <Tabs
        value={tab}
        onChange={(_, v) => setTab(v)}
        variant="scrollable"
        scrollButtons="auto"
        sx={{ mb: 3, borderBottom: 1, borderColor: "divider" }}
      >
        <Tab label="Approvals" />
        <Tab label="Machines" />
        <Tab label="Configs" />
        <Tab label="Resolve" />
        <Tab label="Promote" />
      </Tabs>

      {tab === 0 && <CMApprovals />}
      {tab === 1 && <CMMachines />}
      {tab === 2 && <CMConfigs />}
      {tab === 3 && <CMResolve />}
      {tab === 4 && <CMPromote />}
    </Box>
  );
}

export const Route = createFileRoute("/config")({ component: ConfigPage });

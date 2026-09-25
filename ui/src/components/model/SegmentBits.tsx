/**
 * The segment table, rendered identically at the estate and inside a project.
 *
 * `Network` is one of the five scopable entries, which means the SAME surface
 * has to exist at level 0 and at every depth below it (`SCOPABLE_NAV`). The two
 * screens differ in exactly two things — which rows are selected, and whether
 * the owner column asks *where in this subtree* or *which project at all* — so
 * the table itself is one component and neither screen has its own copy to
 * drift.
 *
 * # THE OWNER IS WHAT MAKES A CROSSING READABLE
 *
 * A member is a MACHINE, and a machine carries no project — so a segment owned
 * by one project can carry another project's box, and that is a real and
 * legitimate state rather than a misconfiguration. Each member's name links to
 * `/machines/$machine`, the one page a box has; nothing here links to a
 * per-project machine URL, because there is no such thing.
 *
 * # UNADDRESSED IS NOT EMPTY
 *
 * `SegmentResp.unaddressed` is every machine that NAMES this segment and has no
 * member entry — the state `hz machine add --segment` leaves. It is not an error
 * and it is not a member, and a screen that counted only members would render
 * the segment as smaller than it is. It gets its own column, always.
 */
import { Link } from "@tanstack/react-router";
import {
  Box,
  Chip,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import type { SegmentResp } from "../../api/generated-types";
import type { ProjectIndex } from "./projectRoutes.ts";
import { LocationCell } from "./ProjectBits";

export function SegmentsTable({
  segments,
  index,
  ownerLabel,
}: {
  segments: SegmentResp[];
  index: ProjectIndex;
  /** "Location" inside a subtree, "Project" on the estate-wide list. */
  ownerLabel: string;
}) {
  return (
    <TableContainer component={Paper}>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Segment</TableCell>
            <TableCell>{ownerLabel}</TableCell>
            <TableCell>Range</TableCell>
            <TableCell>Members</TableCell>
            <TableCell>Named it, not addressed</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {segments.map((s) => (
            <TableRow key={s.name} hover>
              <TableCell sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                {s.name}
                {s.note ? (
                  <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
                    {s.note}
                  </Typography>
                ) : null}
              </TableCell>
              <TableCell>
                <LocationCell index={index} project={s.project} />
              </TableCell>
              <TableCell sx={{ fontFamily: "monospace" }}>
                {s.cidr}
                <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
                  on {s.interface}
                </Typography>
              </TableCell>
              <TableCell>
                {(s.members ?? []).length === 0 ? (
                  <Typography variant="caption" sx={{ color: "text.secondary" }}>
                    no machine is addressed on this network yet
                  </Typography>
                ) : (
                  <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                    {(s.members ?? []).map((m) => (
                      <Link key={m.machine} to="/machines/$machine" params={{ machine: m.machine }}>
                        <Chip
                          size="small"
                          variant={m.hub ? "filled" : "outlined"}
                          label={`${m.machine} ${m.address}${m.hub ? " · hub" : ""}`}
                          sx={{ fontFamily: "monospace", cursor: "pointer" }}
                        />
                      </Link>
                    ))}
                  </Box>
                )}
              </TableCell>
              <TableCell>
                {(s.unaddressed ?? []).length === 0 ? (
                  <Typography variant="caption" sx={{ color: "text.secondary" }}>
                    none — every machine naming this segment has an address on it
                  </Typography>
                ) : (
                  <>
                    <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                      {(s.unaddressed ?? []).map((m) => (
                        <Link key={m} to="/machines/$machine" params={{ machine: m }}>
                          <Chip
                            size="small"
                            color="warning"
                            variant="outlined"
                            label={m}
                            sx={{ fontFamily: "monospace", cursor: "pointer" }}
                          />
                        </Link>
                      ))}
                    </Box>
                    <Typography variant="caption" sx={{ color: "text.secondary" }}>
                      these machines name this segment and have no address on it — half declared, not
                      absent
                    </Typography>
                  </>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

/**
 * Why the VPN clients are not on a Network screen, at either level.
 *
 * Rendered on both, because the reason is the same one and it is about the
 * record rather than about the scope: a client is not a machine, so nothing
 * links it to a segment. It is a sentence on the screen the operator is looking
 * for it on — not a greyed nav entry, which is a door to a room that is not
 * there.
 */
export function ClientsNotHere() {
  return (
    <Paper variant="outlined" sx={{ p: 2, mt: 2, bgcolor: "transparent" }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
        VPN clients are not on this screen
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        A segment&apos;s members are machines. A VPN client is{" "}
        {"`{Name, PublicKey, AllowedIPs}`"} and is not a machine record, so hz cannot say which
        segment — or which project — a client belongs to. Every client is on{" "}
        <Link to="/vpn">VPN Clients</Link>, gateway-wide, until the record carries the link.
      </Typography>
    </Paper>
  );
}

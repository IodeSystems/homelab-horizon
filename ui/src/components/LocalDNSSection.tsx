import { useState } from "react";
import {
  Box,
  Button,
  Alert,
  Chip,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  FormControlLabel,
  Checkbox,
  IconButton,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  Typography,
  Tooltip,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import DeleteIcon from "@mui/icons-material/Delete";
import EditIcon from "@mui/icons-material/Edit";
import {
  useLocalDNS,
  useSetLocalDNSRecord,
  useDeleteLocalDNSRecord,
  type LocalDNSRecord,
} from "../api/hooks";

// Names hz answers for on the inside — the split-horizon half of DNS.
//
// Zones above are what hz publishes to the world. This is what it tells
// clients on the LAN and the VPN, which is a different question and was
// previously unanswerable: a machine with no public presence had no name here
// at all, and a public name could not be pointed at a LAN address for people
// inside.
export default function LocalDNSSection() {
  const { data, isLoading, error } = useLocalDNS();
  const remove = useDeleteLocalDNSRecord();
  const [editing, setEditing] = useState<LocalDNSRecord | null>(null);
  const [addOpen, setAddOpen] = useState(false);

  if (isLoading) return null;
  if (error) {
    return (
      <Alert severity="error" sx={{ mt: 4 }}>
        Could not load local records: {error.message}
      </Alert>
    );
  }

  const records = data?.records ?? [];
  const derived = data?.derived ?? [];

  return (
    <Box sx={{ mt: 5 }}>
      <Box sx={{ display: "flex", alignItems: "center", mb: 1 }}>
        <Typography variant="h5" sx={{ flexGrow: 1 }}>
          Local records
        </Typography>
        <Button
          variant="outlined"
          size="small"
          startIcon={<AddIcon />}
          onClick={() => setAddOpen(true)}
        >
          Add record
        </Button>
      </Box>

      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Answers hz gives clients on the LAN and the VPN, whether or not the name
        exists publicly. Use them to name a machine that has no public record,
        or to point a public name at a LAN address for people inside.
      </Typography>

      {data && !data.enabled && (
        <Alert severity="info" sx={{ mb: 2 }}>
          dnsmasq is off, so hz is not answering DNS for anyone yet. Records are
          saved and served as soon as you enable it.
        </Alert>
      )}

      {records.length === 0 && (
        <Alert severity="info" sx={{ mb: 2 }}>
          No local records. A machine only findable over mDNS — a Mac finds it,
          a phone does not — is the usual reason to add one.
        </Alert>
      )}

      {remove.error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {remove.error.message}
        </Alert>
      )}

      {records.length > 0 && (
        <TableContainer component={Paper} variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Address</TableCell>
                <TableCell>Match</TableCell>
                <TableCell>Note</TableCell>
                <TableCell align="right" />
              </TableRow>
            </TableHead>
            <TableBody>
              {records.map((r) => (
                <TableRow key={r.name} hover>
                  <TableCell sx={{ fontFamily: "monospace" }}>
                    {r.name}
                    {r.shadowsDerived && (
                      <Tooltip
                        title={`A service would resolve this to ${r.shadowsDerived}. This record wins for clients inside — which is usually the point.`}
                      >
                        <Chip
                          size="small"
                          color="warning"
                          variant="outlined"
                          label="overrides service"
                          sx={{ ml: 1 }}
                        />
                      </Tooltip>
                    )}
                  </TableCell>
                  <TableCell sx={{ fontFamily: "monospace" }}>{r.ip}</TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      variant="outlined"
                      label={r.wildcard ? "name + subdomains" : "exact"}
                    />
                  </TableCell>
                  <TableCell>
                    <Typography variant="caption" color="text.secondary">
                      {r.comment}
                    </Typography>
                  </TableCell>
                  <TableCell align="right">
                    <IconButton size="small" onClick={() => setEditing(r)}>
                      <EditIcon fontSize="small" />
                    </IconButton>
                    <IconButton
                      size="small"
                      disabled={remove.isPending}
                      onClick={() => {
                        if (window.confirm(`Remove the local record for ${r.name}?`)) {
                          remove.mutate(r.name);
                        }
                      }}
                    >
                      <DeleteIcon fontSize="small" />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {derived.length > 0 && (
        <Box sx={{ mt: 3 }}>
          <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 1 }}>
            Also served, derived from services ({derived.length})
          </Typography>
          <Typography variant="caption" color="text.secondary">
            {/* Listed so this page shows everything the resolver answers,
                rather than only the half an operator typed. */}
            These come from service definitions and update themselves. Add a
            local record with the same name to override one.
          </Typography>
          <Box sx={{ display: "flex", flexWrap: "wrap", gap: 1, mt: 1 }}>
            {derived.map((r) => (
              <Chip
                key={r.name}
                size="small"
                variant="outlined"
                label={`${r.name} → ${r.ip}`}
                sx={{ fontFamily: "monospace" }}
              />
            ))}
          </Box>
        </Box>
      )}

      <RecordDialog
        open={addOpen || editing !== null}
        record={editing}
        onClose={() => {
          setAddOpen(false);
          setEditing(null);
        }}
      />
    </Box>
  );
}

function RecordDialog({
  open,
  record,
  onClose,
}: {
  open: boolean;
  record: LocalDNSRecord | null;
  onClose: () => void;
}) {
  const save = useSetLocalDNSRecord();
  const [name, setName] = useState("");
  const [ip, setIP] = useState("");
  const [wildcard, setWildcard] = useState(false);
  const [comment, setComment] = useState("");
  const [seeded, setSeeded] = useState<string | null>(null);

  // Seed once per opening rather than on every render, so typing is not
  // clobbered by the record prop.
  const key = record?.name ?? "__new__";
  if (open && seeded !== key) {
    setSeeded(key);
    setName(record?.name ?? "");
    setIP(record?.ip ?? "");
    setWildcard(record?.wildcard ?? false);
    setComment(record?.comment ?? "");
  }

  const close = () => {
    setSeeded(null);
    save.reset();
    onClose();
  };

  return (
    <Dialog open={open} onClose={close} maxWidth="xs" fullWidth>
      <DialogTitle>{record ? `Edit ${record.name}` : "Add local record"}</DialogTitle>
      <DialogContent>
        {save.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {save.error.message}
          </Alert>
        )}
        <TextField
          fullWidth
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={record !== null}
          helperText="A bare host like desktop, or a full name like wiki.example.com"
          sx={{ mt: 1, mb: 2 }}
        />
        <TextField
          fullWidth
          label="Address"
          value={ip}
          onChange={(e) => setIP(e.target.value)}
          helperText="An IP address — these records answer with an address, not another name"
          sx={{ mb: 1 }}
        />
        <FormControlLabel
          control={
            <Checkbox checked={wildcard} onChange={(e) => setWildcard(e.target.checked)} />
          }
          label="Also answer for subdomains"
        />
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 2 }}>
          Off means exact. On, a record for example.com also captures every host
          under it — occasionally what you want, rarely what you meant.
        </Typography>
        <TextField
          fullWidth
          label="Note (optional)"
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          helperText="Why this exists. Written into the generated file too."
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={close}>Cancel</Button>
        <Button
          variant="contained"
          disabled={save.isPending || !name.trim() || !ip.trim()}
          onClick={() =>
            save.mutate(
              { name: name.trim(), ip: ip.trim(), wildcard, comment: comment.trim() },
              { onSuccess: close },
            )
          }
        >
          {save.isPending ? "Saving..." : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

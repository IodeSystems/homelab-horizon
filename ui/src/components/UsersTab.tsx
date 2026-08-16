import { useState } from "react";
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  MenuItem,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
  CircularProgress,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import AccountSecurity from "./AccountSecurity";
import {
  useUsers,
  useCreateUser,
  useSetPassword,
  useSetUserDisabled,
  useAuthStatus,
  type User,
} from "../api/auth";

// Timestamps arrive as RFC3339 UTC; the viewer's zone is the browser's to
// know, not the gateway's.
function formatWhen(ts?: string): string {
  if (!ts) return "";
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString();
}

export default function UsersTab() {
  const { data, isLoading, error } = useUsers();
  const status = useAuthStatus();
  const setDisabled = useSetUserDisabled();

  const [addOpen, setAddOpen] = useState(false);
  const [pwUser, setPwUser] = useState<User | null>(null);

  if (isLoading) {
    return <CircularProgress />;
  }
  if (error) {
    return <Alert severity="error">Could not load users: {error.message}</Alert>;
  }

  const users = data?.users ?? [];
  const me = status.data?.username;

  return (
    <Box>
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          mb: 2,
        }}
      >
        <Box>
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Users
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Accounts that can administer this gateway.
          </Typography>
        </Box>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={() => setAddOpen(true)}
        >
          Add user
        </Button>
      </Box>

      {users.length === 0 && (
        <Alert severity="info" sx={{ mb: 2 }}>
          No accounts yet. Until one exists, the shared admin token is the only
          way in — and a shared token cannot tell you who did what.
        </Alert>
      )}

      {users.length > 0 && !data?.canDisableAdminToken && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          No account can log in yet: an invited user has to set a password
          before the admin token can be turned off.
        </Alert>
      )}

      <Card variant="outlined">
        <CardContent sx={{ p: 0, "&:last-child": { pb: 0 } }}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>User</TableCell>
                <TableCell>Role</TableCell>
                <TableCell>Last login</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {users.map((u) => (
                <TableRow key={u.id}>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontWeight: 500 }}>
                      {u.username}
                      {u.username === me && (
                        <Chip size="small" label="you" sx={{ ml: 1 }} />
                      )}
                      {u.disabled && (
                        <Chip
                          size="small"
                          color="default"
                          label="disabled"
                          sx={{ ml: 1 }}
                        />
                      )}
                    </Typography>
                    {u.email && (
                      <Typography variant="caption" color="text.secondary">
                        {u.email}
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      variant="outlined"
                      color={u.role === "admin" ? "primary" : "default"}
                      label={u.role}
                    />
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary">
                      {formatWhen(u.lastLogin) || "never"}
                    </Typography>
                  </TableCell>
                  <TableCell align="right">
                    <Button size="small" onClick={() => setPwUser(u)}>
                      Set password
                    </Button>
                    <Button
                      size="small"
                      color={u.disabled ? "primary" : "warning"}
                      disabled={setDisabled.isPending}
                      onClick={() =>
                        setDisabled.mutate({
                          userId: u.id,
                          disabled: !u.disabled,
                        })
                      }
                    >
                      {u.disabled ? "Enable" : "Disable"}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {setDisabled.error && (
        <Alert severity="error" sx={{ mt: 2 }}>
          {setDisabled.error.message}
        </Alert>
      )}

      {me && <AccountSecurity />}

      <AddUserDialog open={addOpen} onClose={() => setAddOpen(false)} />
      <SetPasswordDialog user={pwUser} onClose={() => setPwUser(null)} />
    </Box>
  );
}

function AddUserDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const create = useCreateUser();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("admin");
  const [password, setPassword] = useState("");

  const submit = () => {
    create.mutate(
      { username: username.trim(), email: email.trim(), role, password },
      {
        onSuccess: () => {
          setUsername("");
          setEmail("");
          setPassword("");
          onClose();
        },
      },
    );
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
      <DialogTitle>Add user</DialogTitle>
      <DialogContent>
        {create.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {create.error.message}
          </Alert>
        )}
        <TextField
          fullWidth
          label="Username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          sx={{ mt: 1, mb: 2 }}
        />
        <TextField
          fullWidth
          label="Email (optional)"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          sx={{ mb: 2 }}
        />
        <TextField
          select
          fullWidth
          label="Role"
          value={role}
          onChange={(e) => setRole(e.target.value)}
          sx={{ mb: 2 }}
        >
          <MenuItem value="admin">admin — full control</MenuItem>
          <MenuItem value="viewer">viewer — read only</MenuItem>
        </TextField>
        <TextField
          fullWidth
          type="password"
          label="Password (optional)"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          helperText="Leave blank to create the account without one; an admin sets it later. At least 12 characters."
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          disabled={create.isPending || !username.trim()}
          onClick={submit}
        >
          {create.isPending ? "Adding..." : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function SetPasswordDialog({
  user,
  onClose,
}: {
  user: User | null;
  onClose: () => void;
}) {
  const setPassword = useSetPassword();
  const status = useAuthStatus();
  const [password, setPw] = useState("");
  const [current, setCurrent] = useState("");

  // Changing your own password requires the current one; an admin resetting
  // somebody else's does not, because they may be resetting it precisely
  // because nobody knows it.
  const isSelf = user?.username === status.data?.username;

  const submit = () => {
    if (!user) return;
    setPassword.mutate(
      isSelf
        ? { currentPassword: current, password }
        : { userId: user.id, password },
      {
        onSuccess: () => {
          setPw("");
          setCurrent("");
          onClose();
        },
      },
    );
  };

  return (
    <Dialog open={user !== null} onClose={onClose} maxWidth="xs" fullWidth>
      <DialogTitle>Set password for {user?.username}</DialogTitle>
      <DialogContent>
        {setPassword.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {setPassword.error.message}
          </Alert>
        )}
        <Alert severity="info" sx={{ mb: 2 }}>
          Every other session for this account signs out.
        </Alert>
        {isSelf && (
          <TextField
            fullWidth
            type="password"
            label="Current password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            sx={{ mt: 1, mb: 2 }}
          />
        )}
        <TextField
          fullWidth
          type="password"
          label="New password"
          value={password}
          onChange={(e) => setPw(e.target.value)}
          helperText="At least 12 characters"
          sx={{ mt: isSelf ? 0 : 1 }}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          disabled={setPassword.isPending || password.length < 12}
          onClick={submit}
        >
          {setPassword.isPending ? "Saving..." : "Set password"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

/**
 * Putting a service into the project tree, drawn — J8, which had no UI at all.
 *
 * `assign.ts` decides; this draws. The whole point of the split is that the two
 * failures this surface is most likely to ship are invisible to tsc:
 *
 *   1. offering a free-text project box, which lets an operator compose a
 *      request `Save()` is obliged to reject (that is what `/config`'s
 *      AddressPicker does today, two nav entries below the screen that lists
 *      the declared ones);
 *   2. rendering "this service has no project" while the project list is still
 *      in flight, which is the same shipped bug as `795f5db` wearing a
 *      different hat.
 *
 * Both are decisions, both live next door, and `assign.selftest.ts` /
 * `assign.render.selftest.tsx` hold them.
 */
import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControl,
  IconButton,
  InputLabel,
  MenuItem,
  Select,
  Typography,
} from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import { ApiError } from "../../api/client";
import { useAssignService } from "../../api/hooks";
import { ToneChip } from "./ModelBits";
import {
  assignConsequence,
  checkAssignment,
  declaredRungs,
  placementLabel,
  readAssignRefusal,
  samePlacement,
  type AssignIndex,
  type Placement,
  type PlacementReading,
} from "./assign.ts";

// ---------------------------------------------------------------------------
// The cell
// ---------------------------------------------------------------------------

/**
 * Which rung a row sits on, and the control that moves it.
 *
 * A SECOND COLUMN, beside `LocationCell`'s project, rather than one compound
 * cell: project and rung are two facts, they fail independently, and a service
 * in `acme-co` on no rung reads identically to a service in `acme-co` on a rung
 * nobody declared when only the project is drawn. The flat lists carry the
 * Project column already; this is the half that was never rendered anywhere.
 *
 * The button is always rendered, labelled with a verb, and disabled with a
 * REASON rather than hidden while hz is still answering. A control that appears
 * and disappears teaches the operator that the screen is unpredictable; a
 * disabled one that says why teaches them to wait.
 */
export function RungCell({
  reading,
  onAssign,
}: {
  reading: PlacementReading;
  /** Omitted on screens where the row is not the thing being assigned. */
  onAssign?: () => void;
}) {
  return (
    <Box>
      <Box sx={{ display: "flex", alignItems: "center", gap: 0.75, flexWrap: "wrap" }}>
        <ToneChip
          label={reading.rungLabel}
          tone={reading.tone}
          hatched={reading.state === "loading" || reading.state === "unreadable"}
          dashed={reading.state === "unassigned" || reading.state === "project-only"}
        />
      </Box>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mt: 0.25 }}>
        {reading.meaning}
      </Typography>
      {onAssign ? (
        <>
          <Button
            size="small"
            variant="outlined"
            sx={{ mt: 0.75 }}
            disabled={!reading.actionable}
            onClick={(e) => {
              // The row itself expands on click. Without this the dialog opens
              // and the row toggles underneath it.
              e.stopPropagation();
              onAssign();
            }}
          >
            {reading.state === "unassigned" ? "Assign to a project" : "Change project or rung"}
          </Button>
          {!reading.actionable ? (
            <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
              {reading.state === "loading"
                ? "Waiting for hz to send the project tree — assigning needs the list of declared projects."
                : "hz could not be asked for the project tree, so there is nothing to choose from. Reload once it answers."}
            </Typography>
          ) : null}
        </>
      ) : null}
    </Box>
  );
}

// ---------------------------------------------------------------------------
// The dialog
// ---------------------------------------------------------------------------

/**
 * The option value standing for "no project" / "no rung".
 *
 * NOT "", because MUI reads an empty Select value as "nothing is selected" and
 * leaves the InputLabel unshrunk over the value it is covering. A slash is the
 * safe sentinel here for a reason that is not arbitrary: "/" is what separates
 * a project from its rung in every hz address ("<project>/<environment>"), so
 * no project can ever be named this.
 */
const NONE = "//none//";

/**
 * Assign one service, offering only what hz declares.
 *
 * TWO SELECTS AND NO TEXT BOX. The offerable set is `AssignIndex`, built from
 * `/projects` and `/environments`, so there is no way to compose a placement
 * `Save()` would refuse — which is the ordering trap the jobs analysis names,
 * closed by construction rather than by a validation message.
 *
 * `checkAssignment` still runs on every keystroke-equivalent, because the two
 * selects CAN still compose one illegal pair: a rung with no project. That is
 * legal to hold in the dialog (you have to pass through it to unassign) and
 * illegal to save, so it is refused inline at the moment of saving rather than
 * by making the rung select unreachable.
 */
export function AssignDialog({
  service,
  current,
  index,
  onClose,
}: {
  service: string;
  current: Placement;
  index: AssignIndex;
  onClose: () => void;
}) {
  return (
    <Dialog open fullWidth maxWidth="sm" onClose={onClose}>
      <AssignPanel service={service} current={current} index={index} onClose={onClose} />
    </Dialog>
  );
}

/**
 * The dialog's contents, separate from the Dialog shell.
 *
 * Split for the same reason `MoveHostPanel` is: MUI's Dialog renders through a
 * Portal, which produces NOTHING AT ALL under react-dom/server, so a dialog
 * left whole cannot be rendered offline and every rule it exists to keep — only
 * declared rungs offered, the consequence named, two ways out, a refusal shown
 * whole — would be checked by nobody. This was not a guess: the render check
 * was written first, and the first run came back with every dialog assertion
 * red against an empty string.
 */
export function AssignPanel({
  service,
  current,
  index,
  onClose,
}: {
  service: string;
  current: Placement;
  index: AssignIndex;
  onClose: () => void;
}) {
  const assign = useAssignService();
  const [project, setProject] = useState(current.project);
  const [rung, setRung] = useState(current.environment);
  const [done, setDone] = useState<Placement | null>(null);

  const next: Placement = { project, environment: rung };
  const rungs = declaredRungs(index, project);
  const check = checkAssignment(service, project, rung, index);
  const unchanged = samePlacement(current, next);

  const failed = assign.error;
  const refusal =
    failed instanceof ApiError || failed instanceof Error ? readAssignRefusal(failed.message) : null;

  const pickProject = (value: string) => {
    const p = value === NONE ? "" : value;
    setProject(p);
    // A rung means nothing under a different project — `prod` in acme-co is not
    // `prod` in storefront. Carrying the old name across would silently produce
    // either a wrong placement or an undeclared one.
    if (p === "") setRung("");
    else if (!declaredRungs(index, p).some((r) => r.name === rung)) setRung("");
  };

  const submit = () => {
    assign.mutate(
      { service, project: project.trim(), environment: rung.trim() },
      { onSuccess: (resp) => setDone({ project: resp.project ?? "", environment: resp.environment ?? "" }) },
    );
  };

  const noProjectsDeclared = index.state === "ready" && index.projects.length === 0;

  return (
    <>
      <DialogTitle sx={{ display: "flex", alignItems: "center", gap: 2 }}>
        <Box sx={{ flex: 1 }}>Where does {service} live?</Box>
        <IconButton onClick={onClose} aria-label="Close this dialog" title="Close">
          <CloseIcon />
        </IconButton>
      </DialogTitle>
      <DialogContent dividers>
        {done ? (
          <Alert severity="success" sx={{ mb: 2 }}>
            {service} is now filed at {placementLabel({ project: done.project, environment: done.environment })}.
            Nothing was rendered by this — no DNS record, no HAProxy backend — so there is nothing to
            sync; the config change shows up in `hz pending`.
          </Alert>
        ) : null}

        <Typography variant="body2" sx={{ mb: 2 }}>
          Today {service} is at <strong>{placementLabel(current)}</strong>. A service may name no
          project at all — that is legal and permanent, not a defect — and it may sit in a project
          without being on any rung.
        </Typography>
        {/* Said HERE, on open, and not only in the consequence line below.
            The consequence at open is the no-op one (the dialog starts at the
            current placement), so an operator deciding whether it is safe to
            touch this at all would otherwise have to change something first to
            find out that changing it renders nothing. */}
        <Typography variant="body2" sx={{ mb: 2, color: "text.secondary" }}>
          Moving a service between projects renders nothing: no DNS record, no HAProxy backend, no
          box is touched. It changes where hz files the service, and shows up in `hz pending` as a
          config change.
        </Typography>

        {noProjectsDeclared ? (
          <Alert severity="info" sx={{ mb: 2 }}>
            hz declares no project yet, so there is nowhere to file this service. Declare one first
            with <code>hz project add &lt;name&gt;</code>; a project is declared before anything
            moves into it. Until then {service} stays unassigned, which costs it nothing.
          </Alert>
        ) : null}

        {index.state === "loading" ? (
          <Alert severity="info" sx={{ mb: 2 }}>
            Waiting for hz to send the declared projects. The list below is what may be chosen, and
            it is not complete yet — this is not a claim that nothing is declared.
          </Alert>
        ) : null}
        {index.state === "failed" ? (
          <Alert severity="warning" sx={{ mb: 2 }}>
            hz could not be asked which projects are declared. Nothing here can be offered safely,
            because hz refuses a service naming a project it does not declare. Close this, reload,
            and try once hz answers.
          </Alert>
        ) : null}

        <FormControl fullWidth size="small" sx={{ mb: 2 }} disabled={assign.isPending}>
          <InputLabel id={`assign-project-${service}`}>Project</InputLabel>
          <Select
            labelId={`assign-project-${service}`}
            label="Project"
            value={project === "" ? NONE : project}
            onChange={(e) => pickProject(String(e.target.value))}
          >
            {/* Unassigned is an OPTION, not the absence of one. It is a state an
                operator deliberately chooses, so it is offered by name. */}
            <MenuItem value={NONE}>No project — leave {service} off the tree</MenuItem>
            {index.projects.map((p) => (
              <MenuItem key={p} value={p} sx={{ pl: 2 + (index.depthOf.get(p) ?? 0) * 2 }}>
                {p}
              </MenuItem>
            ))}
            {/* A service can already name a project hz no longer declares. The
                option is rendered so the select shows the truth rather than
                silently snapping to something else — but it is the one option
                that is not offerable, so it says so. */}
            {project !== "" && !index.rungs.has(project) && index.state === "ready" ? (
              <MenuItem value={project}>{project} — not declared by hz</MenuItem>
            ) : null}
          </Select>
          <Typography variant="caption" sx={{ color: "text.secondary", mt: 0.5 }}>
            Only projects hz declares are listed. hz refuses a service naming one that is not, so
            there is nothing here to type wrong — declare a new project with{" "}
            <code>hz project add &lt;name&gt;</code>.
          </Typography>
        </FormControl>

        <FormControl
          fullWidth
          size="small"
          sx={{ mb: 2 }}
          disabled={assign.isPending || project === ""}
        >
          <InputLabel id={`assign-rung-${service}`}>Environment</InputLabel>
          <Select
            labelId={`assign-rung-${service}`}
            label="Environment"
            value={rung === "" ? NONE : rung}
            onChange={(e) => {
              const v = String(e.target.value);
              setRung(v === NONE ? "" : v);
            }}
          >
            <MenuItem value={NONE}>No environment — in the project, on no rung</MenuItem>
            {rungs.map((r) => (
              <MenuItem key={r.name} value={r.name}>
                {r.name}
                {r.posture ? ` — ${r.posture} posture` : " — no declared posture"}
              </MenuItem>
            ))}
            {rung !== "" && !rungs.some((r) => r.name === rung) ? (
              <MenuItem value={rung}>{rung} — not declared by {project || "any project"}</MenuItem>
            ) : null}
          </Select>
          <Typography variant="caption" sx={{ color: "text.secondary", mt: 0.5 }}>
            {project === ""
              ? "Pick a project first. An environment name is unique per project, not globally, so a rung with no project resolves against nothing."
              : rungs.length === 0
                ? `${project} declares no environment. Legal — a project is declared before anything moves into it. Declare a rung with \`hz env add ${project}/<name> --posture <dev|staging|prod>\`.`
                : `The rungs ${project} declares. Every project gets its own "prod" — these are ${project}'s.`}
          </Typography>
        </FormControl>

        {!check.ok ? (
          <Alert severity="warning" sx={{ mb: 2 }}>
            <Typography sx={{ fontWeight: 700 }}>{check.headline}</Typography>
            <Typography variant="body2">{check.remedy}</Typography>
          </Alert>
        ) : null}

        {refusal ? (
          <Alert severity="error" sx={{ mb: 2 }} icon={false}>
            <Typography sx={{ fontWeight: 700, mb: 0.5 }}>
              {refusal.isOrderingRefusal ? "hz refused this placement." : "The assignment failed."}
            </Typography>
            <Typography variant="body2" sx={{ mb: refusal.remedy ? 0.5 : 0 }}>
              {refusal.headline}
            </Typography>
            {refusal.remedy ? (
              <Typography variant="body2" sx={{ color: "text.secondary" }}>
                {refusal.remedy}
              </Typography>
            ) : null}
            <Typography variant="caption" sx={{ display: "block", mt: 0.5, color: "text.secondary" }}>
              Nothing was changed. {service} is still at {placementLabel(current)}.
            </Typography>
          </Alert>
        ) : null}

        <Divider sx={{ mb: 1.5 }} />
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          {assignConsequence(service, current, next)}
        </Typography>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={assign.isPending}>
          {done ? "Close" : "Cancel — change nothing"}
        </Button>
        <Button
          variant="contained"
          onClick={submit}
          disabled={assign.isPending || unchanged || !check.ok || index.state !== "ready"}
        >
          {assign.isPending ? (
            <CircularProgress size={20} />
          ) : next.project === "" ? (
            `Take ${service} off the tree`
          ) : (
            `File ${service} under ${placementLabel(next)}`
          )}
        </Button>
      </DialogActions>
    </>
  );
}

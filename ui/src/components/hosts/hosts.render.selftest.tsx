/**
 * Assertions for the host screen's MARKUP — the whole screen, rendered offline.
 *
 * `hosts.selftest.ts` proves the decisions; this proves they reach the page,
 * and it renders the ROUTE COMPONENT rather than the pieces: the screen is
 * driven by a react-query read, and the failure this technique exists to catch
 * is a state that only exists between the component and the query — a pending
 * read rendered as an answered one. That bug was caught this way on the model
 * screens and it is invisible to tsc, to vite and to every decision check.
 *
 * So the query cache is SEEDED and the screen is rendered with react-dom/server,
 * three times over: answered, never-answered, and failed. No server, no network,
 * no browser.
 *
 * Still no test framework. Vite bundles it because Node cannot strip JSX;
 * `pnpm test` runs it.
 *
 * Names are placeholders — homelab-horizon is public.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import theme from "../../theme";
import type { HostRefView, HostView, HostsViewResp } from "../../api/generated-types";
import { HostsScreen, HostCard, MoveHostPanel } from "../../routes/hosts";
import { RefusalNote } from "./HostBits";
import { readRefusal } from "./hosts";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const HOSTS_VIEW_KEY = ["topology", "hosts-view"];

/** A client that never fetches: whatever is in the cache is the whole answer. */
function client(): QueryClient {
  return new QueryClient({
    // retryOnMount:false matters: without it react-query's optimistic result
    // reports a seeded ERROR as pending-and-fetching, because it would retry
    // the moment the component mounted. The screen would then be checked in a
    // state a browser never leaves it in.
    defaultOptions: {
      queries: { retry: false, retryOnMount: false, refetchOnMount: false, staleTime: Infinity },
    },
  });
}

function render(node: React.ReactNode, qc: QueryClient): { html: string; text: string } {
  const html = renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <ThemeProvider theme={theme}>{node}</ThemeProvider>
    </QueryClientProvider>,
  );
  // Entities decoded before the substring checks: react-dom/server escapes an
  // apostrophe to &#x27;, so prose containing one would fail against markup
  // that carries it perfectly — a false alarm that teaches whoever hits it to
  // weaken the assertion.
  const text = html
    .replace(/<[^>]*>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/\s+/g, " ");
  return { html, text };
}

/** The screen as a browser would receive it, with hz's answer already in hand. */
function screenWith(data: HostsViewResp) {
  const qc = client();
  qc.setQueryData(HOSTS_VIEW_KEY, data);
  return render(<HostsScreen />, qc);
}

function ref(over: Partial<HostRefView>): HostRefView {
  return {
    kind: "service backend",
    owner: "files",
    field: "proxy.backend",
    value: "@nas:8080",
    resolved: "192.168.1.51:8080",
    ...over,
  };
}

function host(over: Partial<HostView>): HostView {
  return {
    name: "nas",
    ip: "192.168.1.51",
    self: false,
    ref: "@nas",
    editable: true,
    addressable: true,
    references: [],
    ...over,
  };
}

const SELF_UNRESOLVED_WHY =
  "hz has not detected this instance's own LAN address yet, so it cannot say what @self means. local_interface on the Settings page is what fills it in.";

const GATEWAY: HostsViewResp = {
  literalsUnlisted: true,
  hosts: [
    host({
      name: "self",
      ip: "192.168.1.1",
      self: true,
      ref: "@self",
      editable: false,
      notEditableWhy:
        "@self is not a declaration: it is whichever instance is running, and it moves by setting local_interface on the Settings page.",
      references: [
        ref({
          kind: "service internal DNS",
          owner: "grafana",
          field: "internal_dns.ip",
          value: "@self",
          resolved: "192.168.1.1",
        }),
        ref({
          kind: "service internal DNS",
          owner: "wiki",
          field: "internal_dns.ip",
          value: "@self",
          resolved: "192.168.1.1",
        }),
      ],
    }),
    host({
      references: [
        ref({ kind: "port forward", owner: "files", field: "forwards[0].backend", value: "@nas:22", resolved: "192.168.1.51:22" }),
        ref({}),
      ],
    }),
    // Declared, referenced by nothing. The row that must not read as "safe".
    host({ name: "spare", ip: "192.168.1.60", ref: "@spare", references: [] }),
  ],
};

console.log("host screen — rendered markup");

// ---------------------------------------------------------------------------
console.log("· a reference reaches the page with BOTH halves");
// ---------------------------------------------------------------------------
{
  const { text } = screenWith(GATEWAY);
  check(text.includes("@nas:8080"), "the authored value is on the page");
  check(text.includes("192.168.1.51:8080"), "the resolved value is on the page");
  const authored = text.indexOf("@nas:8080");
  const resolved = text.indexOf("192.168.1.51:8080");
  check(authored < resolved, "the authored half leads and the resolution follows");
  check(text.includes("@nas:22") && text.includes("192.168.1.51:22"), "…for every record, not just the first");
  check(
    text.includes("files") && text.includes("forwards[0].backend"),
    "each record names its owner and its field, so it can be found",
  );
}

// ---------------------------------------------------------------------------
console.log("· @self is a first-class row, and it is not editable here");
// ---------------------------------------------------------------------------
{
  const { html, text } = screenWith(GATEWAY);
  // Compared on the ROW CHIPS, not on the notation: the legend mentions both
  // spellings above the list, so an index of "@self" would measure the legend.
  check(text.includes("this instance"), "@self is labelled as what it is");
  check(
    text.indexOf("this instance") < text.indexOf("A declared host. Write @nas"),
    "…and its row leads the declared ones",
  );

  const buttons = (html.match(/<button[^>]*>([\s\S]*?)<\/button>/g) ?? []).map((b) =>
    b.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim(),
  );
  check(
    buttons.some((b) => b === "Change the address of @nas"),
    `a declared host offers a labelled address change (buttons: ${buttons.join(" | ") || "none"})`,
  );
  check(
    !buttons.some((b) => /@self/.test(b)),
    "@self offers no address control at all",
  );
  // Greyed with a reason, never removed: the address field is still rendered.
  check(text.includes("192.168.1.1"), "@self's address is shown, not hidden");
  check(
    text.includes("local_interface"),
    "…with the sentence naming where it IS changed",
  );
}

// ---------------------------------------------------------------------------
console.log("· a host nothing points at does not read as a host nothing needs");
// ---------------------------------------------------------------------------
{
  const { text } = screenWith(GATEWAY);
  check(text.includes("nothing references this host"), "the zero state says so plainly");
  check(
    text.includes("cannot list records that carry"),
    "…and the literal caveat is on the page beside it",
  );
  check(
    !/nothing (will )?break|safe to move/i.test(text),
    "nothing on the page promises that moving an unreferenced host is free",
  );

  // The caveat is not a one-off footnote under the empty host: it is stated
  // for a populated host too, where "2 records" is just as incomplete.
  const caveats = text.split("cannot list records that carry").length - 1;
  check(caveats >= 3, `the caveat is carried by every host row (found ${caveats}, wanted 3)`);
}

// ---------------------------------------------------------------------------
console.log("· the kinds on the page are the CLI's kinds");
// ---------------------------------------------------------------------------
{
  const { text } = screenWith(GATEWAY);
  for (const kind of ["service backend", "port forward", "service internal DNS"]) {
    check(text.includes(kind), `"${kind}" is the heading, verbatim`);
  }
  check(
    text.includes("HAProxy forwards this service"),
    "a kind heading carries what goes wrong if the address is wrong",
  );
  check(
    text.includes("iptables DNAT"),
    "…and the consequence differs per kind",
  );
}

// ---------------------------------------------------------------------------
console.log("· not answered, failed, and answered-with-nothing are three pages");
// ---------------------------------------------------------------------------
{
  // Never answered: the cache is empty and nothing fetches under SSR.
  const pending = render(<HostsScreen />, client()).text;

  // Failed: the query is in the cache, in an error state.
  const qc = client();
  const q = qc.getQueryCache().build(qc, { queryKey: HOSTS_VIEW_KEY });
  q.setState({ status: "error", error: new Error("503 Service Unavailable"), fetchStatus: "idle" });
  const failed = render(<HostsScreen />, qc).text;

  // Answered, and the answer is "only @self".
  const empty = screenWith({
    literalsUnlisted: true,
    hosts: [
      host({ name: "self", ip: "192.168.1.1", self: true, ref: "@self", editable: false, notEditableWhy: "set on Settings", references: [] }),
    ],
  }).text;

  check(/has not answered yet/.test(pending), "a never-answered read says exactly that");
  check(/Asking hz which hosts/.test(pending), "…with a spinner saying the question is in flight");
  check(
    !/No host is declared/i.test(pending),
    "…and never claims nothing is declared while the answer is in flight",
  );
  check(
    !/nothing references this host/i.test(pending),
    "…and captions no host as unreferenced",
  );

  check(failed.includes("503 Service Unavailable"), "a failed read carries the failure");
  check(
    /could not be asked/.test(failed),
    "…and says the question failed, not that the answer is none",
  );
  check(!/No host is declared/i.test(failed), "…and does not claim an empty config");

  check(
    /No host is declared besides @self/.test(empty),
    "an answered-and-empty config says so, as an empty list",
  );
  check(
    /not a failed read/.test(empty),
    "…and says out loud that it is not a failed read",
  );

  check(
    new Set([pending, failed, empty]).size === 3,
    "three states, three pages",
  );
}

// ---------------------------------------------------------------------------
console.log("· @self before hz knows its own address: named, not resolved");
// ---------------------------------------------------------------------------
{
  const { text } = screenWith({
    literalsUnlisted: true,
    hosts: [
      host({
        name: "self",
        ip: "",
        self: true,
        ref: "@self",
        editable: false,
        addressable: false,
        notEditableWhy: "@self moves by setting local_interface on the Settings page.",
        notAddressableWhy: SELF_UNRESOLVED_WHY,
        references: [
          ref({
            kind: "service internal DNS",
            owner: "grafana",
            field: "internal_dns.ip",
            value: "@self",
            resolved: undefined,
            resolveError: '"@self" is this instance\'s own address, which hz has not detected yet',
          }),
        ],
      }),
    ],
  });
  check(text.includes("@self"), "the dependant keeps its authored half");
  check(
    text.includes("hz cannot say where this points"),
    "…and the resolved half says hz cannot say, rather than being blank",
  );
  check(
    text.includes("has not detected yet"),
    "…carrying hz's own reason",
  );
  check(
    text.includes("hz has not detected one"),
    "the host's address field says it is undetected, not empty",
  );
  check(
    !/nothing references this host/i.test(text),
    "an unresolvable host still reports the records that DO point at it",
  );
}

// ---------------------------------------------------------------------------
console.log("· the address change names its consequence and can be left");
// ---------------------------------------------------------------------------
{
  const target = GATEWAY.hosts[1] as HostView;
  const { html, text } = render(<MoveHostPanel host={target} onClose={() => {}} />, client());

  check(text.includes("Change the address of @nas"), "the dialog is titled with the verb");
  check(
    text.includes("repoints 2 records in one config write"),
    "the consequence counts the records that follow",
  );
  check(
    text.includes("1 port forward") && text.includes("1 service backend"),
    "…and names them by kind",
  );
  check(
    text.includes("These 2 records will follow it"),
    "the records themselves are listed before the save, not just counted",
  );
  check(text.includes("@nas:22") && text.includes("192.168.1.51:22"), "…with both halves, again");

  const buttons = (html.match(/<button[^>]*>([\s\S]*?)<\/button>/g) ?? []).map((b) =>
    b.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim(),
  );
  check(
    buttons.some((b) => /^Cancel/.test(b)),
    `a labelled cancel exists (buttons: ${buttons.join(" | ")})`,
  );
  check(
    html.includes('aria-label="Close this dialog"'),
    "and a visible close control, so Esc and click-outside are not the only way out",
  );
  check(
    buttons.some((b) => b === "Save — move 2 records"),
    "the confirm button says what it will do, not just 'Save'",
  );

  // An unreferenced host gets a DIFFERENT dialog, not the same one with 0.
  const spare = render(<MoveHostPanel host={GATEWAY.hosts[2] as HostView} onClose={() => {}} />, client());
  check(
    spare.text.includes("and nothing else"),
    "moving an unreferenced host says nothing else moves",
  );
  check(
    !spare.text.includes("will follow it"),
    "…and lists no followers",
  );
}

// ---------------------------------------------------------------------------
console.log("· a refusal renders every dependant, not just the first line");
// ---------------------------------------------------------------------------
{
  const message = [
    'host "nas" is still referenced by 2 record(s):',
    "  service backend files (proxy.backend = @nas:8080)",
    "  port forward files (forwards[0].backend = @nas:22)",
    "",
    "Repoint or remove them first, or `hz host set nas <new ip>` if the box simply moved",
  ].join("\n");
  const { text } = render(<RefusalNote refusal={readRefusal(message)} />, client());

  check(text.includes("still referenced by 2 record(s)"), "the headline is on the page");
  check(
    text.includes("proxy.backend = @nas:8080") && text.includes("forwards[0].backend = @nas:22"),
    "EVERY dependant the backend named is on the page — the part a toast eats",
  );
  check(text.includes("hz host set nas"), "the remedy is on the page");
  check(
    text.includes("This is not a failure"),
    "…and the refusal is explained as a refusal, not an error",
  );

  const generic = render(<RefusalNote refusal={readRefusal("invalid IP: 192.168.1.999")} />, client()).text;
  check(generic.includes("invalid IP: 192.168.1.999"), "an unrelated error keeps its text");
  check(
    !generic.includes("This is not a failure"),
    "…and is not dressed up as a dependency refusal",
  );
}

// ---------------------------------------------------------------------------
console.log("· a kind this screen does not know is listed, not dropped");
// ---------------------------------------------------------------------------
{
  const { text } = render(
    <HostCard
      host={host({ references: [ref({ kind: "wireguard peer address", field: "peers[0].endpoint" })] })}
      literalsUnlisted
      onMove={() => {}}
    />,
    client(),
  );
  check(text.includes("wireguard peer address"), "the unknown kind is named");
  check(text.includes("peers[0].endpoint"), "…and its record is listed");
  check(
    text.includes("does not recognise"),
    "…and the screen admits it does not know what the kind means",
  );
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} host render check(s) failed`);
}

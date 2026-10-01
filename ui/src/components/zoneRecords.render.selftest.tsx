/**
 * Assertions for the DNS records screen's MARKUP around delegation — rendered
 * offline, the query cache SEEDED, the same technique as
 * `hosts/hosts.render.selftest.tsx`.
 *
 *   A delegated name is one line — "delegated to <ns…> — records below it
 *   belong to that zone's owner" — and a zone that delegates nothing renders no
 *   such line at all (flows over prose: no empty explanation panel).
 *
 *   A record live below a delegation is marked, because no resolver reads it
 *   from this zone.
 *
 *   The add form offers NS, and for NS asks for nameservers one per line.
 *
 * Names are placeholders — homelab-horizon is public.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import theme from "../theme";
import type { ZoneRecordsResponse } from "../api/generated-types";
import { AddRecordFields, ZoneRecordsTable } from "./ZoneRecords";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function client(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, retryOnMount: false, refetchOnMount: false, staleTime: Infinity },
    },
  });
}

function decode(s: string): string {
  return s
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">");
}

function render(node: React.ReactNode, qc = client()): { html: string; text: string; titles: string } {
  const html = renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <ThemeProvider theme={theme}>{node}</ThemeProvider>
    </QueryClientProvider>,
  );
  const text = decode(html.replace(/<[^>]*>/g, " ")).replace(/\s+/g, " ");
  const titles = decode([...html.matchAll(/(?:title|aria-label)="([^"]*)"/g)].map((m) => m[1]!).join(" | "));
  return { html, text, titles };
}

const ZONE = "example.com";

function screenWith(data: ZoneRecordsResponse) {
  const qc = client();
  qc.setQueryData(["zones", "records", ZONE], data);
  return render(<ZoneRecordsTable zoneName={ZONE} />, qc);
}

// --- a zone with a delegation ----------------------------------------------
{
  const { html, text, titles } = screenWith({
    zone: ZONE,
    records: [
      { name: "example.com", type: "NS", value: "ns-apex.example.net", ttl: 172800, owner: "observed" },
      { name: "loadtest.app.example.com", type: "NS", value: "ns-1.awsdns-01.org", ttl: 300, owner: "declared" },
      { name: "loadtest.app.example.com", type: "NS", value: "ns-2.awsdns-02.com", ttl: 300, owner: "declared" },
      { name: "stale.loadtest.app.example.com", type: "A", value: "192.0.2.9", ttl: 300, owner: "observed" },
      { name: "app.example.com", type: "A", value: "192.0.2.1", ttl: 300, owner: "derived" },
    ],
    declared: [
      { name: "loadtest.app.example.com", type: "NS", value: "ns-1.awsdns-01.org", ttl: 300, live: true },
      { name: "loadtest.app.example.com", type: "NS", value: "ns-2.awsdns-02.com", ttl: 300, live: true },
    ],
  });
  check(html.includes('data-delegation="loadtest.app.example.com"'), "the delegation has its line");
  check(
    text.includes(
      "loadtest.app.example.com delegated to ns-1.awsdns-01.org, ns-2.awsdns-02.com — records below it belong to that zone's owner",
    ),
    "the line names the nameservers and the consequence",
  );
  check(!html.includes('data-delegation="example.com"'), "the apex NS is not shown as a delegation");
  check(!text.includes("not yet live"), "a live delegation is not flagged as pending");
  check(text.includes("below delegation"), "a record below the delegation is marked");
  check(titles.includes("loadtest.app.example.com is delegated to"), "the mark says which delegation, on hover");
  const marks = text.split("below delegation").length - 1;
  check(marks === 1, `exactly one record is marked below the delegation (got ${marks})`);
}

// --- declared but not yet published -------------------------------------------
{
  const { text } = screenWith({
    zone: ZONE,
    records: [],
    declared: [{ name: "loadtest.app.example.com", type: "NS", value: "ns-1.awsdns-01.org", ttl: 300, live: false }],
  });
  check(text.includes("loadtest.app.example.com delegated to ns-1.awsdns-01.org"), "a declared delegation shows before it is live");
  check(text.includes("not yet live at the provider"), "and says it is not live yet");
}

// --- no delegation --------------------------------------------------------------
{
  const { html, text } = screenWith({
    zone: ZONE,
    records: [{ name: "example.com", type: "NS", value: "ns-apex.example.net", ttl: 172800, owner: "observed" }],
    declared: [],
  });
  check(!html.includes("data-delegation"), "no delegation → no delegation line");
  check(!text.includes("delegated to"), "no delegation → no delegation prose");
  check(!text.includes("below delegation"), "no delegation → nothing marked");
}

// --- the add form ---------------------------------------------------------------
{
  const noop = () => {};
  const nsForm = render(
    <AddRecordFields zoneName={ZONE} form={{ name: "", type: "NS", value: "", ttl: 300 }} setForm={noop} refusal={undefined} />,
  );
  check(nsForm.text.includes("Nameservers"), "NS asks for nameservers");
  check(nsForm.text.includes("One per line"), "one per line");
  check(nsForm.html.includes("<textarea"), "NS is multi-line");
  check(nsForm.text.includes("never the zone apex"), "the name hint says not the apex");

  const aForm = render(
    <AddRecordFields zoneName={ZONE} form={{ name: "", type: "A", value: "", ttl: 300 }} setForm={noop} refusal={undefined} />,
  );
  check(!aForm.text.includes("Nameservers"), "A asks for a value, not nameservers");
  check(!aForm.html.includes("data-delegation-refusal"), "no refusal → no refusal line");

  const refused = render(
    <AddRecordFields
      zoneName={ZONE}
      form={{ name: "www.loadtest.app.example.com", type: "A", value: "192.0.2.1", ttl: 300 }}
      setForm={noop}
      refusal="loadtest.app.example.com is delegated to ns-1.awsdns-01.org — records below it belong to that zone's owner"
    />,
  );
  check(refused.html.includes("data-delegation-refusal"), "a refused name shows the delegation");
}

console.log(`zoneRecords render: ${checks - failures}/${checks} checks passed`);
if (failures > 0) process.exit(1);

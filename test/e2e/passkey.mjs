// Drives a full WebAuthn ceremony as a jailed peer, using Chrome's virtual
// authenticator over CDP.
//
// Runs in the VM's `client` network namespace against an HTTPS portal, because
// both halves are load-bearing: the peer must be genuinely jailed (source IP)
// and the page must be a genuine secure context (WebAuthn refuses otherwise).
// Everything short of the authenticator hardware is real — the ceremony, the
// challenge, the signature, and hz's verification of it.
import { chromium } from "playwright";

const PORTAL = "https://vpn.e2e.test";
const OTHER = "https://wiki.e2e.test";
const MFA = `${PORTAL}/app/mfa`;

let failures = 0;
const ok = (m) => console.log(`  PASS  ${m}`);
const bad = (m, d) => {
  console.log(`  FAIL  ${m}`);
  if (d) console.log(`        ${d}`);
  failures++;
};

async function main() {
  const browser = await chromium.launch({
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  // The fixture's cert is self-signed; the point here is WebAuthn, not PKI.
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();

  // A virtual authenticator stands in for a security key or platform
  // authenticator. Chrome generates real credentials and real signatures with
  // it — hz cannot tell the difference, which is what makes this a test of hz
  // rather than of the mock.
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  const { authenticatorId } = await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "internal",
      hasResidentKey: false,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });

  await page.goto(MFA, { waitUntil: "networkidle" });
  await page.waitForTimeout(800);

  const body = await page.locator("body").innerText();
  if (/passkeys unavailable/i.test(body)) {
    bad("passkeys offered over https", body.slice(0, 200));
    await browser.close();
    process.exit(1);
  }
  ok("portal offers passkeys over https");

  // ---- register ----
  const addBtn = page.getByRole("button", { name: /add a passkey/i });
  if (!(await addBtn.count())) {
    bad("Add a Passkey button present", (await page.locator("body").innerText()).slice(0, 300));
    await browser.close();
    process.exit(1);
  }
  await page.getByLabel(/device name/i).fill("virtual key");
  await addBtn.click();
  await page.waitForTimeout(2500);

  const creds = await cdp.send("WebAuthn.getCredentials", { authenticatorId });
  if (creds.credentials.length === 1) {
    ok("authenticator holds exactly one credential after registration");
  } else {
    bad("authenticator holds exactly one credential", `got ${creds.credentials.length}`);
  }

  // Registration alone must not clear the jail — same rule as TOTP, where
  // showing the QR is not authenticating.
  await page.reload({ waitUntil: "networkidle" });
  await page.waitForTimeout(800);
  const afterRegister = await page.locator("body").innerText();
  if (/unlock with passkey/i.test(afterRegister)) {
    ok("registration alone does not open a session");
  } else {
    bad("registration alone does not open a session", afterRegister.slice(0, 200));
  }

  // ---- assert ----
  await page.getByRole("button", { name: /unlock with passkey/i }).click();
  await page.waitForTimeout(3000);

  const afterAssert = await page.locator("body").innerText();
  if (/session is active/i.test(afterAssert)) {
    ok("passkey assertion opens an MFA session");
  } else {
    bad("passkey assertion opens an MFA session", afterAssert.slice(0, 300));
  }

  // ---- the jail actually lifted ----
  //
  // Retried: clearing the jail rewrites HAProxy's source-ACL file and reloads,
  // and a reload is near-seamless but not atomic — a connection opened in that
  // sub-second window can be refused. Asserting on the first attempt would
  // make this test flaky for a reason that has nothing to do with the ceremony.
  let reached = "";
  let status = 0;
  let attempts = 0;
  for (; attempts < 10; attempts++) {
    try {
      const resp = await page.goto(OTHER, { waitUntil: "networkidle" });
      status = resp ? resp.status() : 0;
      reached = await page.locator("body").innerText();
      if (status === 200 && reached.includes("LAN-SECRET-CONTENT")) break;
    } catch {
      // reload window — fall through and retry
    }
    await page.waitForTimeout(500);
  }
  if (status === 200 && reached.includes("LAN-SECRET-CONTENT")) {
    ok(`jail lifted — the LAN service is reachable again${attempts ? ` (after ${attempts} retr${attempts === 1 ? "y" : "ies"} through the reload window)` : ""}`);
  } else {
    bad("jail lifted — the LAN service is reachable again",
        `status=${status} url=${page.url()} body=${reached.slice(0, 120)}`);
  }

  await browser.close();
  console.log(failures === 0 ? "\n  passkey flow: all checks passed" : `\n  passkey flow: ${failures} failed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});

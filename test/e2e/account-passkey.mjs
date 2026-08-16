// Drives a passkey ceremony for an ADMIN ACCOUNT, using Chrome's virtual
// authenticator over CDP.
//
// Distinct from passkey.mjs, which enrols a VPN peer at the captive portal.
// The difference is not cosmetic: a credential is scoped to its relying party
// id, so a passkey enrolled against kiosk_url does not exist as far as the
// admin origin is concerned. This exercises the second relying party, built
// from admin_url, and the two-step login it gates.
import { chromium } from "playwright";

// The SPA is served under /app/; the bare origin redirects there.
const ADMIN = "https://vpn.e2e.test";
const APP = `${ADMIN}/app/`;
const USER = "carl";
const PASSWORD = "correct-horse-battery";

let failures = 0;
const ok = (m) => console.log(`  PASS  ${m}`);
const bad = (m, d) => {
  console.log(`  FAIL  ${m}`);
  if (d) console.log(`        ${d}`);
  failures++;
};

// The app is a SPA behind a router; give it a beat to settle after navigation
// rather than racing its first render.
const settle = (page) => page.waitForTimeout(1200);

async function main() {
  const browser = await chromium.launch({
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();

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

  // ---- sign in with the password ----
  await page.goto(APP, { waitUntil: "networkidle" });
  await settle(page);

  await page.getByLabel(/username/i).fill(USER);
  await page.getByLabel(/^password$/i).fill(PASSWORD);
  await page.getByRole("button", { name: /sign in/i }).click();
  await page.waitForTimeout(2500);

  // ---- enrol a passkey from the Users tab ----
  await page.goto(`${APP}settings`, { waitUntil: "networkidle" });
  await settle(page);
  const usersTab = page.getByRole("tab", { name: /users/i });
  if (!(await usersTab.count())) {
    bad("the Users tab is present", (await page.locator("body").innerText()).slice(0, 300));
    await browser.close();
    process.exit(1);
  }
  await usersTab.click();
  await settle(page);

  const addPasskey = page.getByRole("button", { name: /add passkey/i });
  if (!(await addPasskey.count())) {
    bad("Add passkey button present", (await page.locator("body").innerText()).slice(0, 400));
    await browser.close();
    process.exit(1);
  }
  if (await addPasskey.isDisabled()) {
    bad(
      "Add passkey is enabled over an https admin_url",
      (await page.locator("body").innerText()).slice(0, 400),
    );
    await browser.close();
    process.exit(1);
  }
  ok("passkeys are offered on the account once admin_url is https");

  await addPasskey.click();
  await page.waitForTimeout(3000);

  const creds = await cdp.send("WebAuthn.getCredentials", { authenticatorId });
  if (creds.credentials.length === 1) {
    ok("the authenticator holds one credential after enrolment");
  } else {
    bad("the authenticator holds one credential", `got ${creds.credentials.length}`);
  }

  // ---- sign out, then prove the password alone is no longer enough ----
  await context.clearCookies();
  await page.goto(APP, { waitUntil: "networkidle" });
  await settle(page);

  await page.getByLabel(/username/i).fill(USER);
  await page.getByLabel(/^password$/i).fill(PASSWORD);
  await page.getByRole("button", { name: /sign in/i }).click();
  await page.waitForTimeout(2500);

  const challenge = await page.locator("body").innerText();
  if (/one more step/i.test(challenge)) {
    ok("the password stops at a second-factor challenge");
  } else {
    bad("the password stops at a second-factor challenge", challenge.slice(0, 300));
  }

  const usePasskey = page.getByRole("button", { name: /use a passkey/i });
  if (!(await usePasskey.count())) {
    bad("the challenge offers the passkey", challenge.slice(0, 300));
    await browser.close();
    process.exit(1);
  }

  // ---- assert, and land inside the app ----
  await usePasskey.click();
  await page.waitForTimeout(3500);

  const afterAssert = await page.locator("body").innerText();
  const signedIn = !/one more step/i.test(afterAssert) && !/^\s*$/.test(afterAssert);
  if (signedIn) {
    ok("the passkey assertion completes the login");
  } else {
    bad("the passkey assertion completes the login", afterAssert.slice(0, 300));
  }

  // The session must be real, not merely a rendered page: ask the API as the
  // browser, which is the only thing that proves a cookie was issued.
  const status = await page.evaluate(async () => {
    const r = await fetch("/api/v1/auth/status", { credentials: "include" });
    return r.json();
  });
  if (status.authenticated && status.method === "user" && status.username === USER) {
    ok(`the session is a named account (${status.username})`);
  } else {
    bad("the session is a named account", JSON.stringify(status));
  }

  await browser.close();
  console.log(
    failures === 0
      ? "\n  account passkey flow: all checks passed"
      : `\n  account passkey flow: ${failures} failed`,
  );
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});

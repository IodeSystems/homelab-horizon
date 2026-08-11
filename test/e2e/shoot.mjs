// Captures the captive-portal flow as a genuinely jailed VPN peer.
//
// Runs inside the multipass VM's `client` network namespace, so every request
// really does originate from a jailed peer's WireGuard address — hz identifies
// peers by source IP, and there is no way to fake that convincingly from
// outside the tunnel. That is why these shots are taken here rather than in
// the hermetic Docker fixture that produces the rest of docs/screenshots.
import { chromium } from "playwright";
import { execSync } from "node:child_process";

const OUT = "/opt/shoot/out";
const PORTAL = "http://vpn.e2e.test";
const OTHER = "http://wiki.e2e.test";
const HZ = "http://10.100.0.1:8080";
// The UI is a SPA mounted under /app/ — bare /mfa is a 404 from the Go mux.
const MFA_PATH = "/app/mfa";

const shot = async (page, name) => {
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${OUT}/${name}.png` });
  console.log(`captured ${name}.png`);
};

async function main() {
  const browser = await chromium.launch({
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  const page = await browser.newPage({
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 2,
  });

  // 1. The captive-portal moment: ask for an unrelated service, get bounced.
  await page.goto(OTHER, { waitUntil: "networkidle" });
  console.log(`  ${OTHER} landed on ${page.url()}`);
  await shot(page, "mfa");

  // 2. Enrollment: the QR an unenrolled peer scans.
  await page.goto(`${PORTAL}${MFA_PATH}`, { waitUntil: "networkidle" });
  const enrollBtn = page.getByRole("button", { name: /set up|enroll|begin/i }).first();
  if (await enrollBtn.count()) {
    await enrollBtn.click();
    await page.waitForTimeout(1200);
  }
  await shot(page, "mfa-enroll");

  // 3. Verified: same peer, same page, jail lifted. The code is minted from
  //    the secret hz just issued — the real TOTP path, not a stubbed session.
  const secret = execSync(
    `curl -fsS -X POST ${HZ}/api/v1/mfa/enroll | jq -r '.secret' 2>/dev/null || true`,
    { encoding: "utf8" },
  ).trim();
  const useSecret =
    secret && secret !== "null"
      ? secret
      : execSync(
          `jq -r '.vpn_mfa_secrets.shooter' /etc/homelab-horizon/config.json`,
          { encoding: "utf8" },
        ).trim();
  const code = execSync(`oathtool --totp -b ${useSecret}`, { encoding: "utf8" }).trim();

  await page.goto(`${PORTAL}${MFA_PATH}`, { waitUntil: "networkidle" });
  const field = page.getByLabel(/code/i).first();
  await field.fill(code);
  await shot(page, "mfa-verify");

  await page.getByRole("button", { name: /unlock|verify/i }).first().click();
  await page.waitForTimeout(2500);

  // 4. Proof the jail actually lifted: the service that was unreachable.
  await page.goto(OTHER, { waitUntil: "networkidle" });
  console.log(`  after verify, ${OTHER} landed on ${page.url()}`);
  await shot(page, "mfa-unlocked");

  await browser.close();
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});

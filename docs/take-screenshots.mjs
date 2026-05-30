#!/usr/bin/env node
// Capture README screenshots with Playwright against a running HZ instance.
//
// Usage: node docs/take-screenshots.mjs [base-url] [admin-token]
//   base-url     default http://127.0.0.1:18080
//   admin-token  default matches docker/screenshot-config.json
//
// Intended to be driven by bin/screenshots, which boots a hermetic Docker
// container (no real network) and tears it down afterward. The container's
// config pins the admin token below and uses RFC 5737 documentation IPs, so
// nothing here touches a real homelab.

import { chromium } from "playwright";

const BASE = process.argv[2] || "http://127.0.0.1:18080";
const TOKEN = process.argv[3] || "demo-screenshot-token-do-not-use";
const SCREENSHOT_DIR = new URL("./screenshots/", import.meta.url).pathname;
const VIEWPORT = { width: 1280, height: 900 };

async function main() {
  const browser = await chromium.launch({ args: ["--no-sandbox"] });
  const context = await browser.newContext({ viewport: VIEWPORT });
  const page = await context.newPage();

  // Land on the SPA so fetch() runs with the right origin, then log in.
  // Login sets an HttpOnly session cookie scoped to the context.
  await page.goto(`${BASE}/app/`, { waitUntil: "domcontentloaded" });
  const login = await page.evaluate(async (token) => {
    const r = await fetch("/api/v1/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
    });
    return { status: r.status, body: await r.json().catch(() => null) };
  }, TOKEN);

  if (login.status !== 200 || !login.body?.ok) {
    console.error("Login failed:", login);
    process.exit(1);
  }
  console.log("Logged in");

  const shoot = (name) =>
    page.screenshot({ path: `${SCREENSHOT_DIR}${name}.png` });

  // Settle helper: navigate, wait for network idle, let MUI animations finish.
  const visit = async (path) => {
    await page.goto(`${BASE}/app/${path}`, { waitUntil: "networkidle" });
    await page.waitForTimeout(600);
  };

  // Dashboard
  await visit("dashboard");
  await shoot("dashboard");
  console.log("Captured dashboard.png");

  // Services list
  await visit("services");
  await page.waitForSelector("table");
  await shoot("services");
  console.log("Captured services.png");

  // Service detail — expand the grafana row
  const expanded = await page.evaluate(() => {
    for (const td of document.querySelectorAll("td")) {
      if (td.textContent.trim() === "grafana") {
        td.closest("tr").click();
        return true;
      }
    }
    return false;
  });
  if (expanded) {
    await page.waitForTimeout(600);
    await shoot("services-detail");
    console.log("Captured services-detail.png");
  } else {
    console.warn("grafana row not found — skipping services-detail.png");
  }

  // Port Map dialog — reload to collapse the expanded row first
  await visit("services");
  await page.waitForSelector("table");
  const opened = await page.evaluate(() => {
    for (const b of document.querySelectorAll("button")) {
      if (b.textContent.trim() === "Port Map") {
        b.click();
        return true;
      }
    }
    return false;
  });
  if (opened) {
    await page.waitForSelector('div[role="dialog"]');
    await page.waitForTimeout(600);
    await shoot("port-map");
    console.log("Captured port-map.png");
  } else {
    console.warn("Port Map button not found — skipping port-map.png");
  }

  // Remaining top-level pages
  for (const p of ["domains", "vpn", "checks", "settings"]) {
    await visit(p);
    await shoot(p);
    console.log(`Captured ${p}.png`);
  }

  await browser.close();
  console.log("Done");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});

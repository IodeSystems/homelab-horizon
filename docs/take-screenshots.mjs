#!/usr/bin/env node
// Takes demo screenshots for the README using puppeteer.
// Usage: node docs/take-screenshots.mjs [base-url] [admin-token]

import puppeteer from 'puppeteer';

const BASE = process.argv[2] || 'http://localhost:18080';
const TOKEN = process.argv[3] || '0460c25502e5038db08b25009d5e4cd3';
const SCREENSHOT_DIR = new URL('./screenshots/', import.meta.url).pathname;

async function main() {
  const browser = await puppeteer.launch({
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
    executablePath: '/usr/bin/google-chrome',
  });

  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 900 });

  // Navigate to the app first (so fetch has the right origin)
  await page.goto(`${BASE}/app/`, { waitUntil: 'networkidle0' });

  // Login via fetch from page context
  const resp = await page.evaluate(async (token) => {
    const r = await fetch('/api/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    });
    return r.json();
  }, TOKEN);

  if (!resp.ok) {
    console.error('Login failed:', resp);
    process.exit(1);
  }
  console.log('Logged in');

  // Services page
  await page.goto(`${BASE}/app/services`, { waitUntil: 'networkidle0' });
  await page.waitForSelector('table');
  await new Promise(r => setTimeout(r, 500));
  await page.screenshot({ path: `${SCREENSHOT_DIR}services.png` });
  console.log('Captured services.png');

  // Expand grafana row by clicking text
  const clicked = await page.evaluate(() => {
    const cells = document.querySelectorAll('td');
    for (const td of cells) {
      if (td.textContent.trim() === 'grafana') {
        td.closest('tr').click();
        return true;
      }
    }
    return false;
  });
  if (clicked) {
    await new Promise(r => setTimeout(r, 500));
    await page.screenshot({ path: `${SCREENSHOT_DIR}services-detail.png` });
    console.log('Captured services-detail.png');
  }

  // Settings page
  await page.goto(`${BASE}/app/settings`, { waitUntil: 'networkidle0' });
  await new Promise(r => setTimeout(r, 500));
  await page.screenshot({ path: `${SCREENSHOT_DIR}settings.png` });
  console.log('Captured settings.png');

  // VPN page
  await page.goto(`${BASE}/app/vpn`, { waitUntil: 'networkidle0' });
  await new Promise(r => setTimeout(r, 500));
  await page.screenshot({ path: `${SCREENSHOT_DIR}vpn.png` });
  console.log('Captured vpn.png');

  // Checks page
  await page.goto(`${BASE}/app/checks`, { waitUntil: 'networkidle0' });
  await new Promise(r => setTimeout(r, 500));
  await page.screenshot({ path: `${SCREENSHOT_DIR}checks.png` });
  console.log('Captured checks.png');

  await browser.close();
  console.log('Done');
}

main().catch(e => { console.error(e); process.exit(1); });

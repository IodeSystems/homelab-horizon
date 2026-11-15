/**
 * Assertions for the Observability page's auto-update command.
 *
 * No test framework — a plain Node program. `pnpm test` runs it.
 *
 * What it is defending: the page installs exactly what setup.sh installs. The
 * command must carry the server's script byte-for-byte inside a QUOTED heredoc
 * (so `$TMP`, `$(...)` and the script's own heredocs reach bash unexpanded), and
 * must never grow a second installer — the cron refresher it replaced wrote a
 * partial hz.yml straight onto the live file.
 */
import { heredocDelimiter, setupInstallCommand } from "./promSetup.ts";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const script = [
  "#!/usr/bin/env bash",
  'HZ_TOKEN="tok"',
  "install -m0755 /dev/stdin \"$REFRESH_BIN\" <<'HZ_REFRESH_EOF'",
  'TMP="$(mktemp)"',
  "HZ_REFRESH_EOF",
  "",
].join("\n");

{
  const cmd = setupInstallCommand(script);
  check(cmd.includes(`<<'HZ_SETUP_EOF'\n${script}HZ_SETUP_EOF\n`), "the script is carried verbatim in a quoted heredoc");
  check(cmd.includes('sudo bash "$f"'), "the command runs the script with sudo");
  check(cmd.includes('rm -f "$f"'), "the temp copy (it holds the token) is removed");
  check(!cmd.includes("cron"), "the command installs no cron entry");
}

{
  const hostile = "echo a\nHZ_SETUP_EOF\necho b\n";
  const d = heredocDelimiter(hostile);
  check(d !== "HZ_SETUP_EOF" && !hostile.split("\n").includes(d), "a script containing the delimiter gets a different one");
  check(setupInstallCommand(hostile).includes(`\n${hostile}${d}\n`), "…and is still carried whole");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} prom setup check(s) failed`);
}

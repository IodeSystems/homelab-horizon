/**
 * The Observability page's "Auto-update" command. It carries NO installer of
 * its own: it wraps the server's setup.sh (internal/server/handlers_integration.go,
 * promSetupScript) so the page and `curl setup.sh | sudo bash` install the same
 * thing — a systemd timer whose refresh validates with promtool before it
 * replaces hz.yml. A second copy here is how the unsafe cron refresher happened.
 */

/** Line that ends the quoted heredoc. Never a line of the script itself. */
export function heredocDelimiter(script: string): string {
  const lines = new Set(script.split("\n"));
  let d = "HZ_SETUP_EOF";
  while (lines.has(d)) d += "_";
  return d;
}

/**
 * A paste-able command: write setup.sh to a private temp file (mktemp is 0600;
 * the script contains the scrape token), run it with sudo, remove it.
 */
export function setupInstallCommand(script: string): string {
  const body = script.endsWith("\n") ? script.slice(0, -1) : script;
  const d = heredocDelimiter(body);
  return [
    `f="$(mktemp)"`,
    `cat > "$f" <<'${d}'`,
    body,
    d,
    `sudo bash "$f"; rc=$?; rm -f "$f"; [ "$rc" -eq 0 ] || echo "setup.sh failed (exit $rc)"`,
  ].join("\n");
}

package server

const checksTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>Service Checks - Homelab Horizon</title>
    <style>` + baseCSS + `
    .status-light { display: inline-block; width: 12px; height: 12px; border-radius: 50%; margin-right: 0.5rem; }
    .status-light.ok { background: #27ae60; box-shadow: 0 0 6px #27ae60; }
    .status-light.failed { background: #e74c3c; box-shadow: 0 0 6px #e74c3c; }
    .status-light.pending { background: #f39c12; box-shadow: 0 0 6px #f39c12; }
    .status-light.disabled { background: #666; }
    .check-row { display: flex; align-items: center; padding: 0.75rem; background: #0f3460; border-radius: 6px; margin-bottom: 0.5rem; }
    .check-info { flex: 1; }
    .check-name { font-weight: bold; margin-bottom: 0.25rem; }
    .check-target { color: #888; font-size: 0.9em; }
    .check-meta { color: #666; font-size: 0.85em; margin-top: 0.25rem; }
    .check-actions { display: flex; gap: 0.25rem; }
    .check-actions button { padding: 0.25rem 0.5rem; font-size: 0.85em; margin: 0; }
    .auto-badge { background: #3498db; color: white; font-size: 0.7em; padding: 0.15rem 0.4rem; border-radius: 3px; margin-left: 0.5rem; }
    </style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>Service Checks</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        {{if .Message}}<div class="success">{{.Message}}</div>{{end}}
        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        <div class="card">
            <h2>Notification Settings</h2>
            <form method="POST" action="/admin/checks/settings">
                <label>ntfy URL (leave blank to disable notifications)</label>
                <input type="text" name="ntfy_url" value="{{.Config.NtfyURL}}" placeholder="https://ntfy.sh/my-homelab-alerts">
                <p style="color: #888; font-size: 0.85em; margin-bottom: 1rem;">
                    When a check fails, a notification will be sent to this ntfy topic.
                    Get a free account at <a href="https://ntfy.sh" target="_blank">ntfy.sh</a> or self-host.
                </p>
                <button type="submit" class="secondary">Save Settings</button>
            </form>
        </div>

        <div class="card">
            <h2>Health Checks ({{len .Statuses}})</h2>
            <p style="color: #888; margin-bottom: 1rem;">
                Service checks monitor your infrastructure and notify you when services go down.
                Checks from HAProxy-proxied services are auto-included.
            </p>

            {{if .Statuses}}
            {{range .Statuses}}
            <div class="check-row">
                <span class="status-light {{.Status}}"></span>
                <div class="check-info">
                    <div class="check-name">
                        {{.Name}}
                        {{if .AutoGen}}<span class="auto-badge">auto</span>{{end}}
                    </div>
                    <div class="check-target"><code>{{.Type}}</code>: {{.Target}}</div>
                    <div class="check-meta">
                        Interval: {{.Interval}}s
                        {{if not .LastCheck.IsZero}} | Last: {{.LastCheck.Format "15:04:05"}}{{end}}
                        {{if .LastError}} | <span style="color: #e74c3c;">{{.LastError}}</span>{{end}}
                    </div>
                </div>
                <div class="check-actions">
                    <form method="POST" action="/admin/checks/run" style="display:inline">
                        <input type="hidden" name="name" value="{{.Name}}">
                        <button type="submit" class="secondary" title="Run now">Run</button>
                    </form>
                    <form method="POST" action="/admin/checks/toggle" style="display:inline">
                        <input type="hidden" name="name" value="{{.Name}}">
                        {{if .Enabled}}
                        <button type="submit" class="secondary" title="Disable">Disable</button>
                        {{else}}
                        <button type="submit" class="success" title="Enable">Enable</button>
                        {{end}}
                    </form>
                    {{if not .AutoGen}}
                    <form method="POST" action="/admin/checks/delete" style="display:inline" onsubmit="return confirm('Delete check {{.Name}}?')">
                        <input type="hidden" name="name" value="{{.Name}}">
                        <button type="submit" class="danger" title="Delete">Delete</button>
                    </form>
                    {{end}}
                </div>
            </div>
            {{end}}
            {{else}}
            <p>No health checks configured. Add a check below or configure services with HAProxy to auto-generate checks.</p>
            {{end}}
        </div>

        <div class="card">
            <h2>Add Check</h2>
            <form method="POST" action="/admin/checks/add">
                <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 0.5rem;">
                    <div>
                        <label>Name</label>
                        <input type="text" name="name" placeholder="my-server" required>
                    </div>
                    <div>
                        <label>Type</label>
                        <select name="type" required style="width: 100%; padding: 0.75rem; background: #0f3460; color: #fff; border: 1px solid #1a1a2e; border-radius: 4px;">
                            <option value="ping">Ping (TCP connect)</option>
                            <option value="http">HTTP (expects 200)</option>
                        </select>
                    </div>
                </div>
                <label>Target</label>
                <input type="text" name="target" placeholder="192.168.1.100 or http://service.local/health" required>
                <p style="color: #888; font-size: 0.85em; margin-bottom: 0.5rem;">
                    For ping: hostname or IP. For HTTP: full URL including protocol.
                </p>
                <div style="display: grid; grid-template-columns: 1fr auto; gap: 0.5rem; align-items: end;">
                    <div>
                        <label>Interval (seconds)</label>
                        <input type="number" name="interval" value="300" min="10" max="86400">
                    </div>
                    <button type="submit">Add Check</button>
                </div>
            </form>
        </div>
    </div>
    <script>
    // Auto-inject CSRF token into all POST forms
    (function() {
        var meta = document.querySelector('meta[name="csrf-token"]');
        var csrfToken = meta ? meta.getAttribute('content') : '';
        if (!csrfToken) {
            console.error('[CSRF] No CSRF token in meta tag!');
        }
        document.querySelectorAll('form[method="POST"]').forEach(function(form) {
            if (!form.querySelector('input[name="csrf_token"]')) {
                var input = document.createElement('input');
                input.type = 'hidden';
                input.name = 'csrf_token';
                input.value = csrfToken;
                form.appendChild(input);
            }
        });
    })();
    </script>
</body>
</html>`

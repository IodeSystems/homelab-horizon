package server

const networkTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>Network Map - Homelab Horizon</title>
    <style>` + baseCSS + `
    .host-card { background: #0f3460; border-radius: 8px; padding: 1rem; margin-bottom: 0.75rem; }
    .host-header { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.75rem; }
    .host-ip { font-family: monospace; font-size: 1.1rem; color: #3498db; font-weight: bold; }
    .port-count { color: #888; font-size: 0.85rem; }
    .port-row { display: flex; align-items: center; gap: 0.75rem; padding: 0.35rem 0; border-bottom: 1px solid #16213e; }
    .port-row:last-child { border-bottom: none; }
    .port-num { font-family: monospace; font-size: 0.95rem; color: #2ecc71; min-width: 5rem; }
    .port-proto { font-size: 0.75rem; color: #888; text-transform: uppercase; min-width: 2.5rem; }
    .port-service { color: #e94560; font-weight: 500; }
    .port-domain { color: #888; font-size: 0.85rem; }
    </style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>Network Map</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        <div class="card">
            <h2>Host Port Reservations</h2>
            <p style="color: #888; margin-bottom: 1rem;">Ports claimed on each host, derived from service configuration.</p>

            {{range $host, $ports := .HostPortMap.Hosts}}
            <div class="host-card">
                <div class="host-header">
                    <span class="host-ip">{{$host}}</span>
                    <span class="port-count">{{len $ports}} port{{if ne (len $ports) 1}}s{{end}}</span>
                </div>
                {{range $ports}}
                <div class="port-row">
                    <span class="port-num">:{{.Port}}</span>
                    <span class="port-proto">{{.Proto}}</span>
                    <span class="port-service">{{.Service}}</span>
                    {{if .Domain}}<span class="port-domain">{{.Domain}}</span>{{end}}
                </div>
                {{end}}
            </div>
            {{else}}
            <p>No port reservations found. Add services with proxy backends to see the network map.</p>
            {{end}}
        </div>
    </div>
</body>
</html>`

package config

// Port-allocation exclusions. hz's `ports next`/`ports list` (and any client that
// reads /api/v1/ports) must never hand out a port that a database, dev tool, or
// well-known service expects to own. The denylist has two layers:
//
//   - builtinPortRanges — a curated, server-side constant (single ports are a
//     range with From==To). Always applied, so a fresh config is still safe.
//   - Config.PortExclusions — operator additions, editable via the API/UI.
//
// Exclusions apply to NEW allocation only; a service already sitting on a now-
// excluded port is never evicted (callers flag it as grandfathered).

// PortRange is an inclusive [From, To] port span. A single port sets To==0 (or
// To==From). Note is an optional human label shown in the UI.
type PortRange struct {
	From int    `json:"from"`
	To   int    `json:"to,omitempty"`
	Note string `json:"note,omitempty"`
}

// Contains reports whether p falls in the range (single port when To<=From).
func (r PortRange) Contains(p int) bool {
	hi := r.To
	if hi < r.From {
		hi = r.From
	}
	return p >= r.From && p <= hi
}

// builtinPortRanges is the curated default denylist (migrated from the hz CLI so
// the server is the single source of truth). Grouped for readability.
var builtinPortRanges = []PortRange{
	// classic / system
	{From: 1, To: 1023, Note: "privileged / system ports"},
	{From: 3389, Note: "RDP"}, {From: 5900, Note: "VNC"},
	// databases
	{From: 1433, Note: "mssql"}, {From: 1521, Note: "oracle"},
	{From: 3306, Note: "mysql"}, {From: 5432, To: 5433, Note: "postgres"},
	{From: 6379, To: 6380, Note: "redis"}, {From: 8086, Note: "influxdb"},
	{From: 9042, Note: "cassandra"}, {From: 11211, Note: "memcached"},
	{From: 5984, Note: "couchdb"}, {From: 27017, To: 27019, Note: "mongodb"},
	// search / analytics / observability
	{From: 9200, Note: "elasticsearch"}, {From: 9300, Note: "elasticsearch-transport"},
	{From: 7700, Note: "meilisearch"}, {From: 5601, Note: "kibana"},
	{From: 3100, Note: "loki"}, {From: 9090, To: 9093, Note: "prometheus/alertmanager"},
	{From: 10250, Note: "kubelet"},
	// messaging / streaming
	{From: 1883, Note: "mqtt"}, {From: 2181, Note: "zookeeper"},
	{From: 4222, Note: "nats"}, {From: 5672, Note: "amqp"}, {From: 15672, Note: "rabbitmq-mgmt"},
	{From: 9092, Note: "kafka"}, {From: 61616, Note: "activemq"},
	// infra / orchestration
	{From: 2375, To: 2380, Note: "docker/etcd"}, {From: 6443, Note: "k8s-api"},
	{From: 8200, Note: "vault"}, {From: 8500, Note: "consul"},
	// web / app dev-server bands (the ones clients reach for by habit)
	{From: 3000, To: 3010, Note: "node/react dev"}, {From: 4000, To: 4010, Note: "dev servers"},
	{From: 4200, Note: "angular"}, {From: 5000, To: 5010, Note: "flask/dev"},
	{From: 5173, Note: "vite"}, {From: 4040, Note: "spark-ui"},
	{From: 8000, To: 8099, Note: "http dev servers (8xxx)"},
	{From: 8443, Note: "https-alt"}, {From: 8888, Note: "jupyter"},
	{From: 9000, To: 9099, Note: "php-fpm/misc (9xxx)"},
}

// portExcludedBuiltin reports whether p is in the built-in denylist.
func portExcludedBuiltin(p int) bool {
	for _, r := range builtinPortRanges {
		if r.Contains(p) {
			return true
		}
	}
	return false
}

// PortExcluded reports whether port p is denied for new allocation — either by
// the built-in denylist or the operator's configured PortExclusions.
func (c *Config) PortExcluded(p int) bool {
	if portExcludedBuiltin(p) {
		return true
	}
	for _, r := range c.PortExclusions {
		if r.Contains(p) {
			return true
		}
	}
	return false
}

// BuiltinPortExclusions returns a copy of the built-in denylist (for display).
func BuiltinPortExclusions() []PortRange {
	out := make([]PortRange, len(builtinPortRanges))
	copy(out, builtinPortRanges)
	return out
}

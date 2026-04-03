# Vanilla Ubuntu LTS — no pre-installed dependencies.
# The binary's auto_heal feature installs wireguard-tools, dnsmasq,
# haproxy, etc. on first startup, demonstrating self-configuration.
FROM ubuntu:24.04

COPY dist/homelab-horizon-linux-amd64 /usr/local/bin/homelab-horizon
RUN chmod +x /usr/local/bin/homelab-horizon

COPY docker/demo-config.json /etc/homelab-horizon/config.json

EXPOSE 8080

CMD ["/usr/local/bin/homelab-horizon"]

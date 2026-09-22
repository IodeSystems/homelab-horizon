# Vanilla Ubuntu LTS — no pre-installed dependencies.
#
# hz does NOT install them for you at boot. It used to, when `auto_heal` was
# set in the config; that key and that boot-time apt are gone
# (plan/privilege-classification.md §3.5), because a daemon that runs apt-get
# on a gateway at a moment nobody chose can restart something carrying traffic.
#
# So a container that needs wireguard-tools / dnsmasq / haproxy runs
# `homelab-horizon install-deps` first — see examples/docker-entrypoint.sh,
# which does exactly that before starting hz. This image's own CMD does not,
# deliberately: its only in-tree caller is bin/screenshots, which runs it
# hermetically with every daemon switched off and no outbound network, and an
# apt-get there would be both pointless and a lie about the container being
# offline.
FROM ubuntu:26.04

COPY dist/homelab-horizon-linux-amd64 /usr/local/bin/homelab-horizon
RUN chmod +x /usr/local/bin/homelab-horizon

COPY docker/demo-config.json /etc/homelab-horizon/config.json

EXPOSE 8080

CMD ["/usr/local/bin/homelab-horizon"]

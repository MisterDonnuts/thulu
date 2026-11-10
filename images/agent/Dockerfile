ARG BASE=ghcr.io/misterdonnuts/octopus-agent:latest
FROM ${BASE}

# No USER: the daemon always sets the run's user (internal/types/launch.go); HOME is root's for the
# root start.
ENV HOME=/root

ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/workspace/entrypoint.sh"]

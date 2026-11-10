#!/usr/bin/env bash
# Sourced by entrypoint-base.sh: must not exit or change shell options. Node ignores the system
# store, hence NODE_EXTRA_CA_CERTS.
_octopus_ca=/etc/octopus/ca.crt
if [ -s "$_octopus_ca" ]; then
  _octopus_system=/etc/ssl/certs/ca-certificates.crt
  _octopus_bundle=""
  if [ "$(id -u)" = "0" ] && command -v update-ca-certificates >/dev/null 2>&1 \
      && [ -d /usr/local/share/ca-certificates ]; then
    if cp "$_octopus_ca" /usr/local/share/ca-certificates/octopus-extra.crt \
        && update-ca-certificates >/dev/null 2>&1; then
      _octopus_bundle="$_octopus_system"
    fi
  fi
  if [ -z "$_octopus_bundle" ]; then
    _octopus_bundle="${TMPDIR:-/tmp}/octopus-ca-bundle.crt"
    {
      [ -f "$_octopus_system" ] && cat "$_octopus_system"
      cat "$_octopus_ca"
    } > "$_octopus_bundle" 2>/dev/null || _octopus_bundle="$_octopus_ca"
  fi
  export NODE_EXTRA_CA_CERTS="$_octopus_ca"
  export SSL_CERT_FILE="$_octopus_bundle"
  export REQUESTS_CA_BUNDLE="$_octopus_bundle"
  export CURL_CA_BUNDLE="$_octopus_bundle"
  export GIT_SSL_CAINFO="$_octopus_bundle"
  msg_en agent.log.ca_trusted bundle "$_octopus_bundle"
  unset _octopus_system _octopus_bundle
fi
unset _octopus_ca

#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} != 0 ]]; then
  printf '%s\n' 'Run with sudo on Ubuntu.' >&2
  exit 1
fi
base=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=/opt/backpack-router
state=/var/lib/backpack-router
etc=/etc/backpack-router
units=/etc/systemd/system
mode=${1:-router}
if [[ $# -gt 1 || ( $mode != router && $mode != --agent ) ]]; then
  printf '%s\n' 'Usage: sudo ./install.sh [--agent]' >&2
  exit 1
fi
if [[ ! -x "$base/backpack-router" ]]; then
  printf '%s\n' 'Run the installer from an extracted Linux release archive.' >&2
  exit 1
fi
version=$("$base/backpack-router" version)
if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]]; then
  printf '%s\n' 'Invalid release version.' >&2
  exit 1
fi

install -d -m 0755 "$root/releases" "$etc"
release="$root/releases/$version"
install -d -m 0755 "$release"
install -m 0755 "$base/backpack-router" "$release/backpack-router"
previous=$(readlink "$root/current" || true)
switch_release() {
  ln -sfn -- "$1" "$root/.current-next"
  mv -Tf -- "$root/.current-next" "$root/current"
}

if [[ $mode == --agent ]]; then
  id -u backpack-agent >/dev/null 2>&1 || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin backpack-agent
  install -m 0644 "$base/deploy/backpack-router-agent.service" "$units/"
  if [[ ! -f $etc/agent.env ]]; then
    install -m 0600 "$base/deploy/agent.env.example" "$etc/agent.env"
    switch_release "$release"
    systemctl daemon-reload
    printf '%s\n' 'Set the actual VM address, SOCKS endpoint and probe URL in /etc/backpack-router/agent.env.' +      'Then run: sudo systemctl enable --now backpack-router-agent'
    exit 0
  fi
  services=(backpack-router-agent.service)
else
  getent group backpack-router >/dev/null || groupadd --system backpack-router
  for name in backpack-engine backpack-panel; do
    id -u "$name" >/dev/null 2>&1 || useradd --system --gid backpack-router --no-create-home --shell /usr/sbin/nologin "$name"
  done
  install -d -m 0750 -o root -g backpack-router "$state"
  install -d -m 2750 -o backpack-panel -g backpack-router "$state/config"
  install -d -m 0700 -o backpack-panel -g backpack-router "$state/panel"
  install -d -m 0755 "$release/ui"
  cp -R -- "$base/ui/." "$release/ui/"
  if [[ ! -f $state/config/engine.json ]]; then
    install -m 0640 -o backpack-panel -g backpack-router "$base/deploy/engine.example.json" "$state/config/engine.json"
  fi
  "$release/backpack-router" check -config "$state/config/engine.json"
  if [[ ! -f $etc/panel.env ]]; then
    install -m 0600 "$base/deploy/panel.env.example" "$etc/panel.env"
  fi
  if [[ ! -f $etc/admin.hash && ! -f $state/panel/router.sqlite ]]; then
    umask 077
    password=$(openssl rand -hex 20)
    printf '%s\n' "$password" | "$release/backpack-router" hash-password > "$etc/admin.hash"
    chown root:backpack-router "$etc/admin.hash"
    chmod 0640 "$etc/admin.hash"
    printf 'Initial login: admin\nInitial password: %s\n' "$password"
    unset password
  fi
  install -m 0644 "$base/deploy/backpack-router-engine.service" "$base/deploy/backpack-router-panel.service" "$units/"
  services=(backpack-router-engine.service backpack-router-panel.service)
fi

switch_release "$release"
systemctl daemon-reload
systemctl enable "${services[@]}"
verify_services() {
  local service
  for service in "${services[@]}"; do
    systemctl is-active --quiet "$service" || return
  done
}
if ! systemctl restart "${services[@]}" || ! verify_services; then
  if [[ -n $previous && $previous != "$release" ]]; then
    switch_release "$previous"
    systemctl restart "${services[@]}"
  fi
  printf '%s\n' 'Check systemctl status and the service journal.' >&2
  exit 1
fi
systemctl status --no-pager --lines=0 "${services[@]}"

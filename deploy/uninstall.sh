#!/usr/bin/env bash
set -euo pipefail
[[ $EUID == 0 ]] || { printf '%s\n' 'Run with sudo.' >&2; exit 1; }
for service in backpack-router-kernel-metrics backpack-router-kernel-forward backpack-router-engine backpack-router-panel backpack-router-agent; do
  if [[ -e /etc/systemd/system/$service.service ]]; then
    systemctl disable --now "$service"
    rm -- "/etc/systemd/system/$service.service"
  fi
done
systemctl daemon-reload
printf '%s\n' 'Services removed. Configuration and releases are available under /etc/backpack-router, /var/lib/backpack-router and /opt/backpack-router.'

#!/usr/bin/env bash
set -euo pipefail

repository=https://github.com/dr-hoseyn/backpack-smart-router
arch=$(dpkg --print-architecture)
case "$arch" in
  amd64|arm64) ;;
  *) printf 'Unsupported Ubuntu architecture: %s\n' "$arch" >&2; exit 1 ;;
esac

for tool in curl openssl sha256sum tar; do
  if ! command -v "$tool" >/dev/null; then
    printf 'Missing command: %s. Install it and rerun this command.\n' "$tool" >&2
    exit 1
  fi
done

version=${BACKPACK_ROUTER_VERSION:-}
if [[ -z $version ]]; then
  latest=$(curl --fail --silent --show-error --head --location --max-time 15 \
    --proto '=https' --proto-redir '=https' --output /dev/null --write-out '%{url_effective}' \
    "$repository/releases/latest")
  version=${latest#"$repository/releases/tag/"}
fi
if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]]; then
  printf '%s\n' 'Invalid BackPack Router release version.' >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cd "$work"
package="backpack-smart-router-$version-linux-$arch"
archive="$package.tar.gz"
release_url="$repository/releases/download/$version"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --output "$archive" "$release_url/$archive"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --output SHA256SUMS "$release_url/SHA256SUMS"
awk -v archive="./$archive" '$2 == archive { print; found++ } END { if (found != 1) exit 1 }' SHA256SUMS > expected.sha256
sha256sum --check --strict expected.sha256

# Keep the working directory private and extract files with their package permissions.
tar --no-same-owner --same-permissions -xzf "$archive"
cd "$package"
install -m 0600 /dev/null "$work/install.log"
installer=(./install.sh)
if [[ $EUID != 0 ]]; then
  installer=(sudo "${installer[@]}")
fi

status=0
"${installer[@]}" "$@" > "$work/install.log" || status=$?
cat "$work/install.log"
if (( status != 0 )); then
  exit "$status"
fi
if [[ ${1:-} == --agent ]] || grep -Eq '^(Default web panel|Public HTTPS web panel): ' "$work/install.log"; then
  exit 0
fi
printf '\nDefault web panel: http://127.0.0.1:9080/\n'
if systemctl is-active --quiet backpack-router-public.socket; then
  public_listener=$(systemctl show --property=Listen --value backpack-router-public.socket) || public_listener=
  public_listener=${public_listener%% *}
  if [[ $public_listener =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}:[0-9]{1,5}$ ]]; then
    printf 'Public HTTPS web panel: https://%s/\n' "$public_listener"
  fi
fi

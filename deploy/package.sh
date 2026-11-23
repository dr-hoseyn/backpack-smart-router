#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
version=${1:?version required}
arch=${2:?architecture required}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]]
[[ $arch == amd64 || $arch == arm64 ]]
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
name="backpack-smart-router-$version-linux-$arch"
install -d "$work/$name/ui" "$work/$name/deploy" bin
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$work/$name/backpack-router" ./cmd/backpack-router
install -m 0755 install.sh "$work/$name/"
install -m 0755 deploy/uninstall.sh "$work/$name/deploy/"
install -m 0755 deploy/kernel-forward.py "$work/$name/deploy/"
install -m 0644 deploy/kernel-setup.py "$work/$name/deploy/"
install -m 0644 deploy/*.service deploy/*.example deploy/engine.example.json "$work/$name/deploy/"
install -m 0644 README.md README.fa.md "$work/$name/"
cp -R ui/dist/. "$work/$name/ui/"
licenses="$work/$name/third-party-licenses"
install -d "$licenses"
install -m 0644 "$(go env GOROOT)/LICENSE" "$licenses/go-LICENSE"
dependencies=$(CGO_ENABLED=0 GOOS=linux GOARCH=$arch go list -deps -f '{{with .Module}}{{.Path}}{{"\t"}}{{.Dir}}{{end}}' ./cmd/backpack-router)
while IFS=$'\t' read -r module directory; do
  [[ -n $module && $module != github.com/dr-hoseyn/backpack-smart-router ]] || continue
  slug=${module//\//_}
  for notice in "$directory"/{LICENSE*,NOTICE*,COPYING*}; do
    [[ -f $notice ]] || continue
    install -m 0644 "$notice" "$licenses/$slug-${notice##*/}"
  done
done < <(printf '%s\n' "$dependencies" | sort -u)
for package in react react-dom scheduler @fontsource-variable/vazirmatn; do
  slug=${package//\//_}
  for notice in "ui/node_modules/$package"/{LICENSE*,NOTICE*,OFL*}; do
    [[ -f $notice ]] || continue
    install -m 0644 "$notice" "$licenses/$slug-${notice##*/}"
  done
done
tar -czf "bin/$name.tar.gz" -C "$work" "$name"

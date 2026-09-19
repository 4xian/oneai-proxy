#!/usr/bin/env sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
release_version=${ONEAI_PROXY_VERSION:-0.1.0}
release_root=${ONEAI_PROXY_RELEASE_DIR:-"$project_dir/dist/releases/$release_version"}

cd "$project_dir"
npm ci
npm run build

mkdir -p "$release_root"

checksum_command=""
if command -v sha256sum >/dev/null 2>&1; then
  checksum_command=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  checksum_command="shasum -a 256"
else
  printf '%s\n' '未找到 sha256sum 或 shasum，无法生成校验和' >&2
  exit 1
fi

for target in \
  "windows amd64 exe" \
  "windows arm64 exe" \
  "darwin amd64 bin" \
  "darwin arm64 bin" \
  "linux amd64 bin" \
  "linux arm64 bin"
do
  set -- $target
  goos=$1
  goarch=$2
  suffix=$3
  package_name="oneai-proxy_${release_version}_${goos}_${goarch}"
  staging_dir=$(mktemp -d "${TMPDIR:-/tmp}/oneai-proxy-release.XXXXXX")
  binary_name=oneai-proxy
  if [ "$suffix" = exe ]; then
    binary_name=oneai-proxy.exe
  fi
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "-s -w" -o "$staging_dir/$binary_name" ./cmd/oneai-proxy
  cp plans/oneai-proxy-solution-plan.md "$staging_dir/运行说明-计划.md"
  cp docs/local-client-integration.md "$staging_dir/本地客户端接入.md"
  archive_path="$release_root/$package_name.tar.gz"
  tar -czf "$archive_path" -C "$staging_dir" "$binary_name" "运行说明-计划.md" "本地客户端接入.md"
  rm -rf "$staging_dir"
done

cd "$release_root"
eval "$checksum_command" oneai-proxy_*.tar.gz > SHA256SUMS
printf '发布包已生成: %s\n' "$release_root"

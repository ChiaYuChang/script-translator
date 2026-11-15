#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <version-tag> [dist-dir]" >&2
  exit 1
fi

version="$1"
dist_dir="${2:-dist}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must match vX.Y.Z, got: $version" >&2
  exit 1
fi

python3 - "$version" "$dist_dir" flake.nix <<'PY'
import base64
import hashlib
import pathlib
import re
import sys

version, dist_dir, flake_path = sys.argv[1], pathlib.Path(sys.argv[2]), pathlib.Path(sys.argv[3])
assets = [
    "script-translator-linux-amd64",
    "script-translator-linux-arm64",
    "script-translator-darwin-arm64",
]

hashes = {}
for asset in assets:
    asset_path = dist_dir / asset
    if not asset_path.is_file():
        raise SystemExit(f"missing release binary: {asset_path}")
    digest = hashlib.sha256(asset_path.read_bytes()).digest()
    hashes[asset] = "sha256-" + base64.b64encode(digest).decode("ascii")

text = flake_path.read_text(encoding="utf-8")

text, count = re.subn(
    r'(version\s*=\s*")v[^"]*(";)',
    rf'\g<1>{version}\g<2>',
    text,
    count=1,
)
if count != 1:
    raise SystemExit("failed to update version in flake.nix")

for asset, sri_hash in hashes.items():
    pattern = rf'(asset\s*=\s*"{re.escape(asset)}";\s*\n\s*hash\s*=\s*")[^"]*(";)'
    text, count = re.subn(pattern, rf'\g<1>{sri_hash}\g<2>', text, count=1)
    if count != 1:
        raise SystemExit(f"failed to update hash for {asset} in flake.nix")

flake_path.write_text(text, encoding="utf-8")
PY

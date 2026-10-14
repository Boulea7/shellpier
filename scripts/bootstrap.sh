#!/bin/sh
# Download and extract a trusted explicit archive after SHA-256 verification.
set -eu
if [ "$#" -ne 3 ]; then
  echo 'Usage: bootstrap.sh HTTPS_ARCHIVE_URL SHA256 NEW_DESTINATION' >&2
  exit 2
fi
url=$1
expected=$2
destination=$3
case "$url" in https://*) ;; *) echo 'HTTPS archive URL required' >&2; exit 2;; esac
case "$expected" in *[!a-fA-F0-9]*|'') echo 'Invalid SHA-256' >&2; exit 2;; esac
[ "${#expected}" -eq 64 ] || { echo 'SHA-256 must have 64 characters' >&2; exit 2; }
[ ! -e "$destination" ] || { echo 'Destination exists; choose a new path' >&2; exit 2; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl --fail --location --proto '=https' --proto-redir '=https' --output "$work/release.zip" "$url"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work/release.zip" | cut -d ' ' -f 1)
else
  actual=$(shasum -a 256 "$work/release.zip" | cut -d ' ' -f 1)
fi
expected=$(printf '%s' "$expected" | tr 'ABCDEF' 'abcdef')
[ "$actual" = "$expected" ] || { echo 'SHA-256 mismatch; archive not extracted' >&2; exit 1; }
mkdir -p "$destination"
unzip -q "$work/release.zip" -d "$destination"
chmod u+x "$destination/shellpier"
echo "Download verified and extracted: $destination"

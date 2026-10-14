#!/bin/sh
# Download an explicit HTTPS archive without running downloaded programs.
set -eu
if [ "$#" -ne 2 ]; then
  echo 'Usage: bootstrap.sh HTTPS_ARCHIVE_URL NEW_DESTINATION' >&2
  exit 2
fi
url=$1
destination=$2
case "$url" in https://*) ;; *) echo 'HTTPS archive URL required' >&2; exit 2;; esac
[ ! -e "$destination" ] || { echo 'Destination exists; choose a new path' >&2; exit 2; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl --fail --location --max-redirs 5 --proto '=https' --proto-redir '=https' --output "$work/release.zip" "$url"
mkdir -p "$destination"
unzip -q "$work/release.zip" -d "$destination"
chmod u+x "$destination/shellpier"
echo "Downloaded and extracted: $destination"

#!/bin/sh
set -eu
cd "$(dirname "$0")"
echo 'Temporary foreground access; Ctrl+C stops it. Configure your approved relay environment if needed.'
ticket="./access-$(date +%Y%m%d-%H%M%S)-$$.json"
printf 'Private access file: %s\n' "$ticket"
exec ./shellpier share --keep-awake --output "$ticket"

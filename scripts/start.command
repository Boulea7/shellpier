#!/bin/sh
set -eu
cd "$(dirname "$0")"
echo 'Temporary foreground access; Ctrl+C stops it. Configure your approved relay environment if needed.'
exec ./shellpier share --keep-awake --output ./access.json

#!/usr/bin/env sh
set -eu

# npm dependencies can contain Go source without their own module boundary.
packages=$(go list "$@" ./...)
printf '%s\n' "$packages" | awk '$0 !~ /\/node_modules\//'

#!/bin/sh
set -eu

# Preserve values.yaml's comments and spacing outside the generated scalar.
generated=$(mktemp)
trap 'rm -f "$generated"' EXIT
awk '
  FNR == NR {
    if ($0 == "[tools]") tools = 1
    if (tools) config = config ($0 == "" ? "" : "      " $0) "\n"
    next
  }
  $0 == "  mise:" { mise = 1 }
  mise && /^    config:/ {
    printf "    config: |\n%s", config
    skip = 1
    next
  }
  skip && (/^      / || /^$/) { next }
  skip { skip = 0; mise = 0 }
  { print }
' build/tools/mise.toml charts/kritika/values.yaml > "$generated"
cat "$generated" > charts/kritika/values.yaml

helm-docs --chart-search-root=charts --log-level=warn
helm-schema --chart-search-root charts/kritika --skip-auto-generation required,additionalProperties --append-newline

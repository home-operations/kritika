#!/bin/sh
set -eu

helm-docs --chart-search-root=charts --log-level=warn
helm-schema --chart-search-root charts/kritika --skip-auto-generation required,additionalProperties --append-newline

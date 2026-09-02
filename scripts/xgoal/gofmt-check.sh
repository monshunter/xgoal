#!/bin/sh
set -eu

unformatted=$(gofmt -l ./cmd ./internal)
if [ -n "$unformatted" ]; then
  echo "gofmt required for:" >&2
  echo "$unformatted" >&2
  exit 1
fi

#!/bin/sh
# Turns `go test -json` output into the README's "tests" badge: a shields.io
# endpoint document (https://shields.io/badges/endpoint-badge).
#
#   go test -json ./... > results.json
#   scripts/render-test-badge.sh results.json > tests.json
#
# Every test and subtest counts once. The percentage is passed over passed
# plus failed; skipped tests are named in the message rather than hidden,
# because a suite that skips its slow half is not the suite passing. A
# package that fails to build turns the badge red even with no test failed.
set -eu

results="${1:?usage: render-test-badge.sh <go test -json output>}"

jq -s '
  [ .[] | select(.Test != null and (.Action == "pass" or .Action == "fail" or .Action == "skip")) ] as $tests
  | ($tests | map(select(.Action == "pass")) | length) as $pass
  | ($tests | map(select(.Action == "fail")) | length) as $fail
  | ($tests | map(select(.Action == "skip")) | length) as $skip
  | ([ .[] | select(.Action == "build-fail" or (.Test == null and .Action == "fail")) ] | length) as $broken
  | ($pass + $fail) as $ran
  | {
      schemaVersion: 1,
      label: "tests",
      message: (
        if $ran == 0 then "no tests ran"
        else "\(($pass * 100 / $ran) | floor)% passing · \($pass)/\($ran)"
             + (if $skip > 0 then ", \($skip) skipped" else "" end)
        end),
      color: (
        if $fail > 0 or $broken > 0 or $ran == 0 then "red"
        elif $skip > 0 then "yellow"
        else "brightgreen"
        end)
    }
' "$results"

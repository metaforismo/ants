#!/bin/sh
# Test suite for the demo calculator. Exits non-zero while required features
# are missing or wrong; exits zero when the integrated tree is complete.
set -u
here=$(cd "$(dirname "$0")/.." && pwd)
failures=0

assert_output() {
  desc=$1
  expected=$2
  shift 2
  actual=$(sh "$here/calc.sh" "$@" 2>/dev/null)
  if [ "$actual" != "$expected" ]; then
    echo "FAIL: $desc (expected '$expected', got '$actual')"
    failures=$((failures + 1))
  else
    echo "PASS: $desc"
  fi
}

assert_output "add 2+3=5"          "5"  add 2 3
assert_output "add 10+32=42"       "42" add 10 32
assert_output "multiply 3*4=12"    "12" multiply 3 4
assert_output "multiply 6*7=42"    "42" multiply 6 7

if [ "$failures" -gt 0 ]; then
  echo "$failures test(s) failed"
  exit 1
fi
echo "all tests passed"
exit 0

#!/bin/sh
# Demo calculator: sources every lib_*.sh next to it so features implemented
# on separate branches integrate without touching this file.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
for lib in "$here"/lib_*.sh; do
  if [ -f "$lib" ]; then
    # shellcheck disable=SC1090
    . "$lib"
  fi
done

if [ "$#" -ne 3 ]; then
  echo "usage: calc.sh <operation> <a> <b>" >&2
  exit 2
fi

op=$1
a=$2
b=$3

case "$op" in
  add)
    add "$a" "$b"
    ;;
  multiply)
    multiply "$a" "$b"
    ;;
  *)
    echo "unknown operation: $op" >&2
    exit 2
    ;;
esac

#!/bin/sh
# Exhaustive check of the lifecycle presentation functions and the two
# components that paint success. See .cursor/skills/verify-ui-presentation.
set -eu
cd "$(dirname "$0")/../.."
root=$(pwd)
web="$root/web"

if [ ! -x "$web/node_modules/.bin/vitest" ]; then
  if ! pnpm -v >/dev/null 2>&1; then
    echo "pnpm does not run. Install pnpm 10, then run pnpm install --frozen-lockfile in web/." >&2
    exit 2
  fi
  (cd "$web" && pnpm install --frozen-lockfile)
fi

ver=$(node -p "require('$web/node_modules/vitest/package.json').version")
if [ "$ver" != "5.0.1" ]; then
  echo "vitest is $ver; this check expects 5.0.1 from web/pnpm-lock.yaml" >&2
  exit 2
fi

echo "node $(node -v)"
echo "vitest $ver"
cd "$web"
./node_modules/.bin/vitest run tests/ui-presentation.test.ts --reporter=verbose

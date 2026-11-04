#!/usr/bin/env bash
# Guards the CI trigger setup: conflicting PRs get no pull_request run, so CI
# must also run on pushes to board/** (see docs/ci.md).
set -euo pipefail
cd "$(dirname "$0")/../.github/workflows"
fail=0
check() { # file pattern message
  grep -Eq -- "$2" "$1" || { echo "FAIL $1: $3"; fail=1; }
}
check ci.yml '^ +- "board/\*\*"' 'push trigger for board/**'
check ci.yml '^concurrency:' 'concurrency group'
check ci.yml 'cancel-in-progress: .*github\.ref != .refs/heads/main' 'cancel-in-progress must exclude main'
check docker.yml '^ +- "board/\*\*"' 'push trigger for board/**'
check docker.yml '^concurrency:' 'concurrency group'
exit $fail

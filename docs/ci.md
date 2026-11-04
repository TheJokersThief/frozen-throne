# CI

GitHub does not run `pull_request` workflows on a PR that has merge conflicts,
because there is no merge commit to test. A conflicting PR would therefore get
no checks at all. To avoid that, the CI and Docker workflows also run on `push`
to `board/**` branches, so the head commit of a conflicting PR still gets real
check results. A concurrency group per workflow and ref cancels superseded runs
on non-main branches, which also collapses the duplicate push and
`pull_request` runs on a non-conflicting PR. Job names are unchanged, so
required checks keep working.

`scripts/check-workflows.sh` (run by `just lint-workflows`) guards this setup.

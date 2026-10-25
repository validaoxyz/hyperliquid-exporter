# Releases

Write the version's entry in `CHANGELOG.md` before creating its tag. The release workflow
copies that section into the GitHub release body. It refuses a missing, duplicate or empty
section.

Release notes describe what changes for someone running the exporter: fixes, new behavior,
breaking changes, upgrade steps and specific security changes. Keep them short and concrete.
Use absolute links so they also work in the GitHub release body.

Keep test results, vulnerability-scan outcomes, review status, deployment checks and authoring
process out of release notes and changelog entries. Those belong in CI or internal records.
Describe a security fix and any required action without adding blanket claims about safety.

Preview the exact body before tagging:

```sh
python3 .github/scripts/release_notes.py vX.Y.Z --output /tmp/release-notes.md
cat /tmp/release-notes.md
```

Review that body for its audience as well as accuracy. When correcting a published release,
keep its body consistent with the changelog.

The release workflow disables Python bytecode and requires a clean checkout immediately before
building binaries. Staged changes, unstaged changes and untracked files fail that check. Use
`PYTHONDONTWRITEBYTECODE=1` for local release preparation too.

For byte-for-byte release checks, use a fresh standalone clone with the same history depth and
tags as the workflow checkout. The current workflow uses depth one. A full clone can change
Go's embedded module version because it contains historical tags, even at the same commit.

```sh
git clone --depth 1 --single-branch --branch vX.Y.Z \
  https://github.com/validaoxyz/hyperliquid-exporter.git /path/to/fresh-clone
```

Set `GOENV=off` to exclude persisted local Go settings. Match the workflow's Go version,
environment and build flags, and write comparison binaries outside the source checkout.
Compare the raw binaries; archive timestamps can differ.

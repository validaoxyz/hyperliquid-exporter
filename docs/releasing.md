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

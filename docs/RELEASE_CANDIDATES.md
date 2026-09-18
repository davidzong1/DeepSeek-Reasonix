# Release candidates

The protected `main-v2` control workflow prepares an immutable candidate after
reviewed Stable Notes are embedded. `Prepare release candidate` with `version`
builds the shared CLI/npm files, signs Desktop files, runs final-package native
acceptance, and seals a record. `Publish release candidate` accepts only that
record, checks its provenance and bytes, and requires one `release` approval
before creating the three tags and publishing. `recover` reuses the same sealed
files; it must not rebuild or sign them.

For qualification without publication, dispatch `Prepare release candidate` on
protected `main-v2` with `version` and `rehearsal=true`. An already reviewed
version may be used for this isolated run. It uses separate
`release-candidate-rehearsal-*` artifacts, records `purpose=rehearsal`, and
cannot pass the normal publish resolver or payload verifier. The Desktop child
accepts the existing version tag only in this non-publishing mode. The run must
still complete source CI, signing, and native acceptance. It creates no tags,
GitHub Releases, npm packages, Homebrew updates, R2 pointers, or site changes.

After sealing, run `Verify release candidate rehearsal` on `main-v2` with its
candidate ID. This independent workflow downloads the exact record and payload
artifact IDs. It checks the GitHub archive digest, protected producer run,
OIDC file attestations, sealed file digests, and native acceptance receipts.
Its 90-day report binds the producer and verifier runs without compiler or
signing credentials. The verifier proves reuse of the same signed bytes; it is
not a publication or a substitute for a later formal release's public checks.

The candidate payload lasts 30 days and its record/evidence 90 days. If the
payload expires before publication, prepare a new candidate. The release
skill's public postflight remains the authority for tags, npm, Desktop
updates, Homebrew, and the hydrated website after an authorized publication.

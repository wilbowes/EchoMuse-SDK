# CLAUDE.md

Rules for assistant sessions in this repository. The engineering rules apply
to everyone. If you are working for a contributor, post as that person, never
sign as the project, and leave this file out of your pull request; the
maintainers update it after a merge.

## What this is

The EchoMuse SDK: the device↔controller protocol spec, a conformance suite,
and reference client libraries. The main repo is
https://github.com/wilbowes/EchoMuse; its
`docs/device-controller-interface.md` is the wire contract and
`docs/listening.md` is the privacy spec. If this repo and the main repo
disagree, the main repo's controller is what clients meet in the field, so
fix the spec here and raise the gap there.

## Rules

- **Spec, then test, then code.** Every rule in the spec has a conformance test
  written from its edges and, where possible, run against the real controller.
- **Capabilities, never version strings.** Unknown fields and messages are
  ignored. A missing measurement is null, never 0.
- **Listening is private by default, and the client enforces it**: no audio
  before its own wake word or a button, stop at end of speech, its own
  timeouts, never a fallback to streaming, and listening state reported from
  what it is doing.
- **No Amazon code or blobs**, and nothing that makes them necessary.
- **Never interpolate user text into a shell command.**
- **Pipeline hygiene**, enforced by `tools/check_workflows.py`: actions by SHA,
  runners by release, `permissions: contents: read` at the top of every
  workflow, no `pull_request_target`, downloads checked by sha256.
- **Annotated tags** with `--cleanup=verbatim`. Release workflows refuse a tag
  that is not a version.
- **Merge only on green.** Wait for every check; a successful merge command
  proves nothing.

# Contributing

## Ground rules

- **The spec comes first.** A behaviour goes into the spec before it goes into
  a library, and every rule in the spec gets a conformance test written from
  its edges: minimum and maximum lengths, every permitted character class,
  forbidden forms, and published test vectors where they exist.
- **Negotiate by capability, never by version string.** Any client can meet
  any controller version. A client announces what it can do; a missing
  capability disables a feature with a reason, and never gives a wrong answer.
- **Adding a message is safe. Moving one never is**: the old side silently
  drops the new path.
- **The listening rules in the README do not bend**, for any client, at any
  setting.
- **No Amazon code, and nothing that needs it.** No vendor blobs, firmware
  dumps or coefficient tables, and no dependency on Amazon's software or
  services.
- **Never interpolate user-supplied text into a shell command.**

## Pull requests

- One change per pull request, with a short description that says what it does
  and why.
- Every check must be green before merge.
- Two open pull requests per person at a time.

## AI-assisted contributions

These are welcome. Most of EchoMuse was written with Claude, and the commit
trailers say so. We ask for a person behind each change:

- **Start from a need**: a client you are building, a fault you hit, or an
  issue you read.
- **Understand what you are sending and why.** You do not need to account for
  every line.
- **No unattended agents.** Pull requests, issues and comments posted without
  a person reading them first will be closed.

If you are an assistant working for a contributor, post as that person and
leave `CLAUDE.md` out of the pull request.

## CI rules

The checks in `tools/check_workflows.py` enforce these:

- Actions are pinned by full commit SHA, with the version in a comment.
- Runners name a release (`ubuntu-24.04`), never `-latest`. A floating label
  broke EchoMuse's CI on every branch when GitHub moved it.
- Every workflow sets `permissions: contents: read` at the top, and a job
  asks for more only when it needs it.
- `pull_request_target` is not used.
- A downloaded tool is checked against a pinned sha256. A container image is
  pinned by digest.

Dependabot proposes updates to the pinned actions once a week.

## Releases

Tags are annotated (`git tag -a --cleanup=verbatim`), because the default
cleanup deletes Markdown `#` headings from the notes. A release workflow must
refuse a tag that is not a version. Release workflows are added once there is
something to publish.

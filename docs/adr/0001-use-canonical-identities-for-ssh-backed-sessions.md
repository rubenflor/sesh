---
status: accepted
date: 2026-10-04
decision-makers: "Feature owner"
consulted: "Repository architecture exploration"
informed: "Future contributors and implementation agents"
---

# Use canonical identities for SSH-backed sessions

## Context and Problem Statement

Sesh integrations display a listed session's `Name` and pass that same value
back to `sesh connect`. The Raycast Sesh extension follows this contract. A
remote tmux session's display name is not unique: several configured SSH hosts
may each have a session named `0`. Sesh needs one stable public value that
identifies the source, host alias, and remote tmux session across listing,
JSON, picker selection, integrations, and connection resolution.

How can Sesh identify an SSH-backed remote tmux session without requiring
existing callers to learn a second identity field or breaking local session
behavior when SSH is not configured?

## Decision Drivers

* Keep existing integrations working by passing `Session.Name` unchanged to
  `sesh connect`.
* Distinguish sessions with the same tmux name on different hosts.
* Preserve valid host aliases and tmux session names, including names that
  contain `/` or `%`.
* Ensure the identity can be parsed reversibly and unambiguously.
* Leave local session rows and behavior unchanged when SSH is not configured.

## Considered Options

* Use the remote tmux session name as `Name` and resolve it by display name.
* Add a separate public identity field while keeping `Name` as a display name.
* Use a canonical SSH identity in the existing `Name` field.

## Decision Outcome

Chosen option: **Use a canonical SSH identity in the existing `Name` field.**

Every SSH-backed session has `Src = "ssh"` and a canonical `Name` with this
form:

```text
ssh/<encoded-host>/<encoded-session>
```

Encode each component independently by first converting its text to UTF-8
bytes. Leave only the RFC 3986 unreserved ASCII characters unescaped:
letters, digits, `-`, `.`, `_`, and `~`. Percent-encode every other byte using
`%` followed by two uppercase hexadecimal digits. In particular, `/` is `%2F`
and `%` is `%25`. [RFC 3986](https://www.rfc-editor.org/rfc/r3986.html)

To parse the identity, split on its literal `/` separators before decoding the
host and session components. Decode percent triplets exactly once; reject
malformed triplets and decoded byte sequences that are not valid UTF-8. Never
use the decoded session name as a substitute for the canonical identity when
resolving a connection.

`Name` is the public identity and handoff value from `sesh list` through JSON,
the picker or an integration, and back to `sesh connect`. Do not add a separate
public identity field in v1. A remote row may therefore display a name such as
`ssh/myinventa/0`; that is an accepted compatibility tradeoff. Existing local
rows keep their current fields and semantics. Without SSH configuration, JSON
output and local listing and connection behavior remain unchanged.

The connector must recognize valid canonical SSH identities before alias,
pane-format, directory, or other generic resolution. A malformed value in the
`ssh/` identity namespace must return a useful error rather than falling
through to another resolver.

Using only a remote display name is ambiguous across hosts. A separate identity
field would require existing integrations that currently pass `Name` to learn
and use that field. The canonical `Name` contract preserves their handoff
unchanged.

### Consequences

* Existing integrations can pass the selected `Name` unchanged to
  `sesh connect` and require no identity-specific update.
* Remote session names are globally distinguishable across configured hosts,
  including names containing delimiter characters.
* The canonical identity is less friendly as a display title. A separate
  display-name concept can be considered later without changing this identity.
* Every boundary that carries a session name must preserve the canonical string
  verbatim; only the SSH resolver decodes its components.
* When SSH is not configured, local output and connection behavior remain
  compatible.

## Implementation Plan

* **Affected paths**: `model/sesh_session.go` for the existing `Src` and
  `Name` fields; `lister/list.go` and `lister/srcs.go` for source aggregation
  and source filtering; `seshcli/list.go` and `seshcli/picker.go` for the
  existing list and picker handoff; `connector/connect.go` for resolution and
  dispatch. Add focused tests alongside the affected packages for encoding,
  parsing, listing, JSON, picker handoff, and connection resolution.
* **Dependencies**: use the Go standard library for UTF-8 validation and
  percent-byte handling; do not introduce a new public identity field in v1.
* **Patterns to follow**: keep `Src` as the source discriminator in
  `model.SeshSession`; preserve the selected `Name` through the existing
  `NewListCommand`, `NewPickerCommand`, and `RealConnector.Connect` paths.
* **Patterns to avoid**: do not resolve SSH sessions by remote display name;
  do not decode before splitting the canonical identity; do not let malformed
  `ssh/` values fall through to generic resolvers; do not rewrite local rows or
  output when SSH is absent.
* **Scope boundary**: bridge implementation and lifecycle, host concurrency,
  discovery deadlines, cache behavior, and partial-failure policy are specified
  separately and are not part of this identity decision.

### Verification

- [ ] Each SSH row has `Src = "ssh"` and `Name` in canonical
  `ssh/<encoded-host>/<encoded-session>` form.
- [ ] Component encoding round-trips valid UTF-8, escapes `/` as `%2F` and `%`
  as `%25`, and does not leave component delimiters ambiguous.
- [ ] Parsing splits before decoding and rejects malformed percent triplets or
  invalid UTF-8.
- [ ] Listing, JSON, picker selection, and `sesh connect` preserve and consume
  the same canonical `Name` unchanged.
- [ ] Valid SSH identities are resolved before slash-shaped pane/path
  strategies; malformed `ssh/` values fail clearly without fallback.
- [ ] With SSH absent from configuration, local listing rows, JSON fields, and
  connection behavior remain unchanged.

## More Information

The `Name` handoff contract is used by existing callers such as the Raycast
Sesh extension, which displays `Session.Name` and returns it to `sesh connect`.
The remote connection mechanism is recorded separately in
[`0002-use-local-tmux-bridges-for-ssh-backed-sessions.md`](0002-use-local-tmux-bridges-for-ssh-backed-sessions.md).

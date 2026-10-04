---
status: accepted
date: 2026-10-04
decision-makers: "Feature owner"
consulted: "Repository architecture exploration"
informed: "Future contributors and implementation agents"
---

# Use internal local tmux bridges for SSH-backed sessions

## Context and Problem Statement

Sesh is adding remote tmux sessions discovered through configured OpenSSH host
aliases. Existing callers select a session by its `SeshSession.Name` and pass
that value to `sesh connect --switch`. The Raycast Sesh extension is an
important example: it lists JSON sessions and returns the selected `Name` to
that command. A local tmux client can switch only among sessions on its local
tmux server, so it cannot switch directly to a remote tmux session.

How can Sesh connect to a remote tmux session while preserving the existing
session-name handoff and local `--switch` behavior?

## Decision Drivers

* Keep existing callers, including Raycast, working without caller changes.
* Keep one public SSH identity from listing through picker selection to connect.
* Preserve Sesh's local tmux attach and switch behavior.
* Keep implementation-owned bridge sessions out of user-facing Sesh results.
* Leave authentication, host resolution, and SSH configuration to OpenSSH.

## Considered Options

* Use an internal local tmux bridge for every SSH connection.
* Attach directly to remote tmux, requiring `--switch` callers to change.
* Use a bridge only for `--switch` and direct SSH for normal connect.
* Represent remote sessions as generated local config sessions.

## Decision Outcome

Chosen option: **Use an internal local tmux bridge for every SSH connection.**

An SSH session remains a first-class Sesh session with `Src = "ssh"` and a
canonical `Name` of the form `ssh/<encoded-host>/<encoded-session>`. The
connector recognizes that identity and translates it internally to an
implementation-owned local tmux bridge. The bridge pane runs the interactive
OpenSSH connection and attaches to the named remote tmux session. Sesh applies
its existing local tmux switch-or-attach behavior to the bridge.

The bridge is an internal implementation detail. It must not replace the
canonical SSH `Name` returned by listing or the picker, and it must not appear
as a normal local tmux session in Sesh listings, JSON output, or picker results.
The connector must still be able to find and reuse it.

Direct remote attach would not give a local tmux client a session to switch to
and would require existing `--switch` callers to change. A split model would
give normal connect and `--switch` different session behavior. Generated config
sessions do not represent SSH discovery as a first-class source and would
conflate remote targets with local session startup configuration.

### Consequences

* Existing integrations can continue passing the selected canonical `Name` to
  `sesh connect --switch` unchanged.
* Normal connect and `--switch` use the same bridge; local tmux decides whether
  to attach the current terminal or switch a local client according to Sesh's
  established behavior.
* The user experience includes nested tmux: local tmux contains an SSH pane,
  which attaches to remote tmux. Users may need to send the remote tmux prefix
  through the local tmux binding.
* The local tmux server contains implementation-owned sessions. Sesh must tag,
  reuse, clean up, and hide those sessions without mistaking user sessions for
  bridges.
* SSH connect requires a usable local tmux server, including when a remote
  session is selected from an external caller. The exact no-local-client
  `--switch` response is specified separately.
* Connection lifecycle and immediate SSH/remote-tmux failures occur in the
  bridge pane after the short-lived `sesh connect` process has handed off to
  tmux.

## Implementation Plan

* **Affected paths**: `connector/` for canonical SSH dispatch and bridge
  translation; `tmux/` for command-capable session creation and reuse; `lister/`
  for excluding internal bridges from user-facing discovery; `model/` for any
  internal tmux metadata needed by those paths; `seshcli/` and `picker/` only
  where needed to preserve the existing `Name` handoff; corresponding tests in
  `connector/`, `tmux/`, and `lister/`.
* **Dependencies**: use the system OpenSSH client; do not add an SSH library to
  reimplement OpenSSH behavior.
* **Patterns to follow**: keep tmux operations behind the injected `tmux.Tmux`
  interface; preserve the existing `SwitchOrAttach` connection semantics; keep
  user-facing rows in `model.SeshSession` and connector-only bridge state out
  of public JSON unless a separately reviewed additive contract requires it.
* **Patterns to avoid**: do not expose the bridge name as the SSH session's
  public identity; do not create separate direct-attach and bridge connection
  paths; do not infer bridge ownership from a name prefix alone; do not expose
  bridges as ordinary local tmux sessions.
* **Configuration**: SSH discovery is enabled only when the user configures
  `[ssh]` hosts; configured OpenSSH aliases remain authoritative for connection
  settings.
* **Scope boundary**: marker representation, bridge-name generation, stale
  bridge criteria, zero-client behavior, and failure presentation belong in the
  feature specification and implementation. They are intentionally not fixed
  by this architectural decision.

### Verification

- [ ] A listed SSH session keeps `Src = "ssh"` and its canonical `Name` when
  passed unchanged to `sesh connect`.
- [ ] Both plain connect and `--switch` translate the same SSH identity to a
  local bridge and use existing local tmux attach/switch behavior.
- [ ] Internal bridges do not appear in default listing, tmux-only listing,
  JSON output, or picker results, while the connector can find and reuse them.
- [ ] User-created local tmux sessions are not hidden or reused as bridges
  based only on their names.
- [ ] Local non-SSH session listing and connection behavior remain compatible.
- [ ] The bridge runs interactive OpenSSH and remote tmux attach inside its
  pane; no SSH process is left owned by the short-lived Sesh CLI process.
- [ ] Tests cover bridge reuse and lifecycle, public identity handoff, and
  bridge exclusion from all user-facing discovery paths.

## More Information

Relevant existing seams include `connector/RealConnector.Connect` and
`RealConnector.focusSession`, `tmux.Tmux` and `RealTmux.SwitchOrAttach`,
`lister.listTmux`, and the picker path that passes the selected session name to
the connector. The detailed SSH identity encoding and public listing contract
are feature-spec requirements and may be recorded in a separate identity ADR.

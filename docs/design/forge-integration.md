# Design: git-forge access

> Status: accepted foundation. ycc detects and probes GitHub and GitLab CLI access; issue import
> and PR publication are not public ycc workflows.

## Context

Issue intake and pull-request publication need forge authentication, enterprise-host support, and
clear trust boundaries. Reimplementing provider authentication inside ycc would duplicate mature
host tooling and create another credential store.

## Decision

ycc relies on the official `gh` and `glab` command-line clients when forge access is needed. The
daemon environment, not an attaching client, must contain and authenticate the relevant CLI. This
matches ycc's local-daemon trust model: an agent already has shell access, while the official CLI
handles OAuth refresh, SSO, credential helpers, and enterprise host configuration.

The forge layer detects GitHub/GitLab from issue URLs and common HTTPS, SSH, and scp-style git
remotes. It probes installation, version, authenticated hosts, and readiness, returning actionable
install/login errors without reading forge credential files.

## Workflow boundaries

ycc has no first-class issue-import or PR-publication workflow. Agents may use an authenticated
forge CLI through ordinary, operator-directed shell work. That does not make the forge a hidden
synchronization service: ycc does not mirror issues, make a forge authoritative over the backlog,
publish automatically on session completion, force-push, or expose CLI credentials in events.

When an agent turns an issue into a backlog task, source provenance belongs in the task and forge
labels, assignees, and status are not silently treated as ycc lifecycle fields. Publishing remains
an explicit operation over an already reviewed commit or workstream with target and diff visible
to the operator.

## Rejected alternatives

- Provider API libraries add large dependency and auth surfaces while still requiring bespoke
  enterprise and token handling.
- A forge-neutral REST abstraction erases useful provider capabilities without reducing the
  security decisions.
- Automatic issue/backlog synchronization creates competing sources of truth and ambiguous
  conflict resolution.

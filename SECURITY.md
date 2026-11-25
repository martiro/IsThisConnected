# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems. Report them privately through
[GitHub's private vulnerability reporting](https://github.com/martiro/IsThisConnected/security/advisories/new)
("Report a vulnerability" in the Security tab).

Include what you found, how to reproduce it and which version or commit you tested. You will get an answer
as soon as possible. IsThisConnected is a personal project maintained in spare time, so please allow a few days.
Once a fix is released, the advisory is published with credit to you, unless you prefer otherwise.

## Supported versions

Only the [latest release](https://github.com/martiro/IsThisConnected/releases/latest) receives security fixes.

## Scope

Things we especially want to hear about:

- the local dashboard (`127.0.0.1:8777`) being reachable or controllable from another machine or website;
- anything that runs commands, reads or writes files outside the app's data folder, or sends data anywhere
  other than the network probes listed in the README (*Data and privacy*);
- a release file that does not match its attestation or cannot be rebuilt from source (see *Verifying downloads*).

## How releases are protected

- Executables are built by GitHub Actions from the public source, never on a personal computer.
- Each file has a signed build provenance attestation (Sigstore), which ties it to the exact commit and workflow run.
- The Windows and Linux builds are reproducible, and the release workflow fails if a second build on a different
  machine is not byte-identical.
- Every push runs `govulncheck` against the code, the dependencies and the Go standard library, and Dependabot
  proposes dependency and GitHub Actions updates. The actions are pinned to commit hashes.

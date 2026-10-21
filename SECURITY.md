# Security policy

flotsam handles API tokens and produces backups that can hold sensitive data, so we take
security reports seriously.

## Supported versions

flotsam is before 1.0. Security fixes go into the latest release only.

## Reporting a vulnerability

**Please don't open a public issue.** Report it privately through
[GitHub's private vulnerability reporting](https://github.com/fabelhaft-io/flotsam/security/advisories/new).
If you can't use GitHub, email [flotsam@fabelhaft.io](mailto:flotsam@fabelhaft.io) instead.

Please include:

- what the problem is and what an attacker could do with it
- the steps or config to reproduce it (with secrets removed)
- the flotsam version and, if relevant, the provider

We'll confirm that we received your report, keep you updated while we work on a fix, and
credit you in the advisory unless you'd rather stay anonymous.

## What counts

Examples of what we want to hear about:

- tokens or other secrets ending up in logs, error messages, snapshots or manifests
- snapshot paths that can escape the backup directory (path traversal)
- command or argument injection, for example in how `git` is called
- TLS or certificate checks being weakened or skipped

Vulnerabilities in the services flotsam backs up (GitHub, Jira, ...) should go to those
vendors.

## Protecting your backups

A snapshot contains everything flotsam could read with your token: private code, issues,
attachments. Treat backup storage as sensitive, limit who can read it, and use read-only
tokens with the narrowest permissions that cover what you back up.

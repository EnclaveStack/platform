# Security policy

EnclaveStack runs your apps, databases and secrets, so we take security reports seriously and answer them first.

## Reporting a vulnerability

**Please don't open a public issue, discussion or pull request for a security problem.**

Report it privately instead: on GitHub, open the repository's **Security** tab and choose **Report a vulnerability** ([direct link](https://github.com/EnclaveStack/platform/security/advisories/new)). Only the maintainer sees it.

Please include:

- what an attacker can do, and what they need first (network access, an account, a role);
- the steps or a proof of concept that shows it;
- the version (`enclave version`), your OS, and the engine (`--driver docker` or `fake`);
- any logs, with secrets removed.

## What happens next

| When | What |
|---|---|
| Within 3 working days | We confirm we received it. |
| Within 10 working days | We tell you whether we can reproduce it and how serious we think it is. |
| When a fix is ready | We release it, publish a GitHub security advisory and credit you, unless you'd rather not be named. |

Please give us a reasonable time to release a fix before you talk about it publicly. We won't take legal action against anyone who reports in good faith and follows this policy.

## Supported versions

EnclaveStack is pre-release. Fixes go into the latest release only; there are no backports until 1.0.

## Scope

In scope: the control plane (`enclave server`), the machine agent, the CLI, the OpenTofu provider, the web console and the catalogue templates in this repository.

Out of scope: the software EnclaveStack runs for you (PostgreSQL, Redis, MinIO and the rest). Report those to their own projects, and tell us too if EnclaveStack sets them up in an unsafe way.

How EnclaveStack is secured (authentication, encryption of secrets, what never appears in logs) is described in [`L3-006`](docs/L3-architecture/L3-006-security-architecture-v1.md).

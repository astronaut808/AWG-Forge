# Security Policy

awg-forge manages VPN configuration material, private keys, preshared keys, session secrets, and encrypted backups. Please do not open public issues for vulnerabilities that may expose secrets or weaken access control.

## Reporting a Vulnerability

Use GitHub's private vulnerability reporting for this repository when available:

https://github.com/astronaut808/awg-forge/security/advisories/new

If private reporting is unavailable, contact the maintainer through the GitHub profile and share only the minimum information needed to coordinate a private report.

## Supported Versions

Security fixes target the latest released version.

## Secret Handling

Never attach real `state.json`, `.env`, client `.conf`, encrypted backup passwords, private keys, preshared keys, or session secrets to public issues.

For diagnostics, use the built-in support bundle feature. It is designed to redact secrets before sharing.

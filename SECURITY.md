# Security

`gate` writes complete command logs. Those files can hold tokens, cookies, or
other secrets the wrapped command printed. Treat log directories as sensitive.

## Reporting a vulnerability

Please report security issues privately through
[GitHub Security Advisories](https://github.com/Rethunk-Tech/rethunk-gate-cli/security/advisories/new).

Do not open a public issue for a vulnerability.

## Release signing

Releases from the first one after this section landed carry `SHA256SUMS.asc`, a detached signature over `SHA256SUMS` made with the Rethunk.Tech release signing key. Earlier releases are unsigned.

- Fingerprint: `827D 0553 6BF3 3C8F 634E  DBE3 E60A 9B0F 6E3F 276E`
- Verify: `gpg --import rethunk-release-signing.pub.asc && gpg --verify SHA256SUMS.asc SHA256SUMS`

```
-----BEGIN PGP PUBLIC KEY BLOCK-----

mDMEavZr2xYJKwYBBAHaRw8BAQdAuTAn1oKhKC13SOyJMlYmaoGGQtVRyZ5yEpxT
yZBxH/q0NFJldGh1bmsuVGVjaCByZWxlYXNlIHNpZ25pbmcgPHJlbGVhc2VzQHJl
dGh1bmsudGVjaD6ImQQTFgoAQRYhBIJ9BVNr8zyPY07b4+YKmw9uPyduBQJq9mvb
AhsDBQkDwmcABQsJCAcCAiICBhUKCQgLAgQWAgMBAh4HAheAAAoJEOYKmw9uPydu
7WIA+wYeW352nuKph1uJ+XJPPpgmkPyYujZWiRBZIUUA5hfwAQDY6ru4vNL8TfQ5
5rddKHoSKh1nMIdiLWn9ukkyWW+MCg==
=meeO
-----END PGP PUBLIC KEY BLOCK-----
```

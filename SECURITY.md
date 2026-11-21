# Security

## Reporting a vulnerability

Use GitHub's private report form:
https://github.com/Will-Luck/forgemap/security/advisories/new

Please do not open a public issue for a vulnerability. It helps if you say:

- the ForgeMap version,
- how it is installed (Docker image, binary or built from source),
- the steps to reproduce it.

## Supported versions

The latest release gets fixes.

## What to expect

A reply, then a fixed release and a note in the changelog. There is no promise
of a time.

## Known limits

What ForgeMap shows, and to whom, is described in the wiki:
https://github.com/Will-Luck/forgemap/wiki/Privacy-and-Security

## Checking a download

Release binaries come with `SHA256SUMS`. From 0.3.2 the binaries and the
ghcr.io image also carry signed build provenance, which shows they were built
by this repository's release workflow.

Check a binary:

```bash
gh attestation verify <file> --repo Will-Luck/forgemap \
  --signer-workflow Will-Luck/ForgeMap/.github/workflows/release.yml
```

Check the image:

```bash
gh attestation verify oci://ghcr.io/will-luck/forgemap:0.3.2 --repo Will-Luck/forgemap \
  --signer-workflow Will-Luck/ForgeMap/.github/workflows/release.yml
```

# Signing & verification (cosign, keyless)

Goal: a customer can prove, with no phone call, that the file they downloaded is
the file we built from this commit.

## How it works here

* `.goreleaser.yaml` → `signs:` runs `cosign sign-blob --yes` over
  `dist/checksums.txt`, producing `checksums.txt.sig` + `checksums.txt.pem`.
* It runs **keyless**: the workflow's GitHub OIDC identity (issuer
  `https://token.actions.githubusercontent.com`, subject
  `https://github.com/<owner>/loopworker/.github/workflows/release.yml@refs/tags/vX.Y.Z`)
  becomes the certificate. There is no private key to store, rotate, or leak —
  which is what makes this maintainable with zero support staff.
* Requirements met in `release.yml`: `permissions: id-token: write`, the
  `sigstore/cosign-installer@v4` step with `version: v3.1.3` (pinned in the
  `COSIGN_VERSION` env), and the `release` job is the only signer.
* Each `sign-blob` writes a Rekor transparency-log entry. We emit only the
  certificate (`checksums.txt.pem`), so offline verification needs the Fulcio
  root bundled in cosign or `--certificate-chain`; a `--bundle` variant is an
  open decision below.

## Verify (customer side)

```bash
# 1. checksums
sha256sum -c checksums.txt          # linux/macOS  (Git Bash on Windows works too)
certutil -hashfile loopworker-0.1.0-windows-amd64.zip SHA256   # Windows built-in

# 2. signature over the checksums file
cosign verify-blob \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp 'https://github.com/<owner>/loopworker/.+' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  checksums.txt
```

**Do not** loosen the two `--certificate-*` flags to `.*`/`-regexp '.*'`. A
signature from anybody's workflow also "verifies"; the identity binding is the
whole point.

## CI-side verification

`verify-artifacts` in `release.yml` runs exactly the `cosign verify-blob` above
against the downloaded assets, with the identity regexp built from
`GITHUB_REPOSITORY`. If signing silently stops working (cosign install failure,
OIDC disabled on the repo, Rekor outage), that job fails instead of shipping
unsigned artifacts — `SBOM + signature exist` is asserted before verification.

## Enterprise / air-gapped variant (bring-your-own key)

Replace the keyless `signs` block with a stored key:

```yaml
signs:
  - id: cosign-key
    cmd: cosign
    certificate: "${artifact}.pem"
    output: true
    artifacts: checksum
    args:
      - sign-blob
      - "--yes"
      - "--key=cosign.key"              # cosign generate-key-pair, stored as a secret
      - "--output-signature=${signature}"
      - "--certificate=${certificate}.crt"
      - "${artifact}"
```

Then the customer verifies with `--key cosign.pub` (no network, no OIDC).
Tradeoff: you now own key custody, rotation and revocation — i.e. support work.

## Open decisions for the owner

1. Which identity regexp ships in the customer docs (needs the real owner/repo).
2. Whether to also attest provenance/SLSA for the *binaries* (today provenance
   `mode=max` is generated for the container image only). goreleaser can emit
   GitHub artifact attestations via `--archive=...`/`attest`; decide before v1.0.
3. Whether air-gapped installs are a paid requirement (→ BYO-key variant above).

# Releasing outflow

Releases are built by [GoReleaser](https://goreleaser.com) in GitHub Actions
(`.github/workflows/release.yml`) whenever a `v*` tag is pushed.

## One-time setup

1. A public repo `jbrathwa/homebrew-tap` (holds `Casks/outflow.rb`).
2. A fine-grained token with **Contents: read and write** on that repo only,
   saved in this repo as the Actions secret `HOMEBREW_TAP_GITHUB_TOKEN`.

## Cut a release

```sh
git checkout main && git pull
git tag v0.1.0
git push origin v0.1.0
```

The workflow runs vet and tests, then publishes:

- `outflow_{Darwin,Linux}_{x86_64,arm64}.tar.gz` and `outflow_Windows_x86_64.zip`
- `checksums.txt`
- release notes grouped from commit types (`feat`, `fix`, `docs`)
- an updated `Casks/outflow.rb` in the tap, so `brew upgrade` picks it up

Tags with a suffix such as `v0.2.0-rc.1` are marked as pre-releases.

## Try it locally first

```sh
brew install goreleaser
goreleaser check
HOMEBREW_TAP_GITHUB_TOKEN=unused goreleaser release --snapshot --clean
```

This builds every archive into `dist/` without publishing anything.

## Notes

- Binaries are not Apple-signed. The Homebrew cask clears the macOS quarantine
  flag after install; a binary downloaded with a browser may need
  `xattr -d com.apple.quarantine outflow` before its first run.
- When the tap token expires, the GitHub Release still publishes but the
  Homebrew step fails. Create a new token and update the secret.

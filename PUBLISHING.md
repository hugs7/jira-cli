# Publishing

This repo is wired up the same way as
[`hugs7/bitbucket-cli`](https://github.com/hugs7/bitbucket-cli/blob/main/PUBLISHING.md);
see that file for the full reusable playbook. The summary below covers
what's specific to **`jira-cli`** (binary `jr`).

## Sister repos / accounts

Already shared with bitbucket-cli — nothing new to create:

- **Homebrew tap**: `hugs7/homebrew-tap` (one tap holds many formulae)
- **Scoop bucket**: `hugs7/scoop-bucket` (one bucket holds many manifests)
- **Cloudsmith**: a *new* OSS repo `hugs7/jira-cli` (separate from
  `hugs7/bitbucket-cli`). Set the slug to exactly `jira-cli`.

## GitHub repo secrets

Add the same two secrets as bitbucket-cli (each repo has its own
secret store):

| Secret | Value |
|---|---|
| `TAP_GITHUB_TOKEN` | Classic PAT, scopes `repo` + `workflow`. Lets release-please trigger downstream workflows and lets GoReleaser push to the brew/scoop repos. |
| `CLOUDSMITH_API_KEY` | From https://cloudsmith.io/user/settings/api/ |

## Repo settings

*Settings → Actions → General*:

- **Workflow permissions**: Read and write.
- **Allow GitHub Actions to create and approve pull requests**: on.

## Cutting a release

Same flow as bitbucket-cli:

1. Land Conventional Commit messages on `main` (`feat:`, `fix:`, …).
2. release-please opens / updates a PR titled
   *"chore(main): release X.Y.Z"*.
3. Merge that PR → tag `vX.Y.Z` is created → release.yml runs
   GoReleaser → publishes to GitHub Releases, Homebrew tap, Scoop
   bucket, and Cloudsmith.

To test locally without publishing:

```sh
make snapshot
```

## Channels

| Channel | Install | Update |
|---|---|---|
| Homebrew | `brew install hugs7/tap/jira-cli` | `brew upgrade jira-cli` |
| Scoop | `scoop install jr` | `scoop update jr` |
| apt (Cloudsmith) | setup script + `apt install jira-cli` | `apt upgrade jira-cli` |
| dnf (Cloudsmith) | setup script + `dnf install jira-cli` | `dnf upgrade jira-cli` |
| apk (Cloudsmith) | setup script + `apk add jira-cli` | `apk upgrade jira-cli` |
| Direct binary / curl\|sh | install script | `jr upgrade` |

The binary itself is always **`jr`** — package names are the
descriptive form for clarity at install time.

# GitHub provider

Backs up GitHub repositories: their git data and wikis as git bundles, and issues, pull
requests, comments, releases, labels and milestones as the JSON the GitHub API returns.

## Quick start

```sh
export FLOTSAM_GITHUB_TOKEN=github_pat_...
flotsam backup https://github.com/fabelhaft-io           # every repository of an org or user
flotsam backup https://github.com/fabelhaft-io/flotsam   # one repository
flotsam backup github                                    # everything the token can access
```

## Settings

| Setting   | Environment variable       | Description |
|-----------|----------------------------|-------------|
| `token`   | `FLOTSAM_<SOURCE>_TOKEN`   | Secret. Required without `targets`; optional for public repositories |
| `targets` | `FLOTSAM_<SOURCE>_TARGETS` | `https://github.com/<owner>` or `https://github.com/<owner>/<repository>` URLs. Empty means everything the token can access |

Quick backups use the source name `github`, so their variables are `FLOTSAM_GITHUB_TOKEN`
and `FLOTSAM_GITHUB_TARGETS`.

What a target backs up:

- **`https://github.com/<owner>/<repository>`:** that repository.
- **`https://github.com/<owner>` for an organization:** every repository of it the token
  can see.
- **`https://github.com/<owner>` for a user:** that user's own repositories. Private ones
  are only listed for organizations; to back up your own private repositories, leave
  `targets` empty.
- **No targets:** every repository the token can access, as owner, collaborator or
  organization member.

Any other URL, such as an issue URL, is rejected rather than cut down to its repository.

## Token

A **fine-grained personal access token** with read-only access is recommended:

| Permission    | Needed for |
|---------------|------------|
| Metadata      | Always (GitHub adds it automatically) |
| Contents      | Git data, wikis and releases |
| Issues        | Issues, issue comments, labels and milestones |
| Pull requests | Pull requests and review comments |

Fine-grained tokens belong to one owner, so back up several organizations with one source
(and one token) each. A **classic token** with the `repo` scope covers every organization at
once, but also grants write access.

If the token lacks a permission, that part is skipped with a warning that names the
missing permission, and the snapshot is marked `partial`.

Without a token, public repositories can still be backed up, but GitHub allows only 60 API
requests per hour without one. That's enough for a small repository, not for the issues of
a large one. Cloning doesn't count against the limit.

The permissions above follow GitHub's documentation. The provider has been tested end to
end with a classic token; please report anything a fine-grained token is missing.

## Snapshot layout

```
<owner>/<repository>/
├── repository.json                     # the repository as the API returns it
├── git.bundle                          # every branch and tag; `git clone git.bundle` restores it
├── wiki.bundle                         # only if the wiki has pages
├── issues/42.json                      # issues *and* pull requests (GitHub treats both as issues)
├── issues/42/comments/<id>.json        # conversation comments, on issues and pull requests
├── pulls/17.json                       # pull request details: branches, merge state, reviewers
├── pulls/17/comments/<id>.json         # review comments on the code
├── releases/<id>.json                  # by id, because tag names can contain "/"
├── labels/<id>.json
└── milestones/<number>.json
```

A repository without any commits has no `git.bundle`. Disabled features, such as issues in
a repository with issues turned off, are skipped without a warning, since nothing is
missing.

## Known gaps

Not backed up yet:

- pull request reviews (approvals and their summary texts)
- release assets (the uploaded files; release notes are backed up)
- Discussions, Projects, Actions and their logs, gists
- GitHub Enterprise Server, which needs a setting for its API URL

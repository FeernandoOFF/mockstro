# Agent instructions

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/) for every commit and PR title:

```
type(scope)!: summary
```

- `type`: `feat`, `fix`, `docs`, `refactor`, `test` or `chore`.
- `scope` (optional): the area touched, e.g. `cli`, `tui`, `ui`, `config`, `fixtures`, `release`.
- `!` marks a breaking change; explain it in the body.
- `summary`: imperative, lower case, no trailing period.

Examples from this repo:

```
feat(cli)!: merge bootstrap into init and rename setup to check
fix(release): support prerelease versions across installation
docs: slim README and split reference docs by topic
```

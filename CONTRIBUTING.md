# Contributing

## Commit Message Format

This repo uses Conventional Commits for fully automated versioning.
You never set a version number manually — the CI derives it from your
commit messages.

### Format
```
<type>: <short description>

[optional body]

[optional footer]
```

### Types and their version impact

| Prefix | Version bump | Example |
|--------|-------------|---------|
| `fix:` | Patch (1.0.0 → 1.0.1) | `fix: suppress 0% reading on sleeping mouse` |
| `feat:` | Minor (1.0.0 → 1.1.0) | `feat: add Pixart protocol for Model D2` |
| `docs:` | Patch | `docs: update readme` |
| `chore:` | Patch | `chore: update dependencies` |
| `refactor:` | Patch | `refactor: extract HID read logic` |
| `BREAKING CHANGE:` in body | Major (1.0.0 → 2.0.0) | Any commit whose body contains `BREAKING CHANGE:` |

### What happens when you push to main

1. CI builds and tests your code
2. Auto-tagger reads commits since last tag, determines version bump, creates tag
3. Release workflow fires from that tag
4. Velopack packages the app (full installer + delta patch)
5. GitHub Release is created automatically with a generated changelog

You do nothing except push good commits.

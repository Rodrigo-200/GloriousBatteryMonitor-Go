# Contributing

## Commit Message Format

This repo uses Conventional Commits to keep the history and generated
changelog organized. Release versions are selected deliberately and published
from annotated semantic-version tags; CI does not create tags automatically.

### Format
```
<type>: <short description>

[optional body]

[optional footer]
```

### Types and their intended version impact

| Prefix | Version bump | Example |
|--------|-------------|---------|
| `fix:` | Patch (1.0.0 → 1.0.1) | `fix: suppress 0% reading on sleeping mouse` |
| `feat:` | Minor (1.0.0 → 1.1.0) | `feat: add Pixart protocol for Model D2` |
| `docs:` | Patch | `docs: update readme` |
| `chore:` | Patch | `chore: update dependencies` |
| `refactor:` | Patch | `refactor: extract HID read logic` |
| `BREAKING CHANGE:` in body | Major (1.0.0 → 2.0.0) | Any commit whose body contains `BREAKING CHANGE:` |

### Publishing a release

1. Merge the intended release commits into `main` and confirm CI passes.
2. Select the next semantic version from the accumulated user-facing changes.
3. Create an annotated tag on the tested `main` commit, for example:

   ```powershell
   git tag -a v3.4.1 -m "Glorious Battery Monitor v3.4.1"
   git push origin v3.4.1
   ```

4. The tag-driven Release workflow builds and tests the solution, publishes the
   self-contained Windows app, and creates the Velopack installer, portable
   package, update manifests, and GitHub release.
5. Confirm the workflow and release assets succeeded, then replace the generated
   changelog with the curated stable-release notes when appropriate.

Use SemVer prerelease identifiers such as `v3.4.2-beta.1`; ambiguous forms such
as `v3.4.2-beta1` are rejected by the Release workflow.

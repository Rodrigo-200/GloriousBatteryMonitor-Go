# Agent Instructions

## Branching Rules — ALWAYS follow these, no exceptions

- NEVER commit directly to `main`.
- For every task, create a branch:
  - `feature/<short-description>` for new functionality
  - `fix/<short-description>` for bug fixes
  - `chore/<short-description>` for maintenance/tooling
- Open a Pull Request from that branch to `main` when the task is complete.
- The PR title must follow Conventional Commits format: `feat: ...`, `fix: ...` etc.
- The PR description must explain what changed and why.
- Do NOT merge the PR yourself. Leave it for the human to review and merge.

## Releases — NEVER trigger manually

- Releases are triggered exclusively by the human pushing a git tag (`v1.2.3`).
- Do not create tags yourself.
- Do not modify `release.yml` to trigger on anything other than version tags.
- The version in the built artifacts comes from the tag — do not hardcode versions anywhere.

## Commit Messages

- Use Conventional Commits strictly: `feat:`, `fix:`, `perf:`, `refactor:`, `docs:`, `chore:`, `ci:`
- `chore:` and `ci:` commits are excluded from the changelog automatically — use them for housekeeping.
- Every meaningful user-facing change must be `feat:` or `fix:` so it appears in the release notes.

# Changelog style

The changelog is for the people who use the product, not for the people who
wrote the code. Each entry answers: what can I do now, or what stopped
breaking, and do I have to do anything.

## Layout

Follow Keep a Changelog: newest release first, `## [Unreleased]` on top, and
inside each release only the groups that have entries, in this order:

- `### Added` for new things people can use
- `### Changed` for behavior that works differently
- `### Fixed` for bugs that are gone
- `### Removed` for things that no longer exist
- `### Security` for vulnerabilities closed

If the file already uses other headings, or another language, keep its
headings.

## Entries

- One bullet per change a user notices. Several commits can be one bullet.
- Start with what the person sees: "Exporting a report keeps the filters",
  not "Refactor ExportService".
- Name the screen, command or endpoint, and the number when there is one:
  "Upload limit raised from 10 to 50 MB".
- If people must act (a config key renamed, a migration to run), say exactly
  what to do in the same bullet.
- No commit hashes, ticket numbers or author names unless the file already
  carries them.

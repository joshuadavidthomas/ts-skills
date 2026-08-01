# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project attempts to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

<!--
## [${version}]

### Added - for new features
### Changed - for changes in existing functionality
### Deprecated - for soon-to-be removed features
### Removed - for now removed features
### Fixed - for any bug fixes
### Security - in case of vulnerabilities

[${version}]: https://github.com/joshuadavidthomas/ts-skills/releases/tag/${tag}
-->

## [Unreleased]

### Added

- Added checksum- and provenance-aware install scripts for macOS, Linux, and Windows.

### Changed

- Switched TOML parsing to BurntSushi's Go package.

## [0.1.0]

### Added

- Private Agent Skills registry daemon with persistent SQLite state and Tailnet-backed identity and access control.
- Server-rendered web UI for browsing publications, uploading one local skill directory, reviewing its files and digest, publishing it, and selecting the current publication.
- Immutable publications identified by a namespaced skill and normalized SHA-256 tree digest.
- `ts-skills install` for project-scoped installs, including exact publication selection with `--digest`.
- Project lock files and `ts-skills restore` for recreating the exact trees recorded in a lock.
- Loopback dev mode for trying the full upload, publish, install, and restore flow without Tailnet credentials.
- Complete-tree validation and digest verification for uploads and installs, including path, link, special-file, size, and archive safety checks. Skill content remains inert throughout the process.

[unreleased]: https://github.com/joshuadavidthomas/ts-skills/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/joshuadavidthomas/ts-skills/releases/tag/v0.1.0

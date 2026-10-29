# FruityHub: working conventions

- Never use the user's live FruityHub settings, accounts, projects or server database for tests or UI fixtures. Keep the cross-platform isolation in `internal/app/main_test.go`; use temporary HOME, USERPROFILE, APPDATA, LOCALAPPDATA and XDG_CONFIG_HOME for standalone test processes too.
- Keep unrelated user files out of commits. Build artifacts and credentials are not source files.
- User-facing changes are released as one coherent version after checks: update the version badge at the top of README.md, `web/static/js/changelog.js`, both locale dictionaries, and a human-readable `docs/releases/<version>.md`.
- Release descriptions explain concrete user-facing changes in Russian. Do not publish a separate app update just to describe internal test repairs.
- Use the configured Git author. The user has requested commits, push, builds and deployment through `deploy/server-up.sh` for completed application changes; preserve existing server configuration and verify the uploaded version.
- Check Linux and Windows CI before publishing; Windows-only behavior needs Windows tests/build checks. Report manual testing limitations accurately.

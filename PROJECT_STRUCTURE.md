# VRSky Project Structure

This file used to describe the repository layout. It was written for the
February 2026 move of the code into `src/` and stopped matching soon after:
it named files, packages and make targets that no longer exist.

The layout, the commands and where to run them are in
[`AGENTS.md`](AGENTS.md), which is kept up to date with the code:

- [Layout](AGENTS.md#layout): what lives in `src/`, `ui/`,
  `infrastructure/`, `docs/` and `plans/`
- [Go](AGENTS.md#go): build, test and lint commands, run from `src/`
- [Local stack](AGENTS.md#local-stack): `make up-core` and `docker compose`
- [How a merge deploys](AGENTS.md#how-a-merge-deploys): how images are built
  in ACR and rolled out

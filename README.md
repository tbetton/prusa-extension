# Prusa Connect Local Extensions

Extensions for [Prusa Connect Local](https://www.prusa3d.com/p/connect/connect-local/get-started/).

| Extension | Directory | Description |
| --- | --- | --- |
| Print Queue | [`print-queue/`](print-queue/) | Queue and schedule prints for each printer. |

## Build and publish

Each extension has its own `module.yaml`, Go module and build instructions. The public sources sync from Gitea to `dev`. Review and merge the changes into `main` for a Prusa Marketplace dry run.

To publish an extension, create a GitHub release from a reviewed commit on `main`. Use a tag such as `print-queue/v0.1.26`, with the version from that extension's manifest. Use a pre-release to publish to the beta channel.

Each extension needs its own Trusted publishing entry in the [Prusa author console](https://plugin-marketplace.prusa3d.com). Configure the extension ID, repository `tbetton/prusa-extension`, workflow file `release.yml` and environment `release`. Keep the Go module path of an existing extension stable because it determines the Marketplace ID.

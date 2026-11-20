# Print Queue for Prusa Connect Local

Print Queue manages a separate queue for each printer. When a printer reports **READY**, the extension starts the next eligible job from server storage or the printer's drive. Clear the build plate and confirm readiness on the printer or with **Mark as ready**. You can schedule a job to wait until a given time.

- Open **Print Queue** in the sidebar to see all printers or open the **Print Queue** tab on a printer's page.
- Use **Add to the print queue** in the action menu for a file in server storage.
- Reorder, schedule, remove, or requeue jobs, and hold or resume a printer's queue.
- Use **Mark as ready** on a compatible printer, then confirm the build plate is empty. The queue waits for the printer to report **READY** before it dispatches a job.
- See the current print and its progress, including prints started outside the extension.
- The extension stores the queue in `queue.json` in its data directory.

The extension displays its interface, dates, and error messages in English.

## Build and install

To install a published version, open **Settings → Extensions** in Prusa Connect Local and search for **Print Queue** in the Marketplace.

Check out the official SDK into `.core/`. Run `BUILD_ONLY=1 sh install.sh` to build the manifest, native binary, and UI bundle into `ui/dist`. Run `UI_ONLY=1 sh install.sh` to build just the UI bundle in the format expected by the [Prusa Marketplace builder](https://plugin-marketplace.prusa3d.com/docs/connect/github-actions).

Run `go vet ./...` and `go test -race ./...` to verify the module.

## Publish to Prusa Marketplace

The **Prusa Marketplace** GitHub workflow runs a dry run after each push to `main`. Publishing a GitHub release tagged `print-queue/v<version>` with the version declared in `module.yaml` submits it to Prusa for review. Mark the release as a pre-release to use the beta channel. See the [Prusa publishing guide](https://plugin-marketplace.prusa3d.com/docs/connect/github-actions).

## Printer permissions

The `-approve` flag authorizes the permissions declared by the module. To dispatch queued jobs after the printer reports READY, an administrator must explicitly grant the extension the operator role on each selected printer.

```text
POST /api/v1/grants/device/<printer-id>
{"relation": "operator", "subject": "sink:print-queue"}
```

Viewing the queue or history requires permission to observe the printer. Adding or changing a job requires permission to operate that printer. Adding and starting a job from server storage also requires read access to the storage.

When a job starts, Connect rechecks the permissions of the person who queued or requeued it. Approve `check_absent_people` at installation so Connect can check local accounts after a restart. In open mode or with OIDC, Connect may be unable to check a person until it has handled a request from them since startup. The job is then marked as failed, and the person can requeue it after reopening the extension.

## Print tracking

Print Queue tracks transfer and print events from Prusa Connect. When an event is missing, it checks the printer's current job and job history. It matches the print ID when available, or the exact printer path and start time when they identify a single print. When the extension transfers a file from the server, it adds a unique prefix to the filename on the printer to match the transfer with its start command. The source file keeps its name.

When the result of a start command is uncertain, Print Queue marks the job as Unconfirmed and holds that printer's queue. The result updates when Jobs confirms it. Check the printer and clear the plate if needed before resuming the queue.

## Settings

Open **Settings → Extensions → Print Queue**.

| Key | Default | Purpose |
|---|---|---|
| `poll_seconds` | 10 | How often the extension checks the printers, in seconds |
| `start_timeout_minutes` | 30 | Maximum time between dispatch and the start of a print, in minutes |
| `printer_folder` | usb | Destination folder on the printer |
| `slicer_upload_folder` | print-queue | Folder in Connect Local storage for PrusaSlicer uploads |
| `keep_history` | 20 | Number of finished jobs retained per printer |

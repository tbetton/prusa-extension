# Print Queue for Prusa Connect Local

Print Queue manages a separate queue for each printer. When a printer reports **READY**, the extension starts the next eligible job from server storage or the printer's drive. Clear the build plate and confirm readiness on the printer or with **Mark as ready**. You can schedule a job to wait until a given time.

- Open **Print Queue** in the sidebar to see all printers or open the **Print Queue** tab on a printer's page.
- Use **Add to the print queue** in the action menu for a file in server storage.
- Reorder, schedule, remove, or requeue jobs, and hold or resume a printer's queue.
- Use **Mark as ready** on a compatible printer, then confirm the build plate is empty. The queue waits for the printer to report **READY** before it dispatches a job.
- See the current print and its progress, including prints started outside the extension.
- The extension stores the queue in `queue.json` in its data directory.

## Send a file from PrusaSlicer

Print Queue does not require another extension. The PrusaSlicer 2 Connector extension published by Prusa lets PrusaSlicer 2.4 and later send a file to a printer of the server as to a printer with PrusaLink. PrusaSlicer shows only **Upload** and **Upload and Print** for that kind of host, never a queue.

1. Install the PrusaSlicer 2 Connector, add the printer in PrusaSlicer as a PrusaLink host with an API key, and turn on **Save to storage** on the connector's card for the printer.
2. Pick the storage `local` and a folder, for example the printer's name. Print Queue finds a folder named after the printer, apart from case and spacing or after its id, in `print-queue` or at the root of the storage. For any other folder, type it in the **Folder** field of **From PrusaSlicer** on the printer's card.
3. Press **Upload** in PrusaSlicer, not **Upload and Print**, which starts the file at once.
4. On the printer's card in Print Queue, **From PrusaSlicer** lists the new files. Press **Add** to queue one, or turn on **Add new uploads to the queue by themselves**. The files then join the queue as the person who turned the switch on, while that person may operate the printer and read the storage.

**Settings → Extensions → Print Queue → PrusaSlicer** lists the steps. Change the storage and the folder with `inbox_storage` and `inbox_folder`. **Add a file** also lists the storages of the server.

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
| `inbox_storage` | local | Storage where the PrusaSlicer 2 Connector saves uploads |
| `inbox_folder` | print-queue | Folder of that storage with one folder for each printer |
| `keep_history` | 20 | Number of finished jobs retained per printer |

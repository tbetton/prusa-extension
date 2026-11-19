# Print Queue for Prusa Connect Local

Print Queue manages a separate queue for each printer. When a printer reports **READY**, the extension starts the next eligible job from server storage or the printer's drive. By default you clear the build plate and confirm on the printer. You can schedule a job to wait until a given time.

- Open **Print Queue** in the sidebar to see all printers or open the **Print Queue** tab on a printer's page.
- Use **Add to the print queue** in the action menu for a file in server storage.
- Reorder, schedule, remove, or requeue jobs, and hold or resume a printer's queue.
- See the current print and its progress, including prints started outside the extension.
- The extension stores the queue in `queue.json` in its data directory.

The extension displays its interface, dates, and error messages in English.

## Build and install

To install a published version, open **Settings → Extensions** in Prusa Connect Local and search for **Print Queue** in the Marketplace.

Check out the official SDK into `.core/`. Run `BUILD_ONLY=1 sh install.sh` to build the manifest, native binary, and UI bundle into `ui/dist`. Run `UI_ONLY=1 sh install.sh` to build just the UI bundle in the format expected by the [Prusa Marketplace builder](https://plugin-marketplace.prusa3d.com/docs/connect/github-actions).

Run `go vet ./...` and `go test -race ./...` to verify the module.

## Publish to Prusa Marketplace

The **Prusa Marketplace** GitHub workflow runs a dry run after each push to `main`. Publishing a GitHub release with the version declared in `module.yaml`, such as `v0.1.15`, submits it to Prusa for review. Mark the release as a pre-release to use the beta channel. See the [Prusa publishing guide](https://plugin-marketplace.prusa3d.com/docs/connect/github-actions).

## Printer permissions

The `-approve` flag authorizes the permissions declared by the module. To start jobs automatically, an administrator must explicitly grant the extension the operator role on each selected printer.

```text
POST /api/v1/grants/device/<printer-id>
{"relation": "operator", "subject": "sink:print-queue"}
```

Viewing the queue or history requires permission to observe the printer. Adding or changing a job requires permission to operate that printer. Adding and starting a job from server storage also requires read access to the storage.

When a job starts, Connect rechecks the permissions of the person who queued or requeued it. Approve `check_absent_people` at installation so Connect can check local accounts after a restart. In open mode or with OIDC, Connect may be unable to check a person until it has handled a request from them since startup. The job is then marked as failed, and the person can requeue it after reopening the extension.

## Print tracking

Print Queue tracks transfer and print events from Prusa Connect. When an event is missing, it checks the printer's current job and job history. It matches the print ID when available, or the exact printer path and start time when they identify a single print. When the extension transfers a file from the server, it adds a unique prefix to the filename on the printer to match the transfer with its start command. The source file keeps its name.

When the result of a start command is uncertain, Print Queue marks the job as Unconfirmed and holds that printer's queue. The result updates when Jobs confirms it. Check the printer and clear the plate if needed before resuming the queue.

## Build plate check

1. Open **Settings → Extensions → Print Queue → Build plate check**. Choose the printer and camera, check the preview, and click **Connect camera**. A sole printer or camera is selected automatically. When Prusa Connect requires sign-in, this button creates a read-only credential from your current session and saves it for background checks. If access expires or is revoked, use **Reconnect camera** from the same tab.
2. Clear the plate, park the toolhead where it normally stops after printing, and check the live view. Under **Print Queue → Build plate check**, choose **Capture empty plate reference**. Test the check with automatic starts off. Queue a follow-up file and compare the reported results with what is actually on the plate, including a small object in the camera's view.
3. To opt in, select **Allow automatic starts after the plate check**. After Prusa Connect records a successful print and the printer reports **FINISHED**, Print Queue looks for two fresh matching pictures at least 30 seconds apart. It then asks the printer to switch to **READY**. It waits for the printer to report **READY** before sending the eligible job. Capturing a replacement reference turns automatic starts off.

**Server connection** defaults to `http://127.0.0.1:8080` for a shared Prusa Connect container, or `http://127.0.0.1:8090` when the app is opened on port 8090. If Docker maps the browser's port to a different port inside the container, use the internal port. Automatic credential setup works with either loopback address or with the same origin as the browser. For a different service address, set its snapshot URL and a read-only credential for that server in the standard **Settings** tab. Server-managed installs can also supply the token on one line in `plate-camera.token` next to `queue.json` (mode `0600`). Print Queue uses that file when the masked `plate_camera_token` setting is empty.

Use a fixed camera that sees the entire build plate, with steady lighting and a clear view. The check compares the whole frame and can miss small or hidden objects. It holds if the camera gives stale or identical JPEGs, the view changes, or the queue is paused. It expects snapshots with a `Last-Modified` time at most eight seconds old and supports JPEGs from 320 × 240 up to two megapixels. The reference stays in the extension's data directory. Capture a new reference after changing the printer or camera.

If the automatic READY request becomes uncertain, hold the queue and check the printer in person. While the queue is held, let Print Queue observe **READY** and then its cancellation before resuming normal operation. Keep each state up for at least one polling interval. Clear the plate before confirming **READY** again.

## Settings

Open **Settings → Extensions → Print Queue**.

| Key | Default | Purpose |
|---|---|---|
| `poll_seconds` | 10 | How often the extension checks the printers, in seconds |
| `start_timeout_minutes` | 30 | Maximum time between dispatch and the start of a print, in minutes |
| `printer_folder` | usb | Destination folder on the printer |
| `keep_history` | 20 | Number of finished jobs retained per printer |
| `plate_check_printer` | unset | Device ID of the printer to check |
| `plate_camera_url` | unset | Prusa Connect Local camera snapshot URL on port 80, 443, 8080, or 8090 |
| `plate_camera_token` | unset | Masked read-only API credential for the camera snapshot |

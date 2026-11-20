# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.1.33

- Build plate checks use the selected camera’s local H.264 RTSP stream. Print Queue no longer creates or uses personal API credentials.

## 0.1.32

- Reduce compression noise when comparing HD RTSP snapshots in the build plate check.

## 0.1.31

- Select a camera with only an H.264 RTSP stream in Build plate check. Print Queue reads that stream directly to create fresh plate snapshots.

## 0.1.30

- Retry camera frames during plate calibration, tolerate small uniform changes in exposure, and show what prevents calibration from completing.

## 0.1.29

- Confirm the build plate is empty once to collect three reference photos automatically. Save separate calibration sets for idle and finished printer positions.

## 0.1.28

- Check the build plate and start the next queued job automatically while the printer is idle.

## 0.1.27

- Retry the second build plate capture if the camera temporarily returns a stale or repeated image.

## 0.1.26

- Install Print Queue on Prusa Connect Local hosted on macOS (Intel or Apple silicon) or Windows (x64 or ARM64).

## 0.1.25

- Capture the plate reference, review check status, and enable automatic starts in the **Build plate check** settings tab.

## 0.1.24

- Turn the camera check on or off directly from the **Build plate check** tab.
- Show an **Automatic start** badge when enabled, keep **Mark as ready** visible during printing, and group job details and actions for narrow screens.

## 0.1.23

- Keep **Mark as ready** visible for operators and show why it is disabled while the printer is busy or unavailable.

## 0.1.22

- Turn off the camera-based build plate check with `plate_check_enabled` in the extension settings.
- Use **Mark as ready** in Print Queue to confirm an empty plate and set a compatible printer to READY.

## 0.1.21

- Build plate check supports Prusa Connect Local on port 8090 and suggests the corresponding loopback address when the app uses that port.

## 0.1.20

- Select the camera and choose Connect camera. Print Queue creates a read-only credential when the server requires sign-in.
- Reference capture reports authentication, connection, and snapshot errors separately.

## 0.1.19

- When a direct camera URL is already saved, the Build plate check settings suggest the Prusa Connect server address and let you select the registered camera.

## 0.1.18

- Select a printer and its camera by name in the Build plate check settings tab, and preview a snapshot before saving.

## 0.1.17

- Configure a camera with `plate_check_printer` and `plate_camera_url`, then capture an empty build plate reference in Print Queue.
- Opt in to automatic readiness after a successful print. The next job can start after two fresh captures match the reference at least 30 seconds apart and the printer reports READY.

## 0.1.16

- Print Queue recovers missing start and end events from the printer's Jobs history, including previously unconfirmed prints.
- The queue shows an Unconfirmed badge when Connect has no verified result.

## 0.1.15

- Print Queue uses English for its interface, dates, and error messages regardless of the Prusa Connect Local language setting.

## 0.1.14

- The sidebar and the printer tab display “Print Queue”.

## 0.1.13

- The queue, its error messages, and the timeline follow the Prusa Connect Local language and formats in English or French.

## 0.1.12

- The queue shows the current print and its progress, including prints started from Prusa Connect or on the printer itself.
- The timeline schedules waiting jobs around the current print's estimated end time.

## 0.1.11

- The queue respects the account's permissions on each printer. An administrator explicitly selects the printers the extension can operate.
- When a delayed job starts, Connect rechecks the permissions of the account that queued the file.
- The queue tracks Prusa Connect transfer and print events. An uncertain result holds the printer's queue.
- Changes take effect after they are saved to disk.

## 0.1.0 - unreleased

### Added

- `GET /print-queue` answers one line and the principal the host authorized.
- The setting `message` sets the line that route answers with.
- Open Print Queue from the sidebar.
- Open the queue for a specific printer from its Print Queue tab.
- Add a file to the queue from its action menu in server storage.

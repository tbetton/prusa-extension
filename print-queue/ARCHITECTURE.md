# Print Queue: how it works

This guide explains how the code is organised and how a print moves through it. Read it before changing the code. [README.md](README.md) covers installation and settings, and [CHANGELOG.md](CHANGELOG.md) lists what changed in each version.

## The two halves

Print Queue is one extension made of two programs that ship together.

- **The module** (Go, the `*.go` files at the root) runs as a child process of Prusa Connect Local. It keeps the queues, talks to the printers through the host, and serves a small HTTP API. It talks to the host over its standard input and output, never over the network.
- **The page** (Svelte and TypeScript, `ui/src`) runs in the browser. It draws the queue and calls the module's routes with the session cookie of the person who signed in.

The module never trusts the page. Every route asks the host whether the person may see or operate the printer, and a job is checked again when it starts.

## Map of the code

| File | What it does |
|---|---|
| `main.go` | The process, its settings, and the HTTP routes of the queue. Also the permission helpers (`check`, `allowed`, `requireOperate`, `authorizeDispatch`). |
| `queue.go` | The data (`Job`, `snapshot`), the `Store` that keeps it in `queue.json`, and the `Scheduler` that starts jobs. |
| `print_events.go` | Follows the host's event stream and moves each job along as its transfer, start command and print settle. |
| `print_history.go` | Settles jobs from the printer's current print and job history, for events that were missed. |
| `ready.go` | `POST /devices/{id}/ready`: marks a printer ready after a person confirms the plate is empty. |
| `inbox.go` | The files PrusaSlicer left for a printer, and the optional automatic adding. |
| `faults.go` | The refusals the routes answer, each with a code the page translates. |
| `openapi.json` | The description of the routes, served to the host. Keep it in step with `main.go` and `inbox.go`. |
| `module.yaml` | The manifest: version, permissions the module asks for, settings and the page's contributions. |
| `ui/src/queue-view.svelte` | The queue of one printer or of all. Cards, jobs, timeline, actions. |
| `ui/src/inbox.svelte` | The **From PrusaSlicer** block of a printer's card. |
| `ui/src/printer-files.svelte` | The side panel to add a file from the printer's drive or from a storage. |
| `ui/src/plan.ts`, `timeline.svelte` | The expected start and end of each job, worked out in the browser. |
| `ui/src/how-to.svelte` | The **PrusaSlicer** page under Settings, Extensions. |
| `ui/src/api.ts` | The calls to the module's routes (and to the PrusaSlicer 2 Connector's). |

## Data

The module keeps everything in one file, `queue.json`, in its data directory. `Store` holds it in memory, and every change goes through `Store.update`, which copies the data, applies the change, writes a temporary file and moves it over `queue.json`. A crash leaves the old file or the new one, never half of one.

`snapshot` holds:

- `jobs`: every job of every printer, in queue order.
- `paused`: the printers whose queue is held.
- `cursor`: the last host event handled, saved in the same write as the jobs it changed.
- `plate_ready`: printers marked ready by hand until the printer itself confirms.
- `auto_add`, `inbox_handled`, `inbox_folders`: the inbox (see below).

Only the newest `keep_history` finished jobs of each printer are kept.

## The life of a job

```text
queued --> starting --> printing --> done
   ^           |            |------> stopped
   |           |            `------> failed
   |           `-------------------> failed (and the queue may be held)
   `-- requeue (from done, stopped or failed)
```

1. **Add.** `POST /jobs` checks that the person may operate the printer and, for a file in storage, read it. The job joins the end of the queue as `queued`.
2. **Tick.** Every `poll_seconds`, `Scheduler.Tick` looks at each printer that has work. For each one, `tickDevice` first settles what it can from the printer's data (`syncPrints`).
3. **Copy ahead (optional).** While the printer is not ready, `stageNext` copies the file of the job that will start next onto the printer, without starting it (`prestage`). The transfer settles through the event stream, and the job is marked `staged`. A failed or stale copy is forgotten, and the file is then sent when the print starts. At most two copies stay on a printer.
4. **Pick.** A queued job is picked only if the printer is connected and reports `READY`, nothing else is starting or printing on it, its queue is not held, no manual ready mark is still waiting for the printer to confirm, and the job's start time has come. The job is saved as `starting` before the printer is called, so a crash cannot lose the attempt.
5. **Start.** `Scheduler.start` checks the rights of the person who queued the job again, then asks the host to print. A file from storage is sent first, under a unique name, and then started. A staged job is printed from its copy on the printer, and nothing is sent. A job whose copy is still travelling waits for it.
6. **Follow.** `print_events.go` reads the host's events: the transfer settles, the start command is requested and settles, the job starts (the printer reports a print id) and settles (finished, cancelled or failed). Each event moves the matching job, matched by the ids the job carries.
7. **Recover.** If events are missed, `print_history.go` compares the jobs with the current print, the last print and the job history. A job with no print id is matched only when its path and start time identify exactly one print.
8. **Hold when unsure.** A job that lost track of its print becomes `failed` with the detail `unconfirmed`, and the printer's queue is held. A person checks the printer and resumes. If the print's events arrive later, the job recovers by itself.

## Permissions

The manifest asks for `observe` and `operate` on printers and `read_storage`. The module checks four things through the host:

| When | Check |
|---|---|
| Reading a queue | `observe` on the printer |
| Changing a queue, marking ready | `operate` on the printer |
| Adding a file from storage | `read_storage`, and the file must exist for the caller |
| Starting a job | `operate` and, for storage, `read_storage`, for the person who queued it, at that moment |

An administrator must also grant the module itself the operator role on each printer it starts (see the README). `check_absent_people` lets the host check a local account after a restart.

## The inbox (files from PrusaSlicer)

PrusaSlicer 2 shows only **Upload** and **Upload and Print** for a PrusaLink host, never a queue. The PrusaSlicer 2 Connector extension, published by Prusa, accepts the upload and can save a copy in a storage folder. Print Queue reads that folder.

- **Which folder.** For a printer, the module uses, in order: the folder a person chose (`inbox_folders`), a folder named after the printer inside `inbox_folder` of the storage `inbox_storage`, or a folder named after the printer at the root of that storage. The name is compared without case or extra spaces, and the printer's id also matches.
- **Which files.** Printable files only (`.gcode`, `.bgcode`, `.gco`, `.g`), not empty, and unchanged for at least eight seconds so a file still being written is not queued half sent.
- **Handled files.** Adding or ignoring a file records its modification time in `inbox_handled`. The file is offered again only if it is saved again. A mark is dropped when its file leaves the folder.
- **Automatic adding.** With the switch on, the loop adds each new file, oldest first, as the person who turned the switch on, after checking that person's rights as for any job.
- **The connector.** The page asks the connector's `/printers/{printer}/keep` route which folder it saves to, and follows it. **Set up PrusaSlicer uploads** sets it. The module never calls the connector, so it works without it.

## The page

`queue-view.svelte` reads the queue and each printer's running print every five seconds. A print started outside the queue is shown too. `plan.ts` lays the jobs out in time: the running print ends when the printer says, and each next job starts after a changeover, or at its "not before" time if later. The slicer's estimate comes from the file name, corrected by how long the last finished prints really took.

All text goes through `host.t` and `src/locales/en.json`. Do not write a sentence in a component.

## Building and testing

```sh
git clone --depth 1 --branch sdk/v0.1.0-beta.2 https://github.com/prusa3d/prusa-connect.git .core
go vet ./... && go test -race ./...
BUILD_ONLY=1 sh install.sh
```

The version appears in `module.yaml`, `main.go` and `openapi.json`, and the top heading of `CHANGELOG.md` must match. `sh ../scripts/check-version.sh print-queue` checks them.

## Where to change what

| To change | Start in |
|---|---|
| When a job may start | `tickDevice` in `queue.go` |
| How a print is tracked | `applyPrintEvent` in `print_events.go`, `syncPrints` in `print_history.go` |
| Who may do what | `main.go` (`check`, `requireOperate`) and the manifest |
| A route | `main.go` or `inbox.go`, then `openapi.json`, then `api.ts` |
| A refusal | `faults.go`, then `print-queue.fault.*` in `en.json` |
| The inbox | `inbox.go`, `inbox.svelte` |
| A sentence on screen | `ui/src/locales/en.json` |

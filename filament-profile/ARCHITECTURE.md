# Filament Profile: how it works

This guide explains how the code is organised and how a profile is made. Read it before changing the code. [README.md](README.md) covers installation, settings and use, and [CHANGELOG.md](CHANGELOG.md) lists what changed in each version.

## What it does

A printer such as the Core One INDX can carry several spools, and PrusaSlicer needs a filament profile for each. This extension reads the spools loaded on a printer's tools, finds the nearest PrusaSlicer filament preset for each one, lets a person adjust them, and writes one **Config Bundle** (`.ini`) to import in PrusaSlicer 2.9 with **File > Import > Import Config Bundle**.

It targets PrusaSlicer 2.9 only. The format of PrusaSlicer 3 profiles has no documented import for a user, so it is not written.

## The two halves

- **The module** (Go, the `*.go` files at the root) runs as a child process of Prusa Connect Local and talks to it over its standard input and output. It downloads and indexes the presets, answers matches and searches, and writes the bundle.
- **The page** (Svelte and TypeScript, `ui/src`) is the **Filament Profile** tab of a printer's page. It reads the spools, calls the module's routes with the session cookie, and downloads the bundle.

The Materials extension (`com.github.aux.prusa3d.materials`) says which spool is on which tool. Filament Profile requires it in `module.yaml`, so installing Filament Profile installs Materials when it is missing. The page still falls back to the printer's own report (nozzle and type of material) when Materials does not answer.

## Map of the code

| File | What it does |
|---|---|
| `main.go` | The process, its settings, the catalogue's life (`configure`, `refresh`) and the HTTP routes. |
| `source.go` | The sources of presets and how they are fetched and assembled into one catalogue. |
| `fetch.go` | Downloads the Prusa Research archive and picks its newest bundle. |
| `ini.go` | Reads a PrusaSlicer bundle, flattens presets that inherit from each other, and finds the nearest preset for a spool. |
| `compat.go` | Answers "does this preset fit this printer?" by evaluating PrusaSlicer's `compatible_printers_condition` expressions. |
| `bundle.go` | Writes the Config Bundle: one section per spool, and the `[presets]` section. |
| `printer.go` | Writes a copy of the printer preset whose extruders carry the spools' colours. |
| `properties.go` | Applies the maker's temperatures and density to a profile. |
| `faults.go` | The refusals the routes answer, each with a code the page translates. |
| `openapi.json`, `module.yaml` | The routes and the manifest (version, permissions, settings, network use). |
| `ui/src/device-tab.svelte` | The whole tab: rows, choices, printer picker, download. |
| `ui/src/api.ts` | The calls to the module's routes and to Materials. |
| `ui/src/labels.ts`, `variants.ts`, `printer-tools.ts` | How tools and nozzles are named, and the tools as a printer reports them. |

## From download to bundle

1. **Configure.** The host sends the settings and a data directory. The module loads the catalogue it already has on disk, and starts a download in the background if a source file is older than `refresh_days`.
2. **Sources** (`source.go`). The Prusa Research bundle is always loaded: it holds the printers and the generic presets. The `sources` setting adds Polymaker's presets and bundles at the addresses of `custom_urls`. A source that fails is reported and the others still load. Every address the module contacts is declared under `egress` in `module.yaml`.
3. **Catalogue** (`ini.go`). Each source is parsed into sections. A section whose name starts with `*` is abstract, a base for others, and is never offered. Sections inherit with `inherits = a; b`.
4. **Match.** For a spool, `BestMatches` compares the brand, the product name and the type of material with the presets, and returns the nearest. Matching is limited to the same brand and type of material.
5. **Fit** (`compat.go`). A preset is offered for a printer only if its `compatible_printers_condition` is true for the printer preset the person chose, or its explicit `compatible_printers` list names it. The file reads the same expression language PrusaSlicer does, using only the standard library.
6. **Settings** (`bundle.go`). `Settings` flattens the chosen preset (parents first, its own keys win), adapts it to the printer, and lays the person's edits and the maker's temperatures over it. An edit is checked before it is accepted.
7. **Bundle** (`bundle.go`). `Bundle` writes one `[filament:NAME]` section per spool, each flattened so that it imports without its parents. The `[presets]` section selects the printer and the filament of each extruder (`filament`, `filament_1`, and so on). With **Colour the extruders**, it also writes a complete copy of the printer preset with the spools' colours and selects that copy.

Two details matter when changing this code:

- PrusaSlicer does not resolve `inherits` when it imports a user preset from a bundle. A key that is not in the section takes its default. That is why every section is flattened and why the printer copy is complete.
- A preset with no variant for the printer is marked incompatible in PrusaSlicer. The bundle therefore uses the section made for the exact printer, and when there is none, lays the preset's material settings over a generic one (a candidate flagged `adapted`).

## What is kept

- The downloaded sources and the catalogue's files, in the data directory.
- What a person chose on a printer's tab, in `state/<device>.json` of that directory, through `GET` and `POST /choices`. The host reserves `/state`, so the route has another name. A choice is kept until the person changes that tool, and a changed spool drops it.
- In the browser, the chosen PrusaSlicer printer, as a second place besides the module's.

## The page

`device-tab.svelte` builds one row for each tool that holds a spool. A row carries the brand, the material, the colour, the nearest presets, the chosen preset and the edits. Every change is saved after 300 ms. The page asks the module for the settings of a preset again when the preset, the printer or the maker's temperatures change, and ignores an older answer that arrives late.

All text goes through `host.t` and `src/locales/en.json`.

## Building and testing

```sh
git clone --depth 1 --branch sdk/v0.1.0-beta.2 https://github.com/prusa3d/prusa-connect.git .core
go vet ./... && go test -race ./...
BUILD_ONLY=1 sh install.sh
```

The version appears in `module.yaml`, `main.go` and `openapi.json`, and the top heading of `CHANGELOG.md` must match. `sh ../scripts/check-version.sh filament-profile` checks them.

## Where to change what

| To change | Start in |
|---|---|
| A new source of presets | `source.go`, then `module.yaml` (`egress` and `options`) |
| Which preset is offered for a spool | `BestMatches` in `ini.go`, `compat.go` |
| What is written in a profile | `bundle.go`, `properties.go` |
| The printer copy with colours | `printer.go` |
| A route | `main.go`, then `openapi.json`, then `api.ts` |
| A sentence on screen | `ui/src/locales/en.json` |

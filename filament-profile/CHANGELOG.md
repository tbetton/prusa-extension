# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.1.12

- Require the Materials extension (`com.github.aux.prusa3d.materials`, `>=0.2`). Installing Filament Profile installs it when it is missing, and the install plan lists it for approval. Filament Profile does not start without it, and Materials cannot be removed or switched off while Filament Profile is installed.

## 0.1.11

- Keep the choices of a tool until that tool is changed. A printer that reports nothing yet when the tab opens no longer wipes them, and without the Materials extension a choice follows the tool's type of material.

## 0.1.10

- Fix the choices of a printer's tab not coming back (HTTP 501, `STATE_NOT_CONFIGURED`): the host keeps `/state` for its own module state, so the choices are now read and kept at `GET` and `POST /choices`.

## 0.1.9

- Fix the choices of a printer's tab not being kept (HTTP 501): `GET` and `POST /state` now take the device as a `device` query parameter, not as a path segment.

## 0.1.8

- Keep the choices of a printer's tab with `POST /state` instead of `PUT`, and say on the tab when they cannot be kept or read back, instead of failing without a word.

## 0.1.7

- Fix the module refusing to start because `GET /state` had no description in its OpenAPI fragment.

## 0.1.6

- Keep what is chosen on the **Filament Profile** tab of a printer: the printer, and for each tool its profile, its name, its edited settings and whether it is included. It comes back after a change of page and after the module restarts (`GET` and `POST /state`). A tool whose spool changed starts again.

## 0.1.5

- Remove the warning that a profile is not made for the printer, on every tool of the **Filament Profile** tab.

## 0.1.4

- List only the profiles of the spool's brand and material type, and the generic profile of that type when there is none. `GET /match` no longer returns a preset of another material type.

## 0.1.3

- Search the printer as you type on the **Filament Profile** tab: the printer presets that match appear under the field, and one click chooses it.
- List only the profiles that may suit each spool, and the generic profile of its type. Every other profile is found with the search field.

## 0.1.2

- Rework the **Filament Profile** tab. The printer search now only chooses the printer the `.ini` is written for. Each tool is one line (number, material type, colour, nozzle); a click opens its profile below. The list shows every profile, with a search field, whatever the printer. Beside it are the settings written to the `.ini` (temperatures, density, fan, retraction…), and each can be edited (`POST /settings`, `overrides` on `POST /bundle`). A profile that fits no section of the chosen printer is still written, laid over its generic preset.

## 0.1.1

- Choose the PrusaSlicer printer on the **Filament Profile** tab, from the printer presets of the Prusa profiles (`GET /printers`). The tab offers only the filament presets PrusaSlicer itself lists for that printer: each preset's compatibility condition is evaluated against the printer, so a preset made for another printer is no longer offered and no longer flagged red. `GET /match` and `GET /presets` take a `printer`, and `POST /bundle` takes a `printer` instead of a variant per filament.
- Force that printer in the exported config: the bundle's `[presets]` section now selects the printer, then each profile in the filament of its tool.
- Add sources of filament presets, chosen with the new `sources` setting (default `prusa,polymaker`): `prusa`, `polymaker` and `custom`. The Prusa bundle is always loaded, for its printers and generic presets. Polymaker's own presets (`Polymaker3D/Polymaker-Preset`, MIT) are read from `polymaker_url`; they are made for the CORE One, and on a printer of that family that does not list them, their temperatures, fans and retraction are laid over the printer's generic preset, with a flow no higher than that preset's. `custom_urls` adds PrusaSlicer config bundles of your own (https, at most 8).
- Each preset in the tab names its source, and an adapted preset says so.
- The module keeps up to 128 MiB, for the extra sources and the compatibility cache.
- Work without the Materials extension. When it is not installed, stopped, or does not follow the printer, the **Filament Profile** tab reads the tools from the printer itself: the nozzle and the type of material it reports (PLA, PETG…). The profiles then start from the generic preset of that type. The brand, the product, the colour and the maker's temperatures need Materials, and the tab says so.
- Say what a preset search found on the **Filament Profile** tab: how many presets matched, and how many fit the printer and the nozzle. A search used to hide every preset that has no variant for the printer, without a word, and replace the list already offered. It now adds to that list.
- Read each spool's material from the Materials extension (`GET /materials/{uuid}`) and, when the OpenPrintTag catalogue gives them, use its nozzle and bed temperatures and its density in the profile. The nozzle temperature is the middle of the maker's range, rounded to 5 °C, and the first layer keeps the margin of the preset. A **Use the maker's temperatures** box on each tool turns it off. It is on by default for a generic preset, and off for a Prusa preset of the brand, which is already tuned.
- Accept `properties` on each filament of `POST /bundle`. Values outside a believable range (nozzle 150–350 °C, bed 20–140 °C, density 0.5–5) are ignored.
- Offer only the Prusa presets that fit the printer and the tool's nozzle on the **Filament Profile** tab, so an imported profile is no longer flagged incompatible by PrusaSlicer. A tool with no fitting preset says so.
- `GET /match` returns presets of the spool's brand only, up to twenty, instead of presets of other brands that share a few words.
- Select each imported profile as the filament of its tool in PrusaSlicer. The bundle written by `POST /bundle` carries a `[presets]` section, and each filament in the request takes a `slot`, from 1, in the printer's tool order: Tool 1 is filament 1, Tool 2 is filament 2. The tab sends the slots itself.
- Number the tools from 1 on the **Filament Profile** tab, as the Materials extension and the printer's Tools tab do. A printer with a single tool shows **Extruder**, and a slot of an MMU shows **Tool n · Sm**.
- Add a **Filament Profile** tab to a printer's page. It lists the filament on each tool, read from the Materials extension, and proposes the nearest Prusa preset for each.
- Write a PrusaSlicer 2.9 Config Bundle (`.ini`) with one profile per tool, through `POST /bundle`. Each profile is flattened, so it imports without its parents.
- Download the Prusa Research profiles for PrusaSlicer 2.x on first start, and again after `refresh_days` days (default 7). The address is the `catalogue_url` setting. `POST /catalogue/refresh` downloads them on demand.

# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.2.1

- Offer only the Prusa presets that fit the printer and the tool's nozzle on the **Filament Profile** tab, so an imported profile is no longer flagged incompatible by PrusaSlicer. A tool with no fitting preset says so.
- `GET /match` returns presets of the spool's brand only, up to twenty, instead of presets of other brands that share a few words.

## 0.2.0

- Select each imported profile as the filament of its tool in PrusaSlicer. The bundle written by `POST /bundle` carries a `[presets]` section, and each filament in the request takes a `slot`, from 1, in the printer's tool order: Tool 1 is filament 1, Tool 2 is filament 2. The tab sends the slots itself.

## 0.1.1

- Number the tools from 1 on the **Filament Profile** tab, as the Materials extension and the printer's Tools tab do. A printer with a single tool shows **Extruder**, and a slot of an MMU shows **Tool n · Sm**.

## 0.1.0

- Add a **Filament Profile** tab to a printer's page. It lists the filament on each tool, read from the Materials extension, and proposes the nearest Prusa preset for each.
- Write a PrusaSlicer 2.9 Config Bundle (`.ini`) with one profile per tool, through `POST /bundle`. Each profile is flattened, so it imports without its parents.
- Download the Prusa Research profiles for PrusaSlicer 2.x on first start, and again after `refresh_days` days (default 7). The address is the `catalogue_url` setting. `POST /catalogue/refresh` downloads them on demand.

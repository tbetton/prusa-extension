# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.1.0

- Add a **Filament Profile** tab to a printer's page. It lists the filament on each tool, read from the Materials extension, and proposes the nearest Prusa preset for each.
- Write a PrusaSlicer 2.9 Config Bundle (`.ini`) with one profile per tool, through `POST /bundle`. Each profile is flattened, so it imports without its parents.
- Download the Prusa Research profiles for PrusaSlicer 2.x on first start, and again after `refresh_days` days (default 7). The address is the `catalogue_url` setting. `POST /catalogue/refresh` downloads them on demand.

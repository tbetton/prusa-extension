# Filament Profile for Prusa Connect Local

Filament Profile writes PrusaSlicer **2.9** filament profiles for the spools loaded on a printer. It adds a **Filament Profile** tab to a printer's page, next to the tools. For each tool it proposes the nearest preset of the profiles Prusa Research publishes, and writes one Config Bundle (`.ini`) you import in PrusaSlicer.

1. Assign a material to each tool in the **Materials** extension, which gives the brand, the product, the colour and the maker's temperatures. Without Materials, the tab uses the type of material the printer reports for each tool (PLA, PETG…), and you name each profile yourself.
2. Open the **Filament Profile** tab of the printer.
3. Pick the printer preset your PrusaSlicer uses, for example `Prusa CORE One INDX 8T HF0.4 nozzle`. The tab then offers only the filament presets PrusaSlicer lists for that printer, and the file forces the printer.
4. Check the profile name and the preset for each tool, then download the `.ini`.
5. In PrusaSlicer 2.9, choose **File → Import → Import Config Bundle**. Each profile is selected as the filament of its tool, in the printer's order: Tool 1 is filament 1, Tool 2 is filament 2, and so on. The selected printer must have as many extruders as the tools you import.

When Prusa has no preset for a brand, the extension starts from the generic preset of the material (`Generic PLA`, `Generic PETG`, ...), then sets the profile's name, vendor, and colour. Review the temperatures and speeds before you print: the numbers are Prusa's, not the brand's.

## Sources

The `sources` setting (default `prusa,polymaker`) chooses where filament presets come from:

- `prusa`: the presets Prusa Research publishes for PrusaSlicer 2.x. The bundle is always loaded, because it holds the printers and the generic presets the others are adapted on.
- `polymaker`: Polymaker's own PrusaSlicer presets (`Polymaker3D/Polymaker-Preset`, MIT), from `polymaker_url`. They are made for the CORE One. On another printer of that family, such as the CORE One INDX, the extension lays their temperatures, fans and retraction over the printer's generic preset and keeps the flow no higher than that preset's: check them before you print.
- `custom`: PrusaSlicer config bundles at the https addresses of `custom_urls`.

## Data

- Source: the Prusa Research profile archive for PrusaSlicer 2.x, plus the sources above. The extension downloads it from `catalogue_url` and keeps the newest bundle in its data directory.
- The extension reads the tools from the Materials extension (`GET /printers/{device}/tools`), or from the printer's own report when Materials is not available. It stores nothing about your printers.
- Not supported yet: LayerHub (its API needs an account), and PrusaSlicer 3, which uses a different, YAML-based profile format.

The extension displays its interface and error messages in English.

## Build and install

Check out the official SDK into `.core/`. Run `BUILD_ONLY=1 sh install.sh` to build the manifest, native binary, and UI bundle into `ui/dist`. Run `go vet ./...` and `go test -race ./...` to verify the Go side.

# Filament Profile for Prusa Connect Local

Filament Profile writes PrusaSlicer **2.9** filament profiles for the spools loaded on a printer. It adds a **Filament Profile** tab to a printer's page, next to the tools. For each tool it proposes the nearest preset of the profiles Prusa Research publishes, and writes one Config Bundle (`.ini`) you import in PrusaSlicer.

1. Assign a material to each tool in the **Materials** extension (or let the printer report it).
2. Open the **Filament Profile** tab of the printer.
3. Pick the printer your PrusaSlicer uses. The extension chooses the variant for each tool's nozzle (`HF0.4`, `0.6`, ...).
4. Check the profile name and the preset for each tool, then download the `.ini`.
5. In PrusaSlicer 2.9, choose **File → Import → Import Config Bundle**. Each profile is selected as the filament of its tool, in the printer's order: Tool 1 is filament 1, Tool 2 is filament 2, and so on. The selected printer must have as many extruders as the tools you import.

When Prusa has no preset for a brand, the extension starts from the generic preset of the material (`Generic PLA`, `Generic PETG`, ...), then sets the profile's name, vendor, and colour. Review the temperatures and speeds before you print: the numbers are Prusa's, not the brand's.

## Data

- Source: the Prusa Research profile archive for PrusaSlicer 2.x. The extension downloads it from `catalogue_url` and keeps the newest bundle in its data directory.
- The extension reads the tools from the Materials extension (`GET /printers/{device}/tools`). It stores nothing about your printers.
- Not supported yet: LayerHub (its API needs an account), and PrusaSlicer 3, which uses a different, YAML-based profile format.

The extension displays its interface and error messages in English.

## Build and install

Check out the official SDK into `.core/`. Run `BUILD_ONLY=1 sh install.sh` to build the manifest, native binary, and UI bundle into `ui/dist`. Run `go vet ./...` and `go test -race ./...` to verify the Go side.

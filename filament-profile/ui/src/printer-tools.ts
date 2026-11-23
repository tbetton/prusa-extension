// The tools as the printer itself reports them, for when the Materials
// extension is not there: the nozzle, and the type of the material loaded
// (PLA, PETG...). The brand, the product and the colour are not in what a
// printer says.

/** The members of the core's DeviceTool this module reads. */
export interface ReportedTool {
  active: boolean;
  features?: {
    nozzle_diameter?: number;
    nozzle_hardened?: boolean;
    nozzle_high_flow?: boolean;
    nozzle_high_temp?: boolean;
  };
  feeder?: unknown;
  material_package_instance?: { package: { material: { abbreviation: string } } } | null;
}

export interface PrinterTool {
  id: string;
  active: boolean;
  assignment: null;
  reported: { type: string; abbreviation: string; color_rgba: string } | null;
  nozzle: { diameter_mm: number; high_flow: boolean; hardened: boolean; high_temp: boolean };
}

export const toolsFromTree = (tree: Record<string, ReportedTool | null>): PrinterTool[] =>
  Object.entries(tree).flatMap(([id, tool]) => {
    // A head that carries a feeder holds no material: its slots do.
    if (tool === null || (tool.feeder !== undefined && tool.feeder !== null)) return [];

    // A slot prints through the nozzle of the head above it.
    const features = tool.features ?? tree[id.split('.')[0] ?? '']?.features;
    const type = tool.material_package_instance?.package.material.abbreviation ?? '';

    return [
      {
        id,
        active: tool.active,
        assignment: null,
        reported: type === '' ? null : { type, abbreviation: type, color_rgba: '' },
        nozzle: {
          diameter_mm: features?.nozzle_diameter ?? 0.4,
          high_flow: features?.nozzle_high_flow ?? false,
          hardened: features?.nozzle_hardened ?? false,
          high_temp: features?.nozzle_high_temp ?? false,
        },
      },
    ];
  });

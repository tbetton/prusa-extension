// How a tool is named on screen. The Materials extension identifies tools from
// 0: "0", "1", ... or "head.slot" for a slot of an MMU. A person counts from 1,
// as its own tiles do ("Tool 1", "S1"), and a printer with one tool says
// "Extruder".

export interface ToolName {
  /** Tool number, from 1. Undefined for the only tool of a printer. */
  tool?: number;
  /** Slot number of a feeder, from 1. */
  slot?: number;
}

export const toolName = (id: string, single: boolean): ToolName => {
  const [head = '0', slot] = id.split('.');
  if (slot !== undefined) return { tool: Number(head) + 1, slot: Number(slot) + 1 };

  return single ? {} : { tool: Number(head) + 1 };
};

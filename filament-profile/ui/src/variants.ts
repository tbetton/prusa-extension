// How a nozzle is named: "0.4", "HF0.4" for a high-flow one, "HT0.4" for a
// high-temperature one. The names of Prusa's printer presets use the same
// words.

export interface Nozzle {
  diameter_mm: number;
  high_flow: boolean;
  high_temp?: boolean;
}

export const nozzleToken = (nozzle: Nozzle): string =>
  `${nozzle.high_flow ? 'HF' : nozzle.high_temp === true ? 'HT' : ''}${String(nozzle.diameter_mm)}`;

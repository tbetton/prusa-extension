// Which printer variant of a Prusa preset fits a tool. The names of the Prusa
// bundle end with the printer family and the nozzle, as in
// "Prusament PLA @COREONEINDX HF0.4", "@XL 0.6", or just "@HF0.4". A family
// alone ("@COREONEL", "@PG") stands for its standard 0.4 mm nozzle.

export interface Nozzle {
  diameter_mm: number;
  high_flow: boolean;
  high_temp?: boolean;
}

/** "HF0.4" for a high-flow 0.4 mm nozzle, "HT0.4" for a high-temperature one, "0.4" otherwise. */
export const nozzleToken = (nozzle: Nozzle): string =>
  `${nozzle.high_flow ? 'HF' : nozzle.high_temp === true ? 'HT' : ''}${String(nozzle.diameter_mm)}`;

/** The printer family of a variant: "COREONEINDX HF0.4" is "COREONEINDX". */
export const familyOf = (variant: string): string =>
  variant.replace(/(?:^|\s+)(?:HF|HT)?\d+\.\d+$/u, '').trim();

/** Every family the variants name, sorted, without duplicates. */
export const familiesOf = (variants: string[]): string[] =>
  [...new Set(variants.map(familyOf))].filter((family) => family !== '').sort();

/**
 * The variant of one preset for a printer family and a nozzle: the family and
 * nozzle named exactly, or the bare family when the nozzle is the standard
 * 0.4 mm one. Empty when the preset has none, which writes the preset's own
 * settings. A bare family is never taken for another nozzle: the settings of
 * a 0.4 mm nozzle would be wrong for a 0.8 mm one.
 */
export const pickVariant = (variants: string[], family: string, nozzle: Nozzle): string => {
  const token = nozzleToken(nozzle);
  const exact = family === '' ? token : `${family} ${token}`;
  const standard = nozzle.diameter_mm === 0.4 && !nozzle.high_flow && nozzle.high_temp !== true;
  const wanted = standard && family !== '' ? [exact, family] : [exact];

  return wanted.find((variant) => variants.includes(variant)) ?? '';
};

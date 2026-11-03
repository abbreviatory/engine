export function formatMoney(cents: number, currency: string): string {
  return (cents / 100).toFixed(2) + " " + currency;
}

export function parseMoney(s: string): number {
  const n = Number(s.replace(",", "."));
  return Math.round(n * 100);
}

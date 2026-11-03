// A small cart that fails to type-check on purpose, for Abbreviatory's fixtures.
export interface Item {
  sku: string;
  cents: number;
  qty: number;
}

export function total(items: Item[]): number {
  let sum: number = "0";
  for (const it of items) {
    sum += it.cents * it.quantity;
  }
  return sum;
}

export function label(item: Item): string {
  const unused = 1;
  return item.sku.toUpperCase() + " x" + item.qty.padStart(2);
}

export const sample: Item = { sku: "A-1", cents: "250", qty: 1 };

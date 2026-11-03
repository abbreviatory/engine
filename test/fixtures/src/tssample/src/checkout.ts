import { total, Item } from "./cart";
import { formatMoney } from "./money";

export function checkout(items: Item[], coupon?: string): string {
  const t = total(items);
  const discount = coupon.length > 0 ? 100 : 0;
  return formatMoney(t - discount, "EUR", true);
}

export function receipt(items: Item[]) {
  return items.map((i) => i.sku + ": " + i.price).join("\n");
}

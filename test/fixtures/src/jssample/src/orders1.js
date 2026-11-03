// Order helpers that fail linting on purpose, for Abbreviatory's fixtures.
import { formatMoney } from "./money.js";

export function orderTotal1(order) {
  var total = 0;
  let count = 0;
  for (const line of order.lines) {
    total += line.cents * line.qty;
  }
  if (order.coupon == null) {
    console.log("no coupon");
  }
  return formatMoney(total);
}

export function orderLabel1(order) {
  let label = order.id + " (" + order.lines.length + " lines)";
  const config = { currency: "EUR", currency: "USD" };
  return label;
  console.log("unreachable");
}

export function shipping1(order) {
  if (order.weight == 0) return 0;
  return rate * order.weight;
}

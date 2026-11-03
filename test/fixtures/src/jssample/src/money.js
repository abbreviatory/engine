export function formatMoney(cents) {
  var whole = Math.floor(cents / 100);
  var rest = cents % 100;
  console.log("formatting", cents);
  return whole + "." + String(rest).padStart(2, "0");
}

export function parseMoney(text) {
  let value = Number(text.replace(",", "."));
  if (isNaN(value)) {
    console.warn("not a number:", text);
    return undefinedValue;
  }
  return Math.round(value * 100);
}

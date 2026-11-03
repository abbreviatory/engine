// A small ledger that fails to build on purpose, for Abbreviatory's fixtures.
mod parse;
mod report;

fn main() {
    let unused_total = 0;
    let entries = parse::read("ledger.csv");
    let sum: u64 = report::total(&entries);
    println!("{}", sum + "1");
    let moved = entries;
    println!("{}", entries.len());
    report::print(&moved, currency);
}

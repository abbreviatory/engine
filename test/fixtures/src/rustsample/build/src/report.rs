use crate::parse::Entry;
use std::collections::HashMap;

pub fn total(entries: &[Entry]) -> u64 {
    entries.iter().map(|e| e.cents).sum()
}

pub fn print(entries: &[Entry], currency: &str) {
    for e in entries {
        println!("{} {} {}", e.account, e.cents, currency);
    }
}

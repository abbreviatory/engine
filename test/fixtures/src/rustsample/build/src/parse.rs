pub struct Entry {
    pub account: String,
    pub cents: i64,
    pub memo: String,
}

pub fn read(path: &str) -> Vec<Entry> {
    let mut out = Vec::new();
    let text = std::fs::read_to_string(path).unwrap_or_default();
    for line in text.lines() {
        let parts: Vec<&str> = line.split(',').collect();
        let cents: i64 = parts[1].parse();
        out.push(Entry { account: parts[0].to_string(), cents, memo: parts[2] });
    }
    out
}

pub fn unused_one(x: i32) -> i32 { let y = 1; x }
pub fn unused_two(x: i32) -> i32 { let y = 2; x }
pub fn unused_three(x: i32) -> i32 { let y = 3; x }

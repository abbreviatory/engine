//! Money parsing for Abbreviatory's fixtures; some tests fail on purpose.

pub fn parse_cents(s: &str) -> Result<i64, String> {
    let s = s.trim();
    let (whole, frac) = match s.split_once('.') {
        Some((w, f)) => (w, f),
        None => (s, "0"),
    };
    let w: i64 = whole.parse().map_err(|e| format!("bad amount {:?}: {}", s, e))?;
    let f: i64 = frac.parse().map_err(|e| format!("bad cents {:?}: {}", s, e))?;
    Ok(w * 100 + f)
}

pub fn round_half_up(cents: i64, step: i64) -> i64 {
    (cents / step) * step
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_1() {
        assert_eq!(parse_cents("1.00").unwrap(), 100);
    }

    #[test]
    fn parses_2() {
        assert_eq!(parse_cents("2.00").unwrap(), 200);
    }

    #[test]
    fn parses_3() {
        assert_eq!(parse_cents("3.00").unwrap(), 300);
    }

    #[test]
    fn parses_4() {
        assert_eq!(parse_cents("4.00").unwrap(), 400);
    }

    #[test]
    fn parses_5() {
        assert_eq!(parse_cents("5.00").unwrap(), 500);
    }

    #[test]
    fn parses_6() {
        assert_eq!(parse_cents("6.00").unwrap(), 600);
    }

    #[test]
    fn parses_7() {
        assert_eq!(parse_cents("7.00").unwrap(), 700);
    }

    #[test]
    fn parses_8() {
        assert_eq!(parse_cents("8.00").unwrap(), 800);
    }

    #[test]
    fn parses_9() {
        assert_eq!(parse_cents("9.00").unwrap(), 900);
    }

    #[test]
    fn parses_10() {
        assert_eq!(parse_cents("10.00").unwrap(), 1000);
    }

    #[test]
    fn parses_11() {
        assert_eq!(parse_cents("11.00").unwrap(), 1100);
    }

    #[test]
    fn parses_12() {
        assert_eq!(parse_cents("12.00").unwrap(), 1200);
    }

    #[test]
    fn parses_13() {
        assert_eq!(parse_cents("13.00").unwrap(), 1300);
    }

    #[test]
    fn parses_14() {
        assert_eq!(parse_cents("14.00").unwrap(), 1400);
    }

    #[test]
    fn parses_15() {
        assert_eq!(parse_cents("15.00").unwrap(), 1500);
    }

    #[test]
    fn parses_16() {
        assert_eq!(parse_cents("16.00").unwrap(), 1600);
    }

    #[test]
    fn parses_17() {
        assert_eq!(parse_cents("17.00").unwrap(), 1700);
    }

    #[test]
    fn parses_18() {
        assert_eq!(parse_cents("18.00").unwrap(), 1800);
    }

    #[test]
    fn parses_19() {
        assert_eq!(parse_cents("19.00").unwrap(), 1900);
    }

    #[test]
    fn parses_20() {
        assert_eq!(parse_cents("20.00").unwrap(), 2000);
    }

    #[test]
    fn parses_21() {
        assert_eq!(parse_cents("21.00").unwrap(), 2100);
    }

    #[test]
    fn parses_22() {
        assert_eq!(parse_cents("22.00").unwrap(), 2200);
    }

    #[test]
    fn parses_23() {
        assert_eq!(parse_cents("23.00").unwrap(), 2300);
    }

    #[test]
    fn parses_24() {
        assert_eq!(parse_cents("24.00").unwrap(), 2400);
    }

    #[test]
    fn parses_25() {
        assert_eq!(parse_cents("25.00").unwrap(), 2500);
    }

    #[test]
    fn parses_26() {
        assert_eq!(parse_cents("26.00").unwrap(), 2600);
    }

    #[test]
    fn parses_27() {
        assert_eq!(parse_cents("27.00").unwrap(), 2700);
    }

    #[test]
    fn parses_28() {
        assert_eq!(parse_cents("28.00").unwrap(), 2800);
    }

    #[test]
    fn parses_29() {
        assert_eq!(parse_cents("29.00").unwrap(), 2900);
    }

    #[test]
    fn parses_30() {
        assert_eq!(parse_cents("30.00").unwrap(), 3000);
    }

    #[test]
    fn parses_31() {
        assert_eq!(parse_cents("31.00").unwrap(), 3100);
    }

    #[test]
    fn parses_32() {
        assert_eq!(parse_cents("32.00").unwrap(), 3200);
    }

    #[test]
    fn parses_33() {
        assert_eq!(parse_cents("33.00").unwrap(), 3300);
    }

    #[test]
    fn parses_34() {
        assert_eq!(parse_cents("34.00").unwrap(), 3400);
    }

    #[test]
    fn parses_35() {
        assert_eq!(parse_cents("35.00").unwrap(), 3500);
    }

    #[test]
    fn parses_36() {
        assert_eq!(parse_cents("36.00").unwrap(), 3600);
    }

    #[test]
    fn parses_37() {
        assert_eq!(parse_cents("37.00").unwrap(), 3700);
    }

    #[test]
    fn parses_38() {
        assert_eq!(parse_cents("38.00").unwrap(), 3800);
    }

    #[test]
    fn parses_39() {
        assert_eq!(parse_cents("39.00").unwrap(), 3900);
    }

    #[test]
    fn parses_40() {
        assert_eq!(parse_cents("40.00").unwrap(), 4000);
    }

    #[test]
    fn parses_41() {
        assert_eq!(parse_cents("41.00").unwrap(), 4100);
    }

    #[test]
    fn parses_42() {
        assert_eq!(parse_cents("42.00").unwrap(), 4200);
    }

    #[test]
    fn parses_43() {
        assert_eq!(parse_cents("43.00").unwrap(), 4300);
    }

    #[test]
    fn parses_44() {
        assert_eq!(parse_cents("44.00").unwrap(), 4400);
    }

    #[test]
    fn parses_45() {
        assert_eq!(parse_cents("45.00").unwrap(), 4500);
    }

    #[test]
    fn parses_46() {
        assert_eq!(parse_cents("46.00").unwrap(), 4600);
    }

    #[test]
    fn parses_47() {
        assert_eq!(parse_cents("47.00").unwrap(), 4700);
    }

    #[test]
    fn parses_48() {
        assert_eq!(parse_cents("48.00").unwrap(), 4800);
    }

    #[test]
    fn parses_49() {
        assert_eq!(parse_cents("49.00").unwrap(), 4900);
    }

    #[test]
    fn parses_50() {
        assert_eq!(parse_cents("50.00").unwrap(), 5000);
    }

    #[test]
    fn parses_51() {
        assert_eq!(parse_cents("51.00").unwrap(), 5100);
    }

    #[test]
    fn parses_52() {
        assert_eq!(parse_cents("52.00").unwrap(), 5200);
    }

    #[test]
    fn parses_53() {
        assert_eq!(parse_cents("53.00").unwrap(), 5300);
    }

    #[test]
    fn parses_54() {
        assert_eq!(parse_cents("54.00").unwrap(), 5400);
    }

    #[test]
    fn parses_55() {
        assert_eq!(parse_cents("55.00").unwrap(), 5500);
    }

    #[test]
    fn parses_56() {
        assert_eq!(parse_cents("56.00").unwrap(), 5600);
    }

    #[test]
    fn parses_57() {
        assert_eq!(parse_cents("57.00").unwrap(), 5700);
    }

    #[test]
    fn parses_58() {
        assert_eq!(parse_cents("58.00").unwrap(), 5800);
    }

    #[test]
    fn parses_59() {
        assert_eq!(parse_cents("59.00").unwrap(), 5900);
    }

    #[test]
    fn parses_60() {
        assert_eq!(parse_cents("60.00").unwrap(), 6000);
    }

    #[test]
    fn parses_comma() {
        assert_eq!(parse_cents("1,50").unwrap(), 150);
    }

    #[test]
    fn parses_one_digit_cents() {
        assert_eq!(parse_cents("1.5").unwrap(), 150);
    }

    #[test]
    fn rounds_half_up() {
        assert_eq!(round_half_up(15, 10), 20);
    }

    #[test]
    fn rounds_down() {
        assert_eq!(round_half_up(14, 10), 10);
    }

    #[test]
    fn negative_amounts() {
        let v: Vec<i64> = vec![];
        assert_eq!(v[0], parse_cents("-1.00").unwrap());
    }

    #[test]
    #[ignore]
    fn huge_amounts() {
        assert!(parse_cents("99999999999999999999").is_err());
    }
}

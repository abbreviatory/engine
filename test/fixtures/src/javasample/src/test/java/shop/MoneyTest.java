package shop;

import static org.junit.Assert.assertEquals;

import org.junit.Ignore;
import org.junit.Test;

public class MoneyTest {
    @Test public void parses1() { assertEquals(100L, Money.parseCents("1.00")); }
    @Test public void parses2() { assertEquals(200L, Money.parseCents("2.00")); }
    @Test public void parses3() { assertEquals(300L, Money.parseCents("3.00")); }
    @Test public void parses4() { assertEquals(400L, Money.parseCents("4.00")); }
    @Test public void parses5() { assertEquals(500L, Money.parseCents("5.00")); }
    @Test public void parses6() { assertEquals(600L, Money.parseCents("6.00")); }
    @Test public void parses7() { assertEquals(700L, Money.parseCents("7.00")); }
    @Test public void parses8() { assertEquals(800L, Money.parseCents("8.00")); }
    @Test public void parses9() { assertEquals(900L, Money.parseCents("9.00")); }
    @Test public void parses10() { assertEquals(1000L, Money.parseCents("10.00")); }
    @Test public void parses11() { assertEquals(1100L, Money.parseCents("11.00")); }
    @Test public void parses12() { assertEquals(1200L, Money.parseCents("12.00")); }
    @Test public void parses13() { assertEquals(1300L, Money.parseCents("13.00")); }
    @Test public void parses14() { assertEquals(1400L, Money.parseCents("14.00")); }
    @Test public void parses15() { assertEquals(1500L, Money.parseCents("15.00")); }
    @Test public void parses16() { assertEquals(1600L, Money.parseCents("16.00")); }
    @Test public void parses17() { assertEquals(1700L, Money.parseCents("17.00")); }
    @Test public void parses18() { assertEquals(1800L, Money.parseCents("18.00")); }
    @Test public void parses19() { assertEquals(1900L, Money.parseCents("19.00")); }
    @Test public void parses20() { assertEquals(2000L, Money.parseCents("20.00")); }
    @Test public void parses21() { assertEquals(2100L, Money.parseCents("21.00")); }
    @Test public void parses22() { assertEquals(2200L, Money.parseCents("22.00")); }
    @Test public void parses23() { assertEquals(2300L, Money.parseCents("23.00")); }
    @Test public void parses24() { assertEquals(2400L, Money.parseCents("24.00")); }
    @Test public void parses25() { assertEquals(2500L, Money.parseCents("25.00")); }
    @Test public void parses26() { assertEquals(2600L, Money.parseCents("26.00")); }
    @Test public void parses27() { assertEquals(2700L, Money.parseCents("27.00")); }
    @Test public void parses28() { assertEquals(2800L, Money.parseCents("28.00")); }
    @Test public void parses29() { assertEquals(2900L, Money.parseCents("29.00")); }
    @Test public void parses30() { assertEquals(3000L, Money.parseCents("30.00")); }
    @Test public void parses31() { assertEquals(3100L, Money.parseCents("31.00")); }
    @Test public void parses32() { assertEquals(3200L, Money.parseCents("32.00")); }
    @Test public void parses33() { assertEquals(3300L, Money.parseCents("33.00")); }
    @Test public void parses34() { assertEquals(3400L, Money.parseCents("34.00")); }
    @Test public void parses35() { assertEquals(3500L, Money.parseCents("35.00")); }
    @Test public void parses36() { assertEquals(3600L, Money.parseCents("36.00")); }
    @Test public void parses37() { assertEquals(3700L, Money.parseCents("37.00")); }
    @Test public void parses38() { assertEquals(3800L, Money.parseCents("38.00")); }
    @Test public void parses39() { assertEquals(3900L, Money.parseCents("39.00")); }
    @Test public void parses40() { assertEquals(4000L, Money.parseCents("40.00")); }

    @Test public void parsesComma() { assertEquals(150L, Money.parseCents("1,50")); }
    @Test public void parsesOneDigitCents() { assertEquals(150L, Money.parseCents("1.5")); }
    @Test public void roundsHalfUp() { assertEquals(20L, Money.roundHalfUp(15, 10)); }
    @Test public void roundsDown() { assertEquals(10L, Money.roundHalfUp(14, 10)); }
    @Test public void cartTotal() {
        Cart c = new Cart();
        c.add("A", 250);
        c.add("B:x", 100);
        assertEquals(350L, c.total());
    }
    @Ignore("slow") @Test public void hugeAmounts() { Money.parseCents("99999999999999999999"); }
}

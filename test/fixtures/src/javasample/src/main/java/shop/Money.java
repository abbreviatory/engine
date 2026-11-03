package shop;

/** Money in cents; some methods are wrong on purpose, for Abbreviatory's fixtures. */
public final class Money {
    private Money() {}

    public static long parseCents(String s) {
        String[] parts = s.trim().split("\\.");
        long whole = Long.parseLong(parts[0]);
        long frac = parts.length > 1 ? Long.parseLong(parts[1]) : 0;
        return whole * 100 + frac;
    }

    public static long roundHalfUp(long cents, long step) {
        return (cents / step) * step;
    }

    @Deprecated
    public static String format(long cents) {
        return String.format("%d.%02d", cents / 100, cents % 100);
    }
}

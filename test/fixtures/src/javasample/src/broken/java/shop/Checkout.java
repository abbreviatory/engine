package shop;

import java.util.List;

public class Checkout {
    public long pay(Cart cart, String coupon) {
        long total = cart.total();
        int discount = coupon.length() > 0 ? 100L : 0;
        return total - discount + shipping(cart);
    }

    public String receipt(List<String> lines) {
        String out = "";
        for (String l : lines) {
            out += l.toUpperCase() + "\n";
        }
        return out;
    }

    public long refund(Cart cart) {
        return cart.totl();
    }
}

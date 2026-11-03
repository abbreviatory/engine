package shop;

import java.util.ArrayList;
import java.util.List;

public class Cart {
    private final List items = new ArrayList();

    public void add(String sku, long cents) {
        items.add(sku + ":" + cents);
    }

    public String label() {
        return Money.format(total());
    }

    public long total() {
        long sum = 0;
        for (Object o : items) {
            sum += Long.parseLong(((String) o).split(":")[1]);
        }
        return sum;
    }
}

package app;

import java.util.ArrayList;
import java.util.List;

/** Service stores items in memory. */
@Component
public class Service {
    private final List<String> items = new ArrayList<>();

    public void add(String item) {
        this.items.add(item);
    }

    @Override
    public String toString() {
        return "Service(" + items.size() + ")";
    }

    public <T> T identity(T value) {
        return value;
    }
}

import { Logger } from "./log";

export interface Widget {
  id: number;
  label: string;
}

// makeWidget constructs a Widget.
export const makeWidget = (id: number): Widget => {
  return { id, label: "w" + id };
};

@Injectable()
export class WidgetService {
  private items: Widget[] = [];

  add(w: Widget): void {
    this.items.push(w);
  }

  find(id: number): Widget | undefined {
    return this.items.find((x) => x.id === id);
  }
}

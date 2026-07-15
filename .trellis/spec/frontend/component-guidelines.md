# Component Guidelines

> How components are built in this project.

---

## Overview

- **UI library**: shadcn/ui (Radix primitives + Tailwind)
- **Charts**: Tremor (wrapper around Recharts, for monitoring dashboards)
- **Styling**: Tailwind CSS v4 with `cn()` helper from `@/lib/utils`
- **Icons**: lucide-react

---

## Component Structure

Every component follows this template:

```tsx
// features/servers/components/server-card.tsx
import type { Server } from "@/types/models";

interface ServerCardProps {
  server: Server;
  onConnect?: (id: number) => void;
}

export function ServerCard({ server, onConnect }: ServerCardProps) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <h3 className="font-semibold">{server.name}</h3>
      <p className="text-muted-foreground text-sm">{server.host}</p>
      {onConnect && (
        <Button onClick={() => onConnect(server.id)} variant="outline">
          Connect
        </Button>
      )}
    </div>
  );
}
```

### Rules

- One component per file. No exception.
- Props interface always named `{ComponentName}Props` and defined above the component.
- Export the component as a named export (`export function`), never default export.
- No inline functions >3 lines in JSX — extract to a named function.
- `cn()` helper for conditional class merging:

```tsx
import { cn } from "@/lib/utils";

className={cn(
  "base-class",
  isActive && "active-class",
  size === "lg" && "text-lg",
)}
```

---

## Props Conventions

```tsx
// ✅ Good: clear, typed, optional callbacks
interface ServerListProps {
  servers: Server[];
  loading?: boolean;
  onSelect?: (server: Server) => void;
  onDelete?: (id: number) => void;
}

// ❌ Bad: vague names, any, no callbacks
interface Props {
  data: any;
  action?: Function;
}
```

- Callbacks prefix: `on{Event}` (`onSubmit`, `onClose`, `onSelect`).
- Booleans prefix: `is{State}` or `has{Feature}` (`isLoading`, `hasPermission`).
- Use `React.ReactNode` for children, never `JSX.Element`.
- Never pass raw `className` through — use `cn()` or named variant props.

---

## Styling Patterns

**Tailwind only.** No CSS modules, no styled-components, no inline styles (except dynamic values like `width: ${percent}%`).

### shadcn/ui Variants

Use shadcn/ui's `cva` (class-variance-authority) for components with variants:

```tsx
import { cva, type VariantProps } from "class-variance-authority";

const badgeVariants = cva(
  "inline-flex rounded-full px-2 py-0.5 text-xs font-medium",
  {
    variants: {
      variant: {
        online:  "bg-green-100 text-green-800",
        offline: "bg-gray-100 text-gray-800",
        error:   "bg-red-100 text-red-800",
      },
    },
    defaultVariants: { variant: "offline" },
  },
);

interface StatusBadgeProps extends VariantProps<typeof badgeVariants> {
  label: string;
}

export function StatusBadge({ label, variant }: StatusBadgeProps) {
  return <span className={badgeVariants({ variant })}>{label}</span>;
}
```

### Tremor Charts

For monitoring dashboards, Tremor provides pre-built chart components:

```tsx
import { AreaChart, Card, Title } from "@tremor/react";

const chartdata = [
  { time: "14:00", "CPU %": 45 },
  { time: "14:05", "CPU %": 52 },
];

<Card>
  <Title>CPU Usage</Title>
  <AreaChart
    data={chartdata}
    index="time"
    categories={["CPU %"]}
    colors={["blue"]}
  />
</Card>
```

---

## Accessibility (a11y)

- All interactive elements can be reached via keyboard.
- `aria-label` on icon-only buttons: `<Button aria-label="Delete server"><Trash2 /></Button>`.
- shadcn/ui primitives (Dialog, DropdownMenu, etc.) are a11y-compliant out of the box — don't break their focus management.
- Use semantic HTML: `<main>`, `<nav>`, `<section>`, `<header>`.
- Form inputs always have associated `<label>` — shadcn's Form + Label handles this.

---

## Shared UI Components

Components in `components/ui/` are feature-agnostic building blocks reused across multiple features. Before creating a new one, check if existing components cover the use case.

### Status Indicator Pattern

For displaying operational status with a colored dot, use `StatusIndicator`:

```tsx
// components/ui/status-indicator.tsx
import { StatusIndicator } from "@/components/ui/status-indicator";

// Usage:
<StatusIndicator status="online" />              // green dot + pulse animation
<StatusIndicator status="offline" size="sm" />   // red dot, small
<StatusIndicator status="unknown" />             // gray dot, no pulse
```

**Contract**:
- `status`: `"online" | "offline" | "checking" | "unknown"` — determines color and animation
- `showPulse?`: boolean, defaults `true` for online/checking — adds CSS `animate-ping` overlay
- `size?`: `"sm" | "md"`, default `"md"`
- `className?`: merged with `cn()`
- Accessibility: `role="status"` + `<span className="sr-only">` with status label
- Colors: online=green, offline=red, checking=yellow, unknown=gray

### Metric Progress Bar Pattern

For displaying a labeled percentage with color-coded fill:

```tsx
import { MetricBar } from "@/components/ui/metric-bar";
import { Cpu, HardDrive, MemoryStick } from "lucide-react";

<MetricBar label="CPU" value={45.2} icon={Cpu} />
<MetricBar label="Disk" value={null} icon={HardDrive} />  // "N/A" when null
```

**Contract**:
- `label`: string displayed left of bar
- `value`: `number | null` — null renders gray bar with "N/A"
- `icon?`: LucideIcon rendered left of label
- Color thresholds: `<65` green, `65-89` yellow, `≥90` red, `null` gray
- Percentage: right-aligned, `font-mono tabular-nums`

### Dynamic Key-Value Input Pattern

For forms that need a variable number of key-value pairs (credentials, headers, environment variables), use a component that manages an internal list of `{id, key, value}` objects. Mount two instances side by side when the form has parallel key-value groups (e.g. credentials + hints).

```tsx
// features/services/components/service-key-input.tsx

interface KeyValuePair {
  id: number;      // stable React key; not part of the data model
  key: string;
  value: string;
}

interface ServiceKeyInputProps {
  value: Record<string, string>;           // controlled: parent owns the data
  onChange: (value: Record<string, string>) => void;
}

let nextId = 0;

function toPairs(record: Record<string, string>): KeyValuePair[] {
  return Object.entries(record).map(([key, value]) => ({ id: nextId++, key, value }));
}

function fromPairs(pairs: KeyValuePair[]): Record<string, string> {
  const result: Record<string, string> = {};
  for (const pair of pairs) {
    if (pair.key.trim()) {
      result[pair.key.trim()] = pair.value;
    }
  }
  return result;
}

export function ServiceKeyInput({ value, onChange }: ServiceKeyInputProps) {
  const pairs = toPairs(value);

  const updatePair = (index: number, field: "key" | "value", newVal: string) => {
    const updated = pairs.map((p, i) => (i === index ? { ...p, [field]: newVal } : p));
    onChange(fromPairs(updated));
  };

  const addPair = () => {
    onChange(fromPairs([...pairs, { id: nextId++, key: "", value: "" }]));
  };

  const removePair = (index: number) => {
    onChange(fromPairs(pairs.filter((_, i) => i !== index)));
  };

  return (
    <div className="space-y-2">
      {pairs.map((pair, index) => (
        <div key={pair.id} className="flex items-start gap-2">
          <input
            value={pair.key}
            onChange={(e) => updatePair(index, "key", e.target.value)}
            placeholder="Key"
            className="w-1/3 ..."
          />
          <input
            value={pair.value}
            onChange={(e) => updatePair(index, "value", e.target.value)}
            placeholder="Value"
            className="w-2/3 ..."
          />
          <button onClick={() => removePair(index)} aria-label="Remove key">
            <Trash2 className="h-4 w-4" />
          </button>
        </div>
      ))}
      <button type="button" onClick={addPair}>
        <Plus className="h-3.5 w-3.5" /> Add key
      </button>
    </div>
  );
}
```

**Usage with react-hook-form**:

```tsx
import { Controller, useForm } from "react-hook-form";

const { control } = useForm({ defaultValues: { credentials: {} } });

<Controller
  name="credentials"
  control={control}
  render={({ field }) => (
    <ServiceKeyInput
      value={field.value as Record<string, string>}
      onChange={field.onChange}
    />
  )}
/>
```

### Contract

- `value`: `Record<string, string>` — the current key-value pairs. Empty record means no pairs shown.
- `onChange`: called with a new `Record<string, string>` after every mutation (add, edit, remove).
- Internal pair list uses a module-level `nextId` counter with simple `{id, key, value}` objects. No need for `useId()` or crypto-random IDs because pairs are ephemeral form state.
- Empty-key pairs are **not** included in `fromPairs()` output. The parent receives only valid entries.
- The component is **uncontrolled internally** — it derives from `value` prop but manages pair-level state locally. This is NOT a fully controlled component in the React sense; it's a controlled adapter between `Record<string, string>` and the UI.

### Rules

- Use `Controller` from react-hook-form (not `register`) because the component needs `onChange` with the full record, not a DOM event.
- The parent's Zod schema should validate `credentials` as `z.record(z.string(), z.string())` with at least one entry.
- When mounting two `ServiceKeyInput` instances (e.g. credentials + hints), use two separate `Controller` entries.
- Never put the key-value input inside a `useFieldArray` — `ServiceKeyInput` already manages its own pair list. Nesting would cause duplicate state.

### Common Mistakes

```tsx
// ❌ BAD: using register with a dynamic component
<input {...register("credentials")} />  // register gives a DOM event handler, not a record

// ✅ GOOD: using Controller for record-shaped data
<Controller name="credentials" control={control} render={({ field }) => (
  <ServiceKeyInput value={field.value} onChange={field.onChange} />
)} />

// ❌ BAD: using crypto.randomUUID() for pair IDs
const id = crypto.randomUUID();  // unnecessary: pairs are form-local, not persisted

// ✅ GOOD: simple incrementing counter
let nextId = 0;
const id = nextId++;
```

## Common Mistakes

- ❌ Exporting default function — always use named exports.
- ❌ Inline styles with style prop — use Tailwind classes.
- ❌ `useState` for derived values — compute from props/state directly.
- ❌ Extracting every repeated 2-line JSX into a component — only extract when it has its own state or >10 lines.
- ❌ Mixing shadcn/ui with raw Radix imports — shadcn components ARE Radix, use them directly.
- ❌ Passing `className` to component root without merging: `<div className={props.className}>` — use `cn(baseClass, props.className)`.

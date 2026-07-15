import { Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

interface KeyValuePair {
  id: number;
  key: string;
  value: string;
}

interface ServiceKeyInputProps {
  value: Record<string, string>;
  onChange: (value: Record<string, string>) => void;
}

let nextId = 0;

function toPairs(record: Record<string, string>): KeyValuePair[] {
  return Object.entries(record).map(([key, value]) => ({ id: nextId++, key, value }));
}

function fromPairs(pairs: KeyValuePair[]): Record<string, string> {
  const result: Record<string, string> = {};
  for (const pair of pairs) {
    result[pair.key] = pair.value;
  }
  return result;
}

export function ServiceKeyInput({ value, onChange }: ServiceKeyInputProps) {
  const { t } = useTranslation();
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
            type="text"
            value={pair.key}
            onChange={(e) => updatePair(index, "key", e.target.value)}
            placeholder="Key"
            className="w-1/3 rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          />
          <input
            type="text"
            value={pair.value}
            onChange={(e) => updatePair(index, "value", e.target.value)}
            placeholder="Value"
            className="w-2/3 rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          />
          <button
            type="button"
            onClick={() => removePair(index)}
            className="mt-2 rounded p-1 text-gray-400 hover:text-red-500"
            aria-label={t("service.removeKey")}
          >
            <Trash2 className="h-4 w-4" />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={addPair}
        className="inline-flex items-center gap-1 text-xs text-indigo-600 hover:text-indigo-700 dark:text-indigo-400 dark:hover:text-indigo-300"
      >
        <Plus className="h-3.5 w-3.5" />
        {t("service.addKey")}
      </button>
    </div>
  );
}

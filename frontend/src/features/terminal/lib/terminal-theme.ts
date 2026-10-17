import type { ITheme } from "@xterm/xterm";

const lightANSI: ITheme = {
  black: "#111827", red: "#b91c1c", green: "#047857", yellow: "#854d0e",
  blue: "#1d4ed8", magenta: "#7e22ce", cyan: "#0e7490", white: "#4b5563",
  brightBlack: "#6b7280", brightRed: "#b91c1c", brightGreen: "#047857", brightYellow: "#854d0e",
  brightBlue: "#2563eb", brightMagenta: "#9333ea", brightCyan: "#0e7490", brightWhite: "#111827",
};
const darkANSI: ITheme = {
  black: "#484f58", red: "#ff7b72", green: "#3fb950", yellow: "#d29922",
  blue: "#58a6ff", magenta: "#bc8cff", cyan: "#39c5d0", white: "#b1bac4",
  brightBlack: "#6e7681", brightRed: "#ffa198", brightGreen: "#56d364", brightYellow: "#e3b341",
  brightBlue: "#79c0ff", brightMagenta: "#d2a8ff", brightCyan: "#56d4dd", brightWhite: "#f0f6fc",
};

export function terminalTheme(element: HTMLElement): ITheme {
  const style = getComputedStyle(element);
  const color = (name: string) => style.getPropertyValue(`--ssh-${name}`).trim();
  return {
    ...(document.documentElement.classList.contains("dark") ? darkANSI : lightANSI),
    background: color("background"), foreground: color("terminal-text"),
    cursor: color("accent"), cursorAccent: color("background"),
    selectionBackground: color("selection"), selectionForeground: color("terminal-text"),
  };
}

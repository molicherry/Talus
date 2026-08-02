import { useEffect, useState } from "react";

function getVisibility(): boolean {
  return typeof document !== "undefined" ? document.visibilityState === "visible" : true;
}

/**
 * Returns true when the page/tab is visible, false when hidden.
 * Automatically updates on visibilitychange events.
 */
export function usePageVisibility(): boolean {
  const [visible, setVisible] = useState(getVisibility);

  useEffect(() => {
    const handler = () => setVisible(getVisibility());
    document.addEventListener("visibilitychange", handler);
    return () => document.removeEventListener("visibilitychange", handler);
  }, []);

  return visible;
}

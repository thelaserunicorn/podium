import { createContext, useCallback, useContext, useMemo, useRef, useState } from "react";

/**
 * Toast — single notification payload. Kept tiny on purpose: the goal is
 * "deployment finished", not a generic notification system. `variant`
 * maps to icon/color in src/components/ui/toast.tsx.
 */
export interface Toast {
  id: string;
  title: string;
  description?: string;
  variant: "success" | "error" | "info";
}

interface ToastContextValue {
  toasts: Toast[];
  toast: (t: Omit<Toast, "id">) => string;
  dismiss: (id: string) => void;
}

const ToastContext = createContext<ToastContextValue | null>(null);

/**
 * ToastProvider — owns the active-toast list and auto-dismiss timers.
 * Mounted once near the app root in src/App.tsx; the viewport
 * (`<ToastViewport />`) renders the actual UI.
 *
 * Why a context instead of an event bus / zustand: the toast list is
 * session-local UI state. No need to outlive a remount of the provider,
 * no need to share across browser tabs, no need to persist. A context
 * is the cheapest correct answer.
 */
export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  // Timer handles keyed by toast id. Ref (not state) so changing the
  // map doesn't trigger a re-render and so the cleanup function in
  // dismiss() can call clearTimeout against the most recent handle.
  const timersRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

  const dismiss = useCallback((id: string) => {
    const handle = timersRef.current.get(id);
    if (handle) {
      clearTimeout(handle);
      timersRef.current.delete(id);
    }
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const toast = useCallback(
    (t: Omit<Toast, "id">) => {
      // crypto.randomUUID is widely available (browsers since 2022); falls
      // back to a time-prefixed string for older runtimes just in case.
      const id =
        typeof crypto !== "undefined" && "randomUUID" in crypto
          ? crypto.randomUUID()
          : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      setToasts((prev) => [...prev, { ...t, id }]);
      // Default 4s; success popups feel right at 4s, errors get a touch
      // longer to give the user time to read the message. The variant
      // branch keeps the rule local to the toast call site.
      const ttl = t.variant === "error" ? 6000 : 4000;
      const handle = setTimeout(() => dismiss(id), ttl);
      timersRef.current.set(id, handle);
      return id;
    },
    [dismiss],
  );

  const value = useMemo(() => ({ toasts, toast, dismiss }), [toasts, toast, dismiss]);

  return <ToastContext.Provider value={value}>{children}</ToastContext.Provider>;
}

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    // Falls back to a no-op rather than throwing so a component rendered
    // outside the provider (e.g. in a unit test or Storybook sandbox)
    // doesn't crash the tree. Tests can wrap with their own provider if
    // they need to assert on the toast.
    return { toasts: [], toast: () => "", dismiss: () => {} };
  }
  return ctx;
}

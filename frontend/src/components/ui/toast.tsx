import { CheckCircle2, AlertCircle, Info, X } from "lucide-react";
import { useToast, type Toast } from "@/lib/toast";
import { Button } from "@/components/ui/button";

/**
 * ToastViewport — single fixed-position container for all active toasts.
 * Rendered once by the ToastProvider (mounted at the app root, see
 * src/App.tsx). Toasts are stacked top-down in arrival order; each one
 * owns its own dismiss timer + slide-down animation (see index.css).
 *
 * Why one viewport, not one toast per call: lets us centralize spacing,
 * z-index, and dismissal behavior in one place instead of letting every
 * caller fight the layout. The toast list itself is held in
 * `useToast`; this component is purely presentational.
 */
export function ToastViewport() {
  const { toasts, dismiss } = useToast();

  if (toasts.length === 0) return null;

  return (
    // top-4 + inset-x-0 centers the toast on the viewport regardless of
    // sidebar width. z-50 keeps it above the page content but below any
    // dialog backdrop (which uses z-60 in ConfirmDialog).
    <div
      aria-live="polite"
      aria-atomic="true"
      className="pointer-events-none fixed inset-x-0 top-4 z-50 flex flex-col items-center gap-2 px-4"
    >
      {toasts.map((t) => (
        <ToastItem key={t.id} toast={t} onDismiss={() => dismiss(t.id)} />
      ))}
    </div>
  );
}

function ToastItem({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  // Color/icon per variant. We pick iconography here (not in the caller)
  // so the visual language stays consistent across the app.
  const Icon =
    toast.variant === "success" ? CheckCircle2 : toast.variant === "error" ? AlertCircle : Info;
  const accent =
    toast.variant === "success"
      ? "text-emerald-600 dark:text-emerald-400"
      : toast.variant === "error"
        ? "text-destructive"
        : "text-muted-foreground";

  return (
    <div
      role={toast.variant === "error" ? "alert" : "status"}
      // pointer-events-auto re-enables clicks on the toast itself so the
      // close button works (the wrapper above is pointer-events-none to
      // let through page interactions outside the toast bounds).
      // min-w-[20rem] + max-w-md gives a comfortable reading width; long
      // descriptions wrap inside the card.
      className="toast-slide-down pointer-events-auto flex w-full min-w-[20rem] max-w-md items-start gap-3 rounded-md border border-border bg-background px-4 py-3 shadow-lg"
    >
      <Icon className={`mt-0.5 h-5 w-5 shrink-0 ${accent}`} />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-foreground">{toast.title}</p>
        {toast.description && (
          <p className="mt-0.5 text-xs text-muted-foreground">{toast.description}</p>
        )}
      </div>
      <Button
        size="sm"
        variant="ghost"
        onClick={onDismiss}
        className="h-6 w-6 shrink-0 p-0 text-muted-foreground hover:text-foreground"
        aria-label="Dismiss"
      >
        <X className="h-4 w-4" />
      </Button>
    </div>
  );
}

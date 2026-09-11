// ConfirmDialog — thin wrapper around the shadcn Dialog primitive for
// destructive-action confirmations. Six call sites used to do
// `window.confirm(...)`; this gives them a consistent modal that
// matches the rest of the UI.
//
// Conventions:
//   - Cancel is the default focus (preventing accidental Enter-to-
//     destroy). Radix's DialogContent focuses the first tabbable
//     child on open, so we render Cancel first.
//   - Confirm uses the existing `destructive` button variant.
//   - The destructive action is closed over via the `onConfirm` prop;
//     the caller decides what fires. The modal closes immediately on
//     click (the caller's async work happens in the background; we
//     don't block on it because the page's existing busy/error
//     affordances handle that).
import { useEffect, useRef } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel?: string;
  cancelLabel?: string;
  // Disables both buttons and flips the confirm label to "Working…".
  // The modal typically closes before the operation finishes, so the
  // busy state is rarely visible — but it matters when the caller
  // delays closing until the API resolves.
  busy?: boolean;
  onConfirm: () => void | Promise<void>;
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  busy = false,
  onConfirm,
}: ConfirmDialogProps) {
  const cancelRef = useRef<HTMLButtonElement | null>(null);

  // Re-focus the Cancel button whenever the modal opens, in case
  // Radix's default-first-tabbable heuristic picks something else
  // (e.g. the X close icon we render inside DialogContent).
  useEffect(() => {
    if (open) cancelRef.current?.focus();
  }, [open]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            ref={cancelRef}
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => onOpenChange(false)}
          >
            {cancelLabel}
          </Button>
          <Button
            type="button"
            variant="destructive"
            disabled={busy}
            onClick={() => {
              onOpenChange(false);
              void onConfirm();
            }}
          >
            {busy ? "Working…" : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

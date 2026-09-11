import { useEffect } from "react";
import { Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { nextTheme, useTheme, bindSystemThemeListener } from "@/lib/theme";

// ThemeToggle is the single icon button that cycles the theme.
// Rendered in the authenticated header (Layout.tsx); the logo +
// sidebar sit to its left. The icon reflects the RESOLVED theme —
// what the user currently sees — so it always tells the truth:
//   - resolved light → Sun (clicking goes to dark)
//   - resolved dark  → Moon (clicking goes to system)
// The label/tooltip describes the STORED mode so users can read
// off "system follows my OS" without having to click.
export function ThemeToggle() {
  const { theme, setTheme, resolvedTheme } = useTheme();

  // The system-mode listener only matters while the page is
  // alive; bind once on first render.
  useEffect(() => {
    bindSystemThemeListener();
  }, []);

  const Icon = resolvedTheme === "dark" ? Moon : Sun;
  // nextTheme picks the click target. In system mode we always
  // jump to "light" so the cycle is monotonic regardless of the
  // OS's current setting.
  const target = nextTheme(theme);
  const label = targetLabel(target);

  return (
    <Button
      variant="ghost"
      size="icon"
      // Square hit target keeps the visual weight even with the
      // Sign-out button next to it.
      aria-label={`Theme: ${label}. Click to switch.`}
      title={label}
      onClick={() => setTheme(target)}
    >
      <Icon className="h-4 w-4" />
      {/* The "system" mode marker — a tiny dot so users know the
          page is following the OS, not pinned to a single value. */}
      {theme === "system" && (
        <span
          aria-hidden="true"
          className="pointer-events-none absolute h-1.5 w-1.5 rounded-full bg-foreground"
        />
      )}
    </Button>
  );
}

// targetLabel returns the human-readable description of the click
// target, including the resolved-mode hint in system so a user
// never wonders whether the click "did something".
function targetLabel(target: "light" | "dark" | "system"): string {
  switch (target) {
    case "light":
      return "Light";
    case "dark":
      return "Dark";
    case "system":
      return "System (follows OS)";
  }
}

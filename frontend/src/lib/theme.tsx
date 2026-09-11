// theme.tsx — three-mode theme: light / dark / system.
// State lives in localStorage so the chosen mode survives reloads
// (no backend roundtrip — DECISIONS.md / AGENTS.md "no per-user DB
// column" for cosmetic preferences). The resolved theme (what the
// UI actually renders in) is derived from the stored mode + the OS
// `prefers-color-scheme` when mode === "system".
//
// Why no React Context: every consumer needs the same singleton
// state, but a Context would force a top-level wrapper that re-
// renders on every transition. A module-level state + event pattern
// (useSyncExternalStore below) achieves the same thing without
// adding a provider to main.tsx and without the re-render churn.

import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark" | "system";
export type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "podium-theme";

const subscribers = new Set<() => void>();

// currentTheme is the source of truth in this module. We initialise
// it to the resolved value of the pre-mount script (see
// index.html). Because the pre-mount script already wrote the .dark
// class on <html>, this initialisation matches reality on the first
// render — no flash.
let currentTheme: Theme = readInitialTheme();

function readInitialTheme(): Theme {
  if (typeof window === "undefined") return "system";
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === "light" || stored === "dark" || stored === "system") {
    return stored;
  }
  return "system";
}

function notify() {
  for (const fn of subscribers) fn();
}

// applyTheme is called whenever the resolved theme should change.
// It is idempotent — the .dark class is added or removed exactly
// once. `currentTheme` is updated first so the listeners can read
// the new value during notification.
export function applyTheme(theme: Theme) {
  currentTheme = theme;
  if (typeof window === "undefined") return;
  const root = window.document.documentElement;
  const resolved: ResolvedTheme = resolveTheme(theme);
  root.classList.toggle("dark", resolved === "dark");
  // Persist. Wrapped in try/catch so Safari Private Mode (which
  // throws on localStorage.setItem) doesn't break the toggle.
  try {
    window.localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // Ignored — toggling without persistence is still better than
    // a white screen on first paint.
  }
  notify();
}

export function setTheme(theme: Theme) {
  applyTheme(theme);
}

// resolveTheme computes the effective mode given a stored Theme and
// the OS preference. Pure — does not read state. Used by applyTheme
// and by the React hook below.
export function resolveTheme(theme: Theme): ResolvedTheme {
  if (theme === "dark") return "dark";
  if (theme === "light") return "light";
  // "system" — ask the OS.
  if (typeof window === "undefined" || !window.matchMedia) return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function subscribe(fn: () => void): () => void {
  subscribers.add(fn);
  return () => subscribers.delete(fn);
}

function getSnapshot(): Theme {
  return currentTheme;
}

function getServerSnapshot(): Theme {
  return "system";
}

// useTheme returns the stored mode. Components that need the
// RESOLVED theme (light/dark, what the icon button actually
// switches on) call resolveTheme(theme) themselves — keeps the
// selector stable and the hook cheap.
export function useTheme(): {
  theme: Theme;
  setTheme: (t: Theme) => void;
  resolvedTheme: ResolvedTheme;
} {
  const theme = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
  return { theme, setTheme, resolvedTheme: resolveTheme(theme) };
}

// nextTheme cycles through light → dark → system → light. Called
// by the icon button on click. Keeping the cycle here means the
// button component doesn't need to know the ordering.
export function nextTheme(current: Theme): Theme {
  switch (current) {
    case "light":
      return "dark";
    case "dark":
      return "system";
    case "system":
      return "light";
  }
}

// Wire the OS-level prefers-color-scheme change listener once per
// page load. When the user is in "system" mode and the OS theme
// flips (e.g. macOS auto at sunset), the page should follow.
//
// Calling this is idempotent — the same handler is registered
// regardless of how many components mount, but the module is only
// imported once thanks to Vite's ESM caching.
let bound = false;
export function bindSystemThemeListener() {
  if (typeof window === "undefined" || !window.matchMedia) return;
  if (bound) return;
  bound = true;

  const mql = window.matchMedia("(prefers-color-scheme: dark)");
  mql.addEventListener("change", () => {
    // Only re-apply if the user is in system mode. If they've
    // explicitly picked light/dark we ignore OS changes.
    if (currentTheme === "system") {
      applyTheme("system");
    }
  });
}

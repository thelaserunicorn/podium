// cn: a tiny class-name combiner used by the shadcn-style components.
// Combines clsx + tailwind-merge so later classes win.
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

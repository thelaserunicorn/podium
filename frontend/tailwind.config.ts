import type { Config } from "tailwindcss";

const config: Config = {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  // Class-based dark mode: the `.dark` class is toggled on <html> by
  // src/lib/theme.tsx. Every existing `bg-background`, `text-foreground`,
  // ... class automatically re-binds to the dark CSS variables — no
  // `dark:` variants needed in the components themselves.
  darkMode: ["class", ".dark"],
  theme: {
    extend: {
      colors: {
        // shadcn-style neutrals. Generated as CSS variables below.
        border: "hsl(var(--border))",
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        muted: "hsl(var(--muted))",
        "muted-foreground": "hsl(var(--muted-foreground))",
        primary: "hsl(var(--primary))",
        "primary-foreground": "hsl(var(--primary-foreground))",
        accent: "hsl(var(--accent))",
        "accent-foreground": "hsl(var(--accent-foreground))",
        destructive: "hsl(var(--destructive))",
        "destructive-foreground": "hsl(var(--destructive-foreground))",
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
      },
    },
  },
  plugins: [],
};

export default config;

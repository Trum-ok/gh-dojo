// GitHub Primer colors, so the GIF blends into the README in either theme.
export type Theme = {
  canvas: string; // page background, matches github.com
  panel: string;
  inset: string;
  border: string;
  fg: string;
  muted: string;
  accent: string;
  green: string;
  yellow: string;
  orange: string;
  red: string;
  shadow: string;
  term: {
    bg: string;
    bar: string;
    fg: string;
    muted: string;
    prompt: string;
    ref: string;
    status: string;
  };
};

export const themes: Record<"light" | "dark", Theme> = {
  light: {
    canvas: "#ffffff",
    panel: "#f6f8fa",
    inset: "#ffffff",
    border: "#d1d9e0",
    fg: "#1f2328",
    muted: "#59636e",
    accent: "#0969da",
    green: "#1a7f37",
    yellow: "#9a6700",
    orange: "#bc4c00",
    red: "#d1242f",
    shadow: "0 8px 24px rgba(140,149,159,0.35)",
    term: {
      bg: "#f6f8fa",
      bar: "#eaeef2",
      fg: "#1f2328",
      muted: "#6e7781",
      prompt: "#1a7f37",
      ref: "#0969da",
      status: "#8250df",
    },
  },
  dark: {
    canvas: "#0d1117",
    panel: "#151b23",
    inset: "#0d1117",
    border: "#3d444d",
    fg: "#f0f6fc",
    muted: "#9198a1",
    accent: "#4493f8",
    green: "#3fb950",
    yellow: "#d29922",
    orange: "#db6d28",
    red: "#f85149",
    shadow: "0 8px 24px rgba(1,4,9,0.85)",
    term: {
      bg: "#010409",
      bar: "#151b23",
      fg: "#f0f6fc",
      muted: "#9198a1",
      prompt: "#3fb950",
      ref: "#4493f8",
      status: "#bc8cff",
    },
  },
};

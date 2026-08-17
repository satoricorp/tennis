import localFont from "next/font/local";

// Apple Garamond Light Italic — the same wordmark face as www. The face IS the
// italic, so the descriptor says so: without style: "italic" the browser slants
// an already-slanted font.
export const wordmark = localFont({
  src: "../fonts/AppleGaramond-LightItalic.ttf",
  weight: "300",
  style: "italic",
  variable: "--font-wordmark",
  display: "block",
});

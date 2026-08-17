import { Geist, Geist_Mono } from "next/font/google";
import localFont from "next/font/local";

// Body/UI type. Geist Sans carries everything that isn't code.
export const geist = Geist({
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  variable: "--font-geist",
  display: "swap",
});

// Every terminal block: tennis is a command-line tool, so the blocks that
// quote it should be set in the type it prints in.
export const geistMono = Geist_Mono({
  subsets: ["latin"],
  weight: ["400", "700"],
  variable: "--font-mono",
  display: "swap",
});

// Apple Garamond Light Italic — the wordmark, from a local copy.
// This file is a proprietary Bitstream cut ("unpublished work […] All rights
// reserved. Confidential.", 1991). Clear the licensing before this repo goes
// public, or swap in EB Garamond 300 italic from next/font/google.
// The face IS the italic, so the descriptor says so: without style: "italic"
// the browser slants an already-slanted font.
export const wordmark = localFont({
  src: [
    {
      path: "../fonts/AppleGaramond-LightItalic.ttf",
      weight: "300",
      style: "italic",
    },
  ],
  variable: "--font-wordmark",
  display: "block",
});

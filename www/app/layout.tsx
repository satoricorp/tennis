import type { Metadata } from "next";
import { geist, geistMono, wordmark } from "./fonts";
import "./globals.css";

export const metadata: Metadata = {
  title: "Tennis — store all of your context in one place",
  description:
    "Tennis can collect sessions from Claude, ChatGPT, Codex, and more, including local files. Your context is stored locally in SQLite & markdown.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body
        className={`${geist.variable} ${geistMono.variable} ${wordmark.variable}`}
      >
        {children}
      </body>
    </html>
  );
}

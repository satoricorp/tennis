import type { Metadata } from "next";
import { berkeleyMono, geist, geistMono, wordmark } from "./fonts";
import "./globals.css";

export const metadata: Metadata = {
  title: "Tennis — store all of your sessions, take them anywhere",
  description:
    "Tennis collects context from any app or file, generates markdown summaries to easily review, and can be stored and used anywhere.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body
        className={`${geist.variable} ${geistMono.variable} ${wordmark.variable} ${berkeleyMono.variable}`}
      >
        {children}
      </body>
    </html>
  );
}

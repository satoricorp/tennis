"use client";

import { useEffect, useRef } from "react";
import {
  BANDS,
  RAMP,
  SPIN,
  orient,
  sampleCell,
  seamWidth,
} from "../lib/ball";

/* The tennis ball on the wordmark, raycast per character cell.
 *
 * Everything about the ball itself lives in lib/ball — this is only the part
 * that turns it into type on a canvas, and keeps it turning. The favicon takes
 * the same geometry and draws squares instead.
 */

const LINE_HEIGHT = 1;

// The seam needs roughly 27x16 cells before it reads as a seam rather than as
// scattered marks, so the type has to shrink with the ball to keep the cell
// count up.
const DEFAULT_FONT_SIZE = 9;

export default function BallBackground({
  className,
  fontSize = DEFAULT_FONT_SIZE,
}: {
  className?: string;
  fontSize?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const host = ref.current;
    if (!host) {
      return;
    }

    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }

    const canvas = document.createElement("canvas");
    canvas.style.position = "absolute";
    canvas.style.inset = "0";
    canvas.style.width = "100%";
    canvas.style.height = "100%";
    host.appendChild(canvas);

    const ctx = canvas.getContext("2d");
    if (!ctx) {
      canvas.remove();
      return;
    }

    let w = 0;
    let h = 0;
    let cols = 0;
    let rows = 0;
    let cellW = 0;
    let cellH = 0;

    const layout = () => {
      const dpr = window.devicePixelRatio || 1;
      w = host.clientWidth;
      h = host.clientHeight;
      canvas.width = Math.max(1, Math.round(w * dpr));
      canvas.height = Math.max(1, Math.round(h * dpr));
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.font = `${fontSize}px ui-monospace, SFMono-Regular, Menlo, monospace`;
      ctx.textBaseline = "top";
      // The row is painted as one string, so the grid pitch has to be the
      // font's own advance width or the characters drift out of their cells.
      cellW = ctx.measureText("M").width || fontSize * 0.6;
      cellH = fontSize * LINE_HEIGHT;
      cols = Math.ceil(w / cellW) + 1;
      rows = Math.ceil(h / cellH) + 1;
    };

    layout();
    const observer = new ResizeObserver(layout);
    observer.observe(host);

    let raf = 0;
    let start = 0;

    const draw = (now: number) => {
      if (!start) {
        start = now;
      }
      const t = (now - start) / 1000;

      ctx.clearRect(0, 0, w, h);

      const o = orient(t * SPIN);

      const r = Math.min(w, h) * 0.5;
      const cx = w / 2;
      const cy = h / 2;
      const last = RAMP.length - 1;
      const seamW = seamWidth(cellH, r);

      // One string per colour, so a row is still a handful of fillText calls
      // rather than one per cell.
      const lines: string[] = new Array(BANDS.length);
      const used: boolean[] = new Array(BANDS.length);

      for (let j = 0; j < rows; j++) {
        const dy = ((j + 0.5) * cellH - cy) / r;
        for (let b = 0; b < BANDS.length; b++) {
          lines[b] = "";
          used[b] = false;
        }

        for (let i = 0; i < cols; i++) {
          const dx = ((i + 0.5) * cellW - cx) / r;
          const cell = sampleCell(dx, dy, o, seamW);

          if (!cell) {
            for (let b = 0; b < BANDS.length; b++) lines[b] += " ";
            continue;
          }

          const ch = RAMP[Math.round(cell.shade * last)];
          for (let b = 0; b < BANDS.length; b++) {
            lines[b] += b === cell.band ? ch : " ";
          }
          used[cell.band] = true;
        }

        for (let b = 0; b < BANDS.length; b++) {
          if (!used[b]) continue;
          ctx.fillStyle = BANDS[b].color;
          ctx.fillText(lines[b], 0, j * cellH);
        }
      }

      raf = requestAnimationFrame(draw);
    };

    raf = requestAnimationFrame(draw);

    return () => {
      cancelAnimationFrame(raf);
      observer.disconnect();
      canvas.remove();
    };
  }, [fontSize]);

  return <div ref={ref} className={className} aria-hidden="true" />;
}

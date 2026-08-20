/* Writes cmd/tennis/logoart.go: the site's logo, as terminal half-cells.
 *
 *   npm run logo
 *
 * The CLI wants the mark the site has — the wordmark set in Apple Garamond
 * Light Italic with the ball on its shoulder — and a terminal can only draw in
 * character cells. So both halves are sampled here, at build time, and what
 * ships in the binary is a grid: the word rasterised from the same TTF the site
 * loads, and the ball raycast through lib/ball, the same module the canvas and
 * the favicon call.
 *
 * Both are sampled onto half-cells, two rows to a line, because a character
 * cell is twice as tall as it is wide and a ball drawn one cell to a pixel
 * comes out an ellipse. Folded back into ▀ ▄ █ the pixels are square, the ball
 * is round, and the wordmark gets twice the vertical resolution it would
 * otherwise have — which is the difference between Garamond's italic and a
 * smudge.
 *
 * What the page's ball has and this one does not is its optic yellow. The
 * terminal's foreground is the user's choice and a mark that overrides it is a
 * mark that is wrong in half the terminals it appears in, so the ball is set in
 * the same ink as the word and left to the two things that were carrying it
 * anyway: the circle, and the seam cut out of it.
 *
 * The output is checked in, as lines ready to print. A CLI that rasterises a
 * font and raycasts a sphere to say hello is a CLI carrying a font rasteriser
 * and a sphere raycaster.
 */

import { writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { orient, sampleCell, seamWidth } from "../lib/ball.ts";

const FONT = fileURLToPath(
  new URL("../fonts/AppleGaramond-LightItalic.ttf", import.meta.url),
);

// sharp's text input goes through fontconfig, and the copy inside the prebuilt
// binary has no default config to load — so it is handed one that knows about
// exactly one directory, ours. The site's wordmark is a self-hosted file and
// the CLI's has to be the same file, not whatever the build machine happens to
// have installed under a similar name.
const FC = `${tmpdir()}/tennis-logo-fonts.conf`;
writeFileSync(
  FC,
  `<?xml version="1.0"?><fontconfig><dir>${fileURLToPath(new URL("../fonts", import.meta.url))}</dir><cachedir>${tmpdir()}/tennis-logo-fc</cachedir></fontconfig>`,
);
process.env.FONTCONFIG_FILE = FC;

// After the env var, so fontconfig is initialised against it.
const { default: sharp } = await import("sharp");

/* Everything below is in cells, and the numbers that place the ball are ratios
 * measured off the rendered page rather than copies of the CSS: the site sets
 * the ball in pixels against type set in points, and what has to carry over is
 * how big it looks and where it sits, not either figure. */

// Height of the wordmark's ink. The one free choice here — it sets every other
// dimension, and past 9 the line runs wider than a terminal can be relied on
// to be. Below it the seam goes.
const ROWS = 9;

// A cell is this many times taller than it is wide. Real terminals land near 2
// (Menlo, SF Mono and friends advance 0.6em and line at 1.2em), and the whole
// grid is built in half-cells so that the assumption only has to hold to within
// a few percent.
const AR = 2;

// Ball diameter over wordmark ink height, and how far its top sits above the
// top of the letters, in the same unit. It rides high on the shoulder, the way
// a ® does, and overlaps the word's own top third.
const BALL_D = 0.707;
const BALL_RISE = 0.3015;

// Which frame of the spin, matching the favicon: the seam crosses the face and
// leaves at the silhouette at both ends, which is the one that survives being
// fourteen pixels wide.
const ANGLE = Math.PI / 6;

// Coverage a half-cell needs before it is inked. The letterforms are thresholded
// rather than shaded because the grid is too coarse for both: Garamond Light's
// hairlines are a third of a cell wide, so any ramp renders them as scattered
// grey and the word stops being a word. Under about 0.5 the strokes hold
// together and over it they break, so this sits below that with room to spare.
const CUT = 0.42;

// Point size for the rasterisation. Only the ratio to the cell grid matters —
// this is large enough that each cell averages a few hundred glyph pixels.
const TYPE = 260;

const PAPER = ".";
const WORD = "#";

/** The wordmark's ink coverage, thresholded, as `ROWS * 2` rows of half-cells. */
async function wordmark() {
  const { data, info } = await sharp({
    text: {
      text: "Tennis",
      font: `Apple Garamond Light Italic ${TYPE}`,
      fontfile: FONT,
    },
  })
    .raw()
    .toBuffer({ resolveWithObject: true });

  const { width: W, height: H, channels: ch } = info;
  const alpha = (x: number, y: number) => data[(y * W + x) * ch + ch - 1] / 255;

  // Ink-tight, because the box pango returns is the em box: it carries the
  // font's ascent and descent, and lining the ball up against those instead of
  // against the letters would put it somewhere different from the page.
  let x0 = W, x1 = -1, y0 = H, y1 = -1;
  for (let y = 0; y < H; y++) {
    for (let x = 0; x < W; x++) {
      if (alpha(x, y) > 0.03) {
        if (x < x0) x0 = x;
        if (x > x1) x1 = x;
        if (y < y0) y0 = y;
        if (y > y1) y1 = y;
      }
    }
  }
  const iw = x1 - x0 + 1;
  const ih = y1 - y0 + 1;

  const cols = Math.round((iw / ih) * ROWS * AR);
  const rows = ROWS * 2;
  const grid: string[][] = [];

  for (let j = 0; j < rows; j++) {
    const row: string[] = [];
    for (let i = 0; i < cols; i++) {
      // Box average over the cell's footprint, partial pixels weighted by how
      // much of them is inside it. Point sampling a hairline is a coin toss.
      const sx0 = x0 + (i * iw) / cols;
      const sx1 = x0 + ((i + 1) * iw) / cols;
      const sy0 = y0 + (j * ih) / rows;
      const sy1 = y0 + ((j + 1) * ih) / rows;
      let sum = 0;
      let area = 0;
      for (let y = Math.floor(sy0); y < Math.ceil(sy1); y++) {
        for (let x = Math.floor(sx0); x < Math.ceil(sx1); x++) {
          const w =
            Math.max(0, Math.min(sx1, x + 1) - Math.max(sx0, x)) *
            Math.max(0, Math.min(sy1, y + 1) - Math.max(sy0, y));
          sum += alpha(x, y) * w;
          area += w;
        }
      }
      row.push(area && sum / area >= CUT ? WORD : PAPER);
    }
    grid.push(row);
  }

  return { grid, cols };
}

/** The ball, as a square grid of half-cells: `d` across and `d` down. */
function ballGrid(d: number) {
  const o = orient(ANGLE);
  const seamW = seamWidth(1, d / 2);
  const grid: string[][] = [];
  for (let j = 0; j < d; j++) {
    const row: string[] = [];
    for (let i = 0; i < d; i++) {
      const cell = sampleCell(
        ((i + 0.5) / d - 0.5) * 2,
        ((j + 0.5) / d - 0.5) * 2,
        o,
        seamW,
      );
      // Neither the band nor the shade survives — one ink, and the ramp that
      // thins the page's silhouette has no partial coverage to work with here
      // anyway. What is left is the silhouette and the seam, which is paper
      // either way and was always the read.
      row.push(cell ? WORD : PAPER);
    }
    grid.push(row);
  }
  return grid;
}

const word = await wordmark();

// The ball is square in half-cells, so its height in whole cells is what has to
// round — round the diameter first and take the width from it, or the ball
// comes out a cell wider than it is tall and the roundness goes.
//
// Up rather than to the nearest, which costs a cell of width and buys the seam.
// The seam's own width has a floor in cells, so as the ball shrinks the seam
// takes a larger share of it: at six rows it has eaten enough of the face that
// what is left reads as stripes, and at seven it is a curve again.
const ballRows = Math.ceil(BALL_D * ROWS);
const ball = ballGrid(ballRows * 2);
const rise = Math.round(BALL_RISE * ROWS);

const cols = word.cols + ballRows * 2;
const rows = rise + ROWS;
const art: string[][] = Array.from({ length: rows * 2 }, () =>
  new Array(cols).fill(PAPER),
);

for (let j = 0; j < ROWS * 2; j++) {
  for (let i = 0; i < word.cols; i++) {
    art[rise * 2 + j][i] = word.grid[j][i];
  }
}
// Flush against the right edge of the ink, which is where the page puts it.
for (let j = 0; j < ball.length; j++) {
  for (let i = 0; i < ball[j].length; i++) {
    art[j][word.cols + i] = ball[j][i];
  }
}

// Two half-cell rows to a line. Trailing paper is dropped: it is trailing
// whitespace, and it would end up in every terminal that prints the mark.
const lines: string[] = [];
for (let j = 0; j + 1 < art.length; j += 2) {
  let line = "";
  for (let i = 0; i < cols; i++) {
    const top = art[j][i] !== PAPER;
    const bottom = art[j + 1][i] !== PAPER;
    line += top && bottom ? "█" : top ? "▀" : bottom ? "▄" : " ";
  }
  lines.push(line.replace(/ +$/, ""));
}

const go = `// Code generated by www/scripts/gen-logo.mts. DO NOT EDIT.

package main

// logoArt is the mark from the website, drawn in half blocks.
const logoArt = \`
${lines.join("\n")}
\`
`;

const out = fileURLToPath(new URL("../../cmd/tennis/logoart.go", import.meta.url));
writeFileSync(out, go);
console.log(`${out}: ${cols}x${rows} cells, ball ${ballRows * 2}px`);

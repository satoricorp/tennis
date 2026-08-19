/* The tennis ball's geometry, apart from anything that draws it.
 *
 * Two things need this ball and they agree about everything except output: the
 * mark on the page, which redraws it into characters sixty times a second, and
 * the favicon, which takes one frame of it and writes squares. So the shared
 * part is one cell of the raycast — given where a cell sits on the ball, what
 * comes back is which colour band it belongs to and how much ink it gets, and
 * the caller decides whether that is a glyph or a rectangle.
 *
 * Each cell is a ray at a sphere: inside the disc, the front surface point is
 * also the surface normal (unit sphere), which gives both the shading and —
 * once un-rotated back into the ball's own frame — the seam.
 *
 * The seam is the read, and it is drawn the truthful way round: as paper. A
 * real seam is the white part, and on a white page that is simply the cell left
 * empty, which is why the body has to stay dense — the curve reads as a gap in
 * the green, so there has to be green to gap.
 */

const TAU = Math.PI * 2;

// Most ink to least — block elements, not punctuation, because this ball has to
// hold a colour. Measured at this size, an ASCII ramp inks about 22% of its
// cells, and 22% of anything over white paper is a pastel: the green came out
// cream no matter which green it was. Blocks fill the cell, so what lands on the
// page is the colour itself. They share the monospace advance, so the row is
// still one string.
export const RAMP = "█▓▒░";

// Optic yellow, shadow to lit. Colour carries the light here rather than the
// character ramp: at this size the glyphs merge, so density alone reads as a
// smudge, where a shift in hue still reads as a lit sphere. These are the
// colour as it should look, not compensated for paper, because at full coverage
// there is no paper to mix with.
export const BANDS = [
  { upTo: 0.3, color: "#A3B82B" },
  { upTo: 0.62, color: "#C6DC3A" },
  { upTo: Infinity, color: "#DCEC52" },
];

// How square-on the surface has to be before it is drawn at full weight. A hard
// edge at the silhouette turns the top and bottom of the disc — the two places
// the boundary runs along a row rather than across it — into flat bars, so the
// ball thins out over this band and dissolves into the page instead.
const RIM = 0.34;

export const SPIN = 0.34; // radians/second, about the ball's own seam axis
export const TILT = 0.9; // where the seam axis leans; this is the angle that reads

// Half-width of the seam in radians of arc, not in units of the function below.
// Thresholding that function directly makes the seam pool into a bar wherever it
// runs tangentially — most visibly along the bottom — because equal function
// values are not equal distances. Dividing by the gradient converts one to the
// other and the seam keeps its width all the way round.
const SEAM_W = 0.042;

// ...but arc is a share of the ball, so on a small one it shrinks below what the
// grid can draw and the seam survives as flecks rather than a curve. This is the
// floor, as a half-width in cells, and it only ever binds on the small end — a
// large ball keeps the arc figure. Half a cell is the whole usable range: much
// under and the curve breaks up, much over and it stops being a curve at all,
// because the same arc covers most of the ball once the ball is 20 cells wide.
const SEAM_MIN_CELLS = 0.5;

/* The seam curve.
 *
 * The obvious curve — latitude as a plain sinusoid of azimuth — is the wrong
 * one: it turns at the top of each lobe with a radius of about 0.19 of the ball,
 * which reads as a corner, not a seam. The real one is the curve a tennis ball
 * is actually cut along,
 *
 *   x + iy = a·e^{it} + b·e^{-3it},   z = c·sin 2t,
 *
 * which lies on the unit sphere exactly when a + b = 1 and c = 2√(ab). It turns
 * at 0.57 of the ball instead — three times rounder — and sweeps rather than
 * points. b sets how far it swings; 0.12 puts the peak at 41° of latitude.
 *
 * Azimuth is monotonic in t whenever a > 3b, which holds comfortably here, so
 * the curve is a graph of latitude over azimuth and can be tabulated as one.
 * That keeps the per-cell test O(1) — the alternative, nearest-point against a
 * sampled curve, is a search per cell.
 */
const SEAM_B = 0.12;
const SEAM_TABLE = 512;

function buildSeam() {
  const b = SEAM_B;
  const a = 1 - b;
  const c = 2 * Math.sqrt(a * b);

  const N = 4096;
  const phi = new Float64Array(N + 1);
  const lat = new Float64Array(N + 1);
  for (let i = 0; i <= N; i++) {
    const t = (i / N) * TAU;
    const x = a * Math.cos(t) + b * Math.cos(3 * t);
    const y = a * Math.sin(t) - b * Math.sin(3 * t);
    const z = c * Math.sin(2 * t);
    const p = Math.atan2(y, x);
    phi[i] = i === N ? TAU : p < 0 ? p + TAU : p;
    lat[i] = Math.asin(Math.max(-1, Math.min(1, z)));
  }

  // Resample onto a uniform azimuth grid, interpolating: taking the nearest
  // sample instead puts a staircase into the curve that reads as jitter.
  const g = new Float64Array(SEAM_TABLE);
  let j = 0;
  for (let k = 0; k < SEAM_TABLE; k++) {
    const target = (k / SEAM_TABLE) * TAU;
    while (j < N - 1 && phi[j + 1] < target) j++;
    const span = phi[j + 1] - phi[j];
    const fr = span > 1e-12 ? (target - phi[j]) / span : 0;
    g[k] = lat[j] + (lat[j + 1] - lat[j]) * Math.max(0, Math.min(1, fr));
  }

  const dg = new Float64Array(SEAM_TABLE);
  const step = TAU / SEAM_TABLE;
  for (let k = 0; k < SEAM_TABLE; k++) {
    const next = g[(k + 1) % SEAM_TABLE];
    const prev = g[(k - 1 + SEAM_TABLE) % SEAM_TABLE];
    dg[k] = (next - prev) / (2 * step);
  }

  return { g, dg };
}

const SEAM = buildSeam();

// A light high on the left, so the shading has a direction to it.
const LX = -0.42;
const LY = 0.5;
const LZ = 0.76;

/** Where the ball is facing: the lean, and how far it has spun into it. */
export type Orientation = {
  ca: number;
  sa: number;
  ct: number;
  st: number;
};

/** The ball at angle `a` about its seam axis, in radians. */
export function orient(a: number): Orientation {
  return {
    ca: Math.cos(a),
    sa: Math.sin(a),
    ct: Math.cos(TILT),
    st: Math.sin(TILT),
  };
}

/**
 * Seam half-width for a grid of this fineness — `cell` and `radius` in the same
 * units, whatever they are. The arc figure is what the ball wants; the floor is
 * what the grid can actually draw.
 */
export function seamWidth(cell: number, radius: number) {
  return Math.max(SEAM_W, (SEAM_MIN_CELLS * cell) / radius);
}

/** An inked cell: which colour band it takes, and how much of the cell it fills. */
export type Cell = {
  band: number;
  /** 0 is a full cell, 1 is the faintest the ramp goes. */
  shade: number;
};

/**
 * One ray at the ball. `dx`/`dy` are the cell's centre as an offset from the
 * ball's, in units of its radius, with y down as the page has it.
 *
 * Null is a cell left empty, which is three different things — off the ball,
 * dissolved at the silhouette, or the seam — that all want the same nothing.
 */
export function sampleCell(
  dx: number,
  dy: number,
  o: Orientation,
  seamW: number,
): Cell | null {
  const d2 = dx * dx + dy * dy;
  if (d2 >= 1) {
    return null;
  }

  // Front surface point of the unit sphere, in maths axes (y up).
  const X = dx;
  const Y = -dy;
  const Z = Math.sqrt(1 - d2);

  // Z doubles as how square-on the surface is: it goes to zero at the
  // silhouette, which is exactly where the ball should thin out.
  const rim = Math.min(1, Z / RIM);
  if (rim < 0.1) {
    return null;
  }

  // Same vector is the normal, so the shading is one dot product.
  const lit = Math.max(0, X * LX + Y * LY + Z * LZ);

  // Undo the lean, then the spin, to read the point in ball coords. The spin
  // has to be about the seam axis itself — spin it about any other and the
  // pattern tumbles instead of turning.
  const Y1 = Y * o.ct + Z * o.st;
  const Z1 = -Y * o.st + Z * o.ct;
  const X2 = X * o.ca + Y1 * o.sa;
  const Y2 = -X * o.sa + Y1 * o.ca;

  // Where the seam sits at this azimuth.
  const { g, dg } = SEAM;
  let u = (Math.atan2(Y2, X2) / TAU) * SEAM_TABLE;
  u -= Math.floor(u / SEAM_TABLE) * SEAM_TABLE;
  const k0 = u | 0;
  const fr = u - k0;
  const k1 = (k0 + 1) % SEAM_TABLE;
  const gv = g[k0] + (g[k1] - g[k0]) * fr;

  const f = Z1 - Math.sin(gv);

  // Cheap reject first, and it doubles as the guard on the division: the seam
  // never climbs past 41°, so the poles — where the azimuth is undefined and the
  // gradient blows up — can never pass this.
  if (Math.abs(f) < 0.3) {
    const rho2 = X2 * X2 + Y2 * Y2;
    if (rho2 > 1e-9) {
      const dgv = dg[k0] + (dg[k1] - dg[k0]) * fr;
      const cg = Math.cos(gv) * dgv;
      const gx = (cg * Y2) / rho2;
      const gy = (-cg * X2) / rho2;
      // Gradient along the surface, so drop the part normal to it. The
      // azimuthal terms cancel in that dot product, leaving just Z1.
      const tx = gx - Z1 * X2;
      const ty = gy - Z1 * Y2;
      const tz = 1 - Z1 * Z1;
      const gm = Math.sqrt(tx * tx + ty * ty + tz * tz);
      // The seam is the paper: leave the cell empty and the page is already the
      // right colour.
      if (gm > 1e-6 && Math.abs(f) / gm < seamW) {
        return null;
      }
    }
  }

  // Solid across the whole body, so the seam has something to cut through; the
  // ramp is only there to thin the silhouette. The light is carried by the
  // colour band, not by how much ink the cell gets.
  const shade = Math.max(0, Math.min(1, 1 - (1 - 0.15 * lit) * rim));

  let band = 0;
  while (lit >= BANDS[band].upTo) band++;

  return { band, shade };
}

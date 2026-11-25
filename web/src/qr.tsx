import type { CSSProperties } from 'react';

type VersionSpec = {
  version: number;
  totalCW: number;
  ecPerBlk: number;
  g1Blocks: number;
  g1DataCW: number;
  g2Blocks: number;
  g2DataCW: number;
  alignment: number[];
};

const SPECS_M: VersionSpec[] = [
  { version: 1, totalCW: 26, ecPerBlk: 10, g1Blocks: 1, g1DataCW: 16, g2Blocks: 0, g2DataCW: 0, alignment: [] },
  { version: 2, totalCW: 44, ecPerBlk: 16, g1Blocks: 1, g1DataCW: 28, g2Blocks: 0, g2DataCW: 0, alignment: [6, 18] },
  { version: 3, totalCW: 70, ecPerBlk: 26, g1Blocks: 1, g1DataCW: 44, g2Blocks: 0, g2DataCW: 0, alignment: [6, 22] },
  { version: 4, totalCW: 100, ecPerBlk: 18, g1Blocks: 2, g1DataCW: 32, g2Blocks: 0, g2DataCW: 0, alignment: [6, 26] },
  { version: 5, totalCW: 134, ecPerBlk: 24, g1Blocks: 2, g1DataCW: 43, g2Blocks: 0, g2DataCW: 0, alignment: [6, 30] },
  { version: 6, totalCW: 172, ecPerBlk: 16, g1Blocks: 4, g1DataCW: 27, g2Blocks: 0, g2DataCW: 0, alignment: [6, 34] },
  { version: 7, totalCW: 196, ecPerBlk: 18, g1Blocks: 4, g1DataCW: 31, g2Blocks: 0, g2DataCW: 0, alignment: [6, 22, 38] },
  { version: 8, totalCW: 242, ecPerBlk: 22, g1Blocks: 2, g1DataCW: 38, g2Blocks: 2, g2DataCW: 39, alignment: [6, 24, 42] },
  { version: 9, totalCW: 292, ecPerBlk: 22, g1Blocks: 3, g1DataCW: 36, g2Blocks: 2, g2DataCW: 37, alignment: [6, 26, 46] },
  { version: 10, totalCW: 346, ecPerBlk: 26, g1Blocks: 4, g1DataCW: 43, g2Blocks: 1, g2DataCW: 44, alignment: [6, 28, 50] },
  { version: 11, totalCW: 404, ecPerBlk: 30, g1Blocks: 1, g1DataCW: 50, g2Blocks: 4, g2DataCW: 51, alignment: [6, 30, 54] },
  { version: 12, totalCW: 466, ecPerBlk: 22, g1Blocks: 6, g1DataCW: 36, g2Blocks: 2, g2DataCW: 37, alignment: [6, 32, 58] },
  { version: 13, totalCW: 532, ecPerBlk: 22, g1Blocks: 8, g1DataCW: 37, g2Blocks: 1, g2DataCW: 38, alignment: [6, 34, 62] },
  { version: 14, totalCW: 581, ecPerBlk: 24, g1Blocks: 4, g1DataCW: 40, g2Blocks: 5, g2DataCW: 41, alignment: [6, 26, 46, 66] },
  { version: 15, totalCW: 655, ecPerBlk: 24, g1Blocks: 5, g1DataCW: 41, g2Blocks: 5, g2DataCW: 42, alignment: [6, 26, 48, 70] },
];

const GF_EXP = new Uint8Array(512);
const GF_LOG = new Uint8Array(256);
(() => {
  let x = 1;
  for (let i = 0; i < 255; i++) {
    GF_EXP[i] = x;
    GF_LOG[x] = i;
    x <<= 1;
    if (x & 0x100) x ^= 0x11d;
  }
  for (let i = 255; i < 512; i++) GF_EXP[i] = GF_EXP[i - 255];
})();

function gfMul(a: number, b: number): number {
  return a === 0 || b === 0 ? 0 : GF_EXP[GF_LOG[a] + GF_LOG[b]];
}

function rsRemainder(data: Uint8Array, ecCount: number): Uint8Array {
  let gen = [1];
  for (let i = 0; i < ecCount; i++) {
    const next = new Array(gen.length + 1).fill(0);
    const factor = GF_EXP[i];
    for (let j = 0; j < gen.length; j++) {
      next[j] ^= gen[j];
      next[j + 1] ^= gfMul(gen[j], factor);
    }
    gen = next;
  }
  const rem = new Uint8Array(ecCount);
  for (const byte of data) {
    const factor = byte ^ rem[0];
    rem.copyWithin(0, 1);
    rem[ecCount - 1] = 0;
    for (let i = 0; i < ecCount; i++) rem[i] ^= gfMul(gen[i + 1], factor);
  }
  return rem;
}

export function qrMatrix(text: string): boolean[][] {
  const raw = new TextEncoder().encode(text);
  const spec = SPECS_M.find(s => {
    const dataCW = s.g1Blocks * s.g1DataCW + s.g2Blocks * s.g2DataCW;
    const countBits = s.version >= 10 ? 16 : 8;
    return 4 + countBits + raw.length * 8 <= dataCW * 8;
  });
  if (!spec) throw new Error('QR payload exceeds supported length');

  const dataCW = spec.g1Blocks * spec.g1DataCW + spec.g2Blocks * spec.g2DataCW;
  const bits: number[] = [];
  const push = (val: number, count: number) => {
    for (let i = count - 1; i >= 0; i--) bits.push((val >> i) & 1);
  };
  push(0b0100, 4);
  push(raw.length, spec.version >= 10 ? 16 : 8);
  for (const b of raw) push(b, 8);
  const rem = Math.min(4, dataCW * 8 - bits.length);
  if (rem > 0) push(0, rem);
  while (bits.length % 8 !== 0) bits.push(0);
  const data = new Uint8Array(dataCW);
  for (let i = 0; i < bits.length; i++) {
    if (bits[i]) data[i >> 3] |= 1 << (7 - (i & 7));
  }
  const pads = [0xec, 0x11];
  for (let i = bits.length >> 3, p = 0; i < dataCW; i++, p++) data[i] = pads[p & 1];

  const numBlocks = spec.g1Blocks + spec.g2Blocks;
  const blocks: Uint8Array[] = [];
  const ecs: Uint8Array[] = [];
  let offset = 0;
  for (let i = 0; i < numBlocks; i++) {
    const len = i < spec.g1Blocks ? spec.g1DataCW : spec.g2DataCW;
    const slice = data.slice(offset, offset + len);
    blocks.push(slice);
    ecs.push(rsRemainder(slice, spec.ecPerBlk));
    offset += len;
  }
  const codewords: number[] = [];
  const maxData = Math.max(spec.g1DataCW, spec.g2DataCW);
  for (let c = 0; c < maxData; c++) {
    for (const blk of blocks) if (c < blk.length) codewords.push(blk[c]);
  }
  for (let c = 0; c < spec.ecPerBlk; c++) {
    for (const ec of ecs) codewords.push(ec[c]);
  }

  const size = 17 + 4 * spec.version;
  const base = new Uint8Array(size * size);
  const reserved = new Uint8Array(size * size);
  const setFunc = (x: number, y: number, dark: boolean) => {
    if (x < 0 || y < 0 || x >= size || y >= size) return;
    base[y * size + x] = dark ? 1 : 0;
    reserved[y * size + x] = 1;
  };
  const placeFinder = (ox: number, oy: number) => {
    for (let dy = -1; dy <= 7; dy++) {
      for (let dx = -1; dx <= 7; dx++) {
        const dark = dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6 &&
          (dx === 0 || dx === 6 || dy === 0 || dy === 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4));
        setFunc(ox + dx, oy + dy, dark);
      }
    }
  };
  placeFinder(0, 0);
  placeFinder(size - 7, 0);
  placeFinder(0, size - 7);
  for (let i = 8; i < size - 8; i++) {
    setFunc(i, 6, i % 2 === 0);
    setFunc(6, i, i % 2 === 0);
  }
  const pos = spec.alignment;
  for (let i = 0; i < pos.length; i++) {
    for (let j = 0; j < pos.length; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === pos.length - 1) || (i === pos.length - 1 && j === 0)) continue;
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          setFunc(pos[j] + dx, pos[i] + dy, Math.max(Math.abs(dx), Math.abs(dy)) === 2 || (dx === 0 && dy === 0));
        }
      }
    }
  }
  for (let i = 0; i < 9; i++) if (i !== 6) { setFunc(8, i, false); setFunc(i, 8, false); }
  for (let i = size - 8; i < size; i++) setFunc(i, 8, false);
  for (let i = size - 7; i < size; i++) setFunc(8, i, false);
  setFunc(8, size - 8, true);
  if (spec.version >= 7) {
    for (let i = 0; i < 6; i++) for (let j = 0; j < 3; j++) {
      setFunc(size - 11 + j, i, false);
      setFunc(i, size - 11 + j, false);
    }
  }

  let bitIdx = 0;
  const totalBits = codewords.length * 8;
  let up = true;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let step = 0; step < size; step++) {
      const y = up ? size - 1 - step : step;
      for (let dx = 0; dx < 2; dx++) {
        const x = right - dx;
        const idx = y * size + x;
        if (reserved[idx]) continue;
        base[idx] = bitIdx < totalBits ? (codewords[bitIdx >> 3] >> (7 - (bitIdx & 7))) & 1 : 0;
        bitIdx++;
      }
    }
    up = !up;
  }

  let bestScore = Infinity;
  let best = base;
  for (let mask = 0; mask < 8; mask++) {
    const grid = base.slice();
    for (let y = 0; y < size; y++) {
      for (let x = 0; x < size; x++) {
        const idx = y * size + x;
        if (reserved[idx]) continue;
        const inv =
          mask === 0 ? (x + y) % 2 === 0 :
          mask === 1 ? y % 2 === 0 :
          mask === 2 ? x % 3 === 0 :
          mask === 3 ? (x + y) % 3 === 0 :
          mask === 4 ? (Math.floor(y / 2) + Math.floor(x / 3)) % 2 === 0 :
          mask === 5 ? (x * y) % 2 + (x * y) % 3 === 0 :
          mask === 6 ? ((x * y) % 2 + (x * y) % 3) % 2 === 0 :
          ((x + y) % 2 + (x * y) % 3) % 2 === 0;
        if (inv) grid[idx] ^= 1;
      }
    }
    let fmt = (0b00 << 3) | mask;
    let remBits = fmt << 10;
    for (let i = 14; i >= 10; i--) if ((remBits >> i) & 1) remBits ^= 0x537 << (i - 10);
    const fmtBits = ((fmt << 10) | remBits) ^ 0x5412;
    for (let i = 0; i < 15; i++) {
      const dark = (fmtBits >> i) & 1;
      if (i < 6) grid[i * size + 8] = dark;
      else if (i < 8) grid[(i + 1) * size + 8] = dark;
      else grid[(size - 15 + i) * size + 8] = dark;
      if (i < 8) grid[8 * size + (size - 1 - i)] = dark;
      else if (i < 9) grid[8 * size + 7] = dark;
      else grid[8 * size + (14 - i)] = dark;
    }
    grid[(size - 8) * size + 8] = 1;
    if (spec.version >= 7) {
      let vRem = spec.version << 12;
      for (let i = 17; i >= 12; i--) if ((vRem >> i) & 1) vRem ^= 0x1f25 << (i - 12);
      const vBits = (spec.version << 12) | vRem;
      for (let i = 0; i < 18; i++) {
        const dark = (vBits >> i) & 1;
        const a = size - 11 + (i % 3);
        const b = Math.floor(i / 3);
        grid[b * size + a] = dark;
        grid[a * size + b] = dark;
      }
    }
    const score = qrPenalty(grid, size);
    if (score < bestScore) { bestScore = score; best = grid; }
  }

  const rows: boolean[][] = [];
  for (let y = 0; y < size; y++) {
    const row: boolean[] = [];
    for (let x = 0; x < size; x++) row.push(best[y * size + x] === 1);
    rows.push(row);
  }
  return rows;
}

function qrPenalty(g: Uint8Array, size: number): number {
  let score = 0;
  for (let y = 0; y < size; y++) {
    let c = g[y * size], len = 1;
    for (let x = 1; x < size; x++) {
      const v = g[y * size + x];
      if (v === c) len++;
      else { if (len >= 5) score += 3 + (len - 5); c = v; len = 1; }
    }
    if (len >= 5) score += 3 + (len - 5);
  }
  for (let x = 0; x < size; x++) {
    let c = g[x], len = 1;
    for (let y = 1; y < size; y++) {
      const v = g[y * size + x];
      if (v === c) len++;
      else { if (len >= 5) score += 3 + (len - 5); c = v; len = 1; }
    }
    if (len >= 5) score += 3 + (len - 5);
  }
  for (let y = 0; y < size - 1; y++) {
    for (let x = 0; x < size - 1; x++) {
      const v = g[y * size + x];
      if (g[y * size + x + 1] === v && g[(y + 1) * size + x] === v && g[(y + 1) * size + x + 1] === v) score += 3;
    }
  }
  const checkLine = (get: (i: number) => number) => {
    for (let i = 0; i <= size - 11; i++) {
      const a = get(i) && !get(i + 1) && get(i + 2) && get(i + 3) && get(i + 4) && !get(i + 5) && get(i + 6) && !get(i + 7) && !get(i + 8) && !get(i + 9) && !get(i + 10);
      const b = !get(i) && !get(i + 1) && !get(i + 2) && !get(i + 3) && get(i + 4) && !get(i + 5) && get(i + 6) && get(i + 7) && get(i + 8) && !get(i + 9) && get(i + 10);
      if (a || b) score += 40;
    }
  };
  for (let y = 0; y < size; y++) checkLine(i => g[y * size + i]);
  for (let x = 0; x < size; x++) checkLine(i => g[i * size + x]);
  let dark = 0;
  for (const v of g) if (v) dark++;
  const pct = Math.floor((dark * 100) / (size * size));
  const prev = Math.floor(pct / 5) * 5;
  score += (Math.min(Math.abs(prev - 50), Math.abs(prev + 5 - 50)) / 5) * 10;
  return score;
}

export function QrCode({ value, label, size = 216, style }: { value: string; label: string; size?: number; style?: CSSProperties }) {
  const matrix = qrMatrix(value);
  const margin = 4;
  const total = matrix.length + margin * 2;
  let path = '';
  for (let y = 0; y < matrix.length; y++) {
    for (let x = 0; x < matrix.length; x++) {
      if (matrix[y][x]) path += `M${x + margin},${y + margin}h1v1h-1z`;
    }
  }
  return (
    <svg
      role="img"
      aria-label={label}
      width={size}
      height={size}
      viewBox={`0 0 ${total} ${total}`}
      shapeRendering="crispEdges"
      style={style}
      className="qr-svg"
    >
      <rect width={total} height={total} fill="#ffffff" />
      <path d={path} fill="#182016" />
    </svg>
  );
}

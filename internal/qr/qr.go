package qr

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Code represents a rendered QR matrix (true = dark module, false = light module).
type Code struct {
	Size    int
	Version int
	modules []bool
}

// At reports whether the module at (x, y) is dark. Out-of-bounds coordinates return false (quiet zone).
func (c *Code) At(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.modules[y*c.Size+x]
}

type versionSpec struct {
	version   int
	totalCW   int
	ecPerBlk  int
	g1Blocks  int
	g1DataCW  int
	g2Blocks  int
	g2DataCW  int
	alignment []int
}

// ISO/IEC 18004 ECC Level M specification for versions 1 through 15.
var specsM = []versionSpec{
	{1, 26, 10, 1, 16, 0, 0, nil},
	{2, 44, 16, 1, 28, 0, 0, []int{6, 18}},
	{3, 70, 26, 1, 44, 0, 0, []int{6, 22}},
	{4, 100, 18, 2, 32, 0, 0, []int{6, 26}},
	{5, 134, 24, 2, 43, 0, 0, []int{6, 30}},
	{6, 172, 16, 4, 27, 0, 0, []int{6, 34}},
	{7, 196, 18, 4, 31, 0, 0, []int{6, 22, 38}},
	{8, 242, 22, 2, 38, 2, 39, []int{6, 24, 42}},
	{9, 292, 22, 3, 36, 2, 37, []int{6, 26, 46}},
	{10, 346, 26, 4, 43, 1, 44, []int{6, 28, 50}},
	{11, 404, 30, 1, 50, 4, 51, []int{6, 30, 54}},
	{12, 466, 22, 6, 36, 2, 37, []int{6, 32, 58}},
	{13, 532, 22, 8, 37, 1, 38, []int{6, 34, 62}},
	{14, 581, 24, 4, 40, 5, 41, []int{6, 26, 46, 66}},
	{15, 655, 24, 5, 41, 5, 42, []int{6, 26, 48, 70}},
}

// Encode encodes data as a Byte-mode QR code with ECC Level M.
func Encode(data string) (*Code, error) {
	raw := []byte(data)
	if len(raw) == 0 {
		return nil, errors.New("qr: empty payload")
	}
	var spec *versionSpec
	for i := range specsM {
		s := &specsM[i]
		dataCW := s.g1Blocks*s.g1DataCW + s.g2Blocks*s.g2DataCW
		countBits := 8
		if s.version >= 10 {
			countBits = 16
		}
		if 4+countBits+len(raw)*8 <= dataCW*8 {
			spec = s
			break
		}
	}
	if spec == nil {
		return nil, fmt.Errorf("qr: payload too large (%d bytes)", len(raw))
	}
	codewords := encodeDataCodewords(raw, spec)
	interleaved := addErrorCorrection(codewords, spec)
	size := 17 + 4*spec.version

	base := newGrid(size)
	placeFunctionPatterns(base, spec)
	placeDataBits(base, interleaved)

	bestMask := 0
	bestPenalty := math.MaxInt
	var bestModules []bool
	for mask := range 8 {
		candidate := make([]bool, size*size)
		copy(candidate, base.modules)
		applyMask(candidate, base.reserved, size, mask)
		placeFormatBits(candidate, size, mask)
		if spec.version >= 7 {
			placeVersionBits(candidate, size, spec.version)
		}
		if score := penaltyScore(candidate, size); score < bestPenalty {
			bestPenalty = score
			bestMask = mask
			bestModules = candidate
		}
	}
	_ = bestMask
	return &Code{Size: size, Version: spec.version, modules: bestModules}, nil
}

// Terminal renders the QR code using compact Unicode half-blocks with a 2-module quiet zone.
// When color is true, explicit black-on-white ANSI styling is used so dark terminals scan reliably.
func (c *Code) Terminal(color bool) string {
	const margin = 2
	total := c.Size + margin*2
	var b strings.Builder
	for y := -margin; y < c.Size+margin; y += 2 {
		if color {
			b.WriteString("\x1b[30;47m")
		}
		for x := -margin; x < c.Size+margin; x++ {
			top := c.At(x, y)
			bottom := false
			if y+1 < c.Size+margin {
				bottom = c.At(x, y+1)
			}
			// Foreground is dark (black), background is light (white) when color is enabled.
			// When color is false, invert blocks for typical dark terminals? No: use standard
			// dark-on-light half-blocks with inverted polarity only if requested; wait:
			// in unstyled output, space is background and full block is foreground.
			if color {
				switch {
				case top && bottom:
					b.WriteRune('█')
				case top && !bottom:
					b.WriteRune('▀')
				case !top && bottom:
					b.WriteRune('▄')
				default:
					b.WriteRune(' ')
				}
			} else {
				// Without ANSI background, light modules are spaces and dark modules are blocks on light terminals,
				// or vice versa; keep direct polarity so At(x,y)==true renders filled block.
				_ = total
				switch {
				case top && bottom:
					b.WriteRune('█')
				case top && !bottom:
					b.WriteRune('▀')
				case !top && bottom:
					b.WriteRune('▄')
				default:
					b.WriteRune(' ')
				}
			}
		}
		if color {
			b.WriteString("\x1b[0m")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

type bitBuffer struct {
	bytes []byte
	bits  int
}

func (b *bitBuffer) push(val, count int) {
	for i := count - 1; i >= 0; i-- {
		if b.bits%8 == 0 {
			b.bytes = append(b.bytes, 0)
		}
		if (val>>i)&1 == 1 {
			b.bytes[len(b.bytes)-1] |= 1 << (7 - (b.bits % 8))
		}
		b.bits++
	}
}

func encodeDataCodewords(raw []byte, spec *versionSpec) []byte {
	dataCW := spec.g1Blocks*spec.g1DataCW + spec.g2Blocks*spec.g2DataCW
	countBits := 8
	if spec.version >= 10 {
		countBits = 16
	}
	var buf bitBuffer
	buf.push(0b0100, 4)
	buf.push(len(raw), countBits)
	for _, ch := range raw {
		buf.push(int(ch), 8)
	}
	rem := dataCW*8 - buf.bits
	if rem > 4 {
		rem = 4
	}
	if rem > 0 {
		buf.push(0, rem)
	}
	for buf.bits%8 != 0 {
		buf.push(0, 1)
	}
	pads := []byte{0xEC, 0x11}
	for i := 0; len(buf.bytes) < dataCW; i++ {
		buf.bytes = append(buf.bytes, pads[i%2])
	}
	return buf.bytes
}

var gfExp [512]byte
var gfLog [256]byte

func init() {
	x := 1
	for i := range 255 {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func rsGenerator(ecCount int) []byte {
	gen := []byte{1}
	for i := range ecCount {
		next := make([]byte, len(gen)+1)
		factor := gfExp[i]
		for j, coef := range gen {
			next[j] ^= coef
			next[j+1] ^= gfMul(coef, factor)
		}
		gen = next
	}
	return gen
}

func rsRemainder(data []byte, ecCount int) []byte {
	gen := rsGenerator(ecCount)
	rem := make([]byte, ecCount)
	for _, b := range data {
		factor := b ^ rem[0]
		copy(rem, rem[1:])
		rem[ecCount-1] = 0
		for i := range ecCount {
			rem[i] ^= gfMul(gen[i+1], factor)
		}
	}
	return rem
}

func addErrorCorrection(data []byte, spec *versionSpec) []byte {
	numBlocks := spec.g1Blocks + spec.g2Blocks
	blocks := make([][]byte, numBlocks)
	ecs := make([][]byte, numBlocks)
	offset := 0
	maxData := spec.g1DataCW
	if spec.g2Blocks > 0 && spec.g2DataCW > maxData {
		maxData = spec.g2DataCW
	}
	for i := range numBlocks {
		cw := spec.g1DataCW
		if i >= spec.g1Blocks {
			cw = spec.g2DataCW
		}
		blocks[i] = data[offset : offset+cw]
		ecs[i] = rsRemainder(blocks[i], spec.ecPerBlk)
		offset += cw
	}
	out := make([]byte, 0, spec.totalCW)
	for col := range maxData {
		for i := range numBlocks {
			if col < len(blocks[i]) {
				out = append(out, blocks[i][col])
			}
		}
	}
	for col := range spec.ecPerBlk {
		for i := range numBlocks {
			out = append(out, ecs[i][col])
		}
	}
	return out
}

type grid struct {
	size     int
	modules  []bool
	reserved []bool
}

func newGrid(size int) *grid {
	return &grid{
		size:     size,
		modules:  make([]bool, size*size),
		reserved: make([]bool, size*size),
	}
}

func (g *grid) setFunc(x, y int, dark bool) {
	if x < 0 || y < 0 || x >= g.size || y >= g.size {
		return
	}
	idx := y*g.size + x
	g.modules[idx] = dark
	g.reserved[idx] = true
}

func placeFinder(g *grid, ox, oy int) {
	for dy := -1; dy <= 7; dy++ {
		for dx := -1; dx <= 7; dx++ {
			x, y := ox+dx, oy+dy
			if x < 0 || y < 0 || x >= g.size || y >= g.size {
				continue
			}
			dark := dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6 &&
				(dx == 0 || dx == 6 || dy == 0 || dy == 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4))
			g.setFunc(x, y, dark)
		}
	}
}

func placeAlignment(g *grid, cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			dark := maxAbs(dx, dy) == 2 || (dx == 0 && dy == 0)
			g.setFunc(cx+dx, cy+dy, dark)
		}
	}
}

func maxAbs(a, b int) int {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}

func placeFunctionPatterns(g *grid, spec *versionSpec) {
	placeFinder(g, 0, 0)
	placeFinder(g, g.size-7, 0)
	placeFinder(g, 0, g.size-7)

	for i := 8; i < g.size-8; i++ {
		dark := i%2 == 0
		g.setFunc(i, 6, dark)
		g.setFunc(6, i, dark)
	}

	pos := spec.alignment
	for i, cy := range pos {
		for j, cx := range pos {
			if (i == 0 && j == 0) || (i == 0 && j == len(pos)-1) || (i == len(pos)-1 && j == 0) {
				continue
			}
			placeAlignment(g, cx, cy)
		}
	}

	// Reserve format info strips + dark module.
	for i := range 9 {
		if i != 6 {
			g.setFunc(8, i, false)
			g.setFunc(i, 8, false)
		}
	}
	for i := g.size - 8; i < g.size; i++ {
		g.setFunc(i, 8, false)
	}
	for i := g.size - 7; i < g.size; i++ {
		g.setFunc(8, i, false)
	}
	g.setFunc(8, g.size-8, true)

	if spec.version >= 7 {
		for i := range 6 {
			for j := range 3 {
				g.setFunc(g.size-11+j, i, false)
				g.setFunc(i, g.size-11+j, false)
			}
		}
	}
}

func placeDataBits(g *grid, codewords []byte) {
	bitIdx := 0
	totalBits := len(codewords) * 8
	up := true
	for right := g.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for step := range g.size {
			y := g.size - 1 - step
			if !up {
				y = step
			}
			for dx := range 2 {
				x := right - dx
				idx := y*g.size + x
				if g.reserved[idx] {
					continue
				}
				dark := false
				if bitIdx < totalBits {
					dark = (codewords[bitIdx/8]>>(7-(bitIdx%8)))&1 == 1
					bitIdx++
				}
				g.modules[idx] = dark
			}
		}
		up = !up
	}
}

func applyMask(modules, reserved []bool, size, mask int) {
	for y := range size {
		for x := range size {
			idx := y*size + x
			if reserved[idx] {
				continue
			}
			var invert bool
			switch mask {
			case 0:
				invert = (x+y)%2 == 0
			case 1:
				invert = y%2 == 0
			case 2:
				invert = x%3 == 0
			case 3:
				invert = (x+y)%3 == 0
			case 4:
				invert = (y/2+x/3)%2 == 0
			case 5:
				invert = (x*y)%2+(x*y)%3 == 0
			case 6:
				invert = ((x*y)%2+(x*y)%3)%2 == 0
			case 7:
				invert = ((x+y)%2+(x*y)%3)%2 == 0
			}
			if invert {
				modules[idx] = !modules[idx]
			}
		}
	}
}

func bchFormat(ecBits, mask int) int {
	data := (ecBits << 3) | mask
	rem := data << 10
	const gen = 0x537
	for i := 14; i >= 10; i-- {
		if (rem>>i)&1 != 0 {
			rem ^= gen << (i - 10)
		}
	}
	return ((data << 10) | rem) ^ 0x5412
}

func placeFormatBits(modules []bool, size, mask int) {
	// ECC Level M indicator = 00
	bits := bchFormat(0b00, mask)
	for i := range 15 {
		dark := (bits>>i)&1 == 1
		if i < 6 {
			modules[i*size+8] = dark
		} else if i < 8 {
			modules[(i+1)*size+8] = dark
		} else {
			modules[(size-15+i)*size+8] = dark
		}
		if i < 8 {
			modules[8*size+(size-1-i)] = dark
		} else if i < 9 {
			modules[8*size+7] = dark
		} else {
			modules[8*size+(14-i)] = dark
		}
	}
	modules[(size-8)*size+8] = true
}

func bchVersion(version int) int {
	rem := version << 12
	const gen = 0x1f25
	for i := 17; i >= 12; i-- {
		if (rem>>i)&1 != 0 {
			rem ^= gen << (i - 12)
		}
	}
	return (version << 12) | rem
}

func placeVersionBits(modules []bool, size, version int) {
	bits := bchVersion(version)
	for i := range 18 {
		dark := (bits>>i)&1 == 1
		a := size - 11 + (i % 3)
		b := i / 3
		modules[b*size+a] = dark
		modules[a*size+b] = dark
	}
}

func penaltyScore(modules []bool, size int) int {
	score := 0
	// Rule 1: 5+ consecutive same-color modules in rows and columns.
	for y := range size {
		runColor := modules[y*size]
		runLen := 1
		for x := 1; x < size; x++ {
			c := modules[y*size+x]
			if c == runColor {
				runLen++
			} else {
				if runLen >= 5 {
					score += 3 + (runLen - 5)
				}
				runColor = c
				runLen = 1
			}
		}
		if runLen >= 5 {
			score += 3 + (runLen - 5)
		}
	}
	for x := range size {
		runColor := modules[x]
		runLen := 1
		for y := 1; y < size; y++ {
			c := modules[y*size+x]
			if c == runColor {
				runLen++
			} else {
				if runLen >= 5 {
					score += 3 + (runLen - 5)
				}
				runColor = c
				runLen = 1
			}
		}
		if runLen >= 5 {
			score += 3 + (runLen - 5)
		}
	}
	// Rule 2: 2x2 same-color blocks.
	for y := range size - 1 {
		for x := range size - 1 {
			c := modules[y*size+x]
			if modules[y*size+x+1] == c && modules[(y+1)*size+x] == c && modules[(y+1)*size+x+1] == c {
				score += 3
			}
		}
	}
	// Rule 3: finder-like patterns 10111010000 or 00001011101.
	matchPattern := func(get func(int) bool, length int) int {
		hits := 0
		for i := 0; i <= length-11; i++ {
			a := get(i) && !get(i+1) && get(i+2) && get(i+3) && get(i+4) && !get(i+5) && get(i+6) && !get(i+7) && !get(i+8) && !get(i+9) && !get(i+10)
			b := !get(i) && !get(i+1) && !get(i+2) && !get(i+3) && get(i+4) && !get(i+5) && get(i+6) && get(i+7) && get(i+8) && !get(i+9) && get(i+10)
			if a || b {
				hits += 40
			}
		}
		return hits
	}
	for y := range size {
		row := y * size
		score += matchPattern(func(i int) bool { return modules[row+i] }, size)
	}
	for x := range size {
		col := x
		score += matchPattern(func(i int) bool { return modules[i*size+col] }, size)
	}
	// Rule 4: dark module balance.
	dark := 0
	for _, m := range modules {
		if m {
			dark++
		}
	}
	pct := (dark * 100) / (size * size)
	prev := (pct / 5) * 5
	next := prev + 5
	d1 := prev - 50
	if d1 < 0 {
		d1 = -d1
	}
	d2 := next - 50
	if d2 < 0 {
		d2 = -d2
	}
	if d2 < d1 {
		d1 = d2
	}
	score += (d1 / 5) * 10
	return score
}

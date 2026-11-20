package rax

// Netgear's Broadcom-based routers (RAX series and others) store their
// configuration backup as an LZW-compressed XML tree. The LZW variant is the
// TIFF-style code (MSB-first bit packing, 9→12 bit codes) with a few
// Netgear-specific quirks:
//
//   - a CLEAR code (256) is emitted at the start of the stream,
//   - the code table is flushed with CLEAR once it reaches 4094 entries,
//   - an END_OF_INFORMATION code (257) terminates the stream.
//
// This matches the reference decompressor in joeatwork/python-lzw
// (lzw.PagingDecoder), which is also what the Zyxel/Broadcom tooling uses.

const (
	lzwClear        = 256
	lzwEOI          = 257
	lzwInitCodeSize = 258  // 256 literal codes + CLEAR + EOI
	lzwMaxCode      = 4094 // flush the code table before assigning this code
)

// bitReader reads bits MSB-first from a byte slice.
type bitReader struct {
	data []byte
	pos  int // bit offset; 0 is the MSB of data[0]
}

func (r *bitReader) read(n int) (uint32, bool) {
	if r.pos+n > len(r.data)*8 {
		return 0, false
	}
	var v uint32
	for i := 0; i < n; i++ {
		byteIdx := r.pos / 8
		bitIdx := 7 - (r.pos % 8)
		v = (v << 1) | uint32((r.data[byteIdx]>>uint(bitIdx))&1)
		r.pos++
	}
	return v, true
}

// lzwDecompress decodes a Netgear LZW stream.
func lzwDecompress(data []byte) ([]byte, error) {
	codebook := make(map[uint32][]byte, 4096)
	for i := 0; i < 256; i++ {
		codebook[uint32(i)] = []byte{byte(i)}
	}
	codebook[lzwClear] = nil
	codebook[lzwEOI] = nil

	var prefix []byte
	r := &bitReader{data: data}
	width := 9
	codesize := uint32(lzwInitCodeSize)

	out := make([]byte, 0, len(data)*4)
	for {
		code, ok := r.read(width)
		if !ok {
			break
		}
		codesize++
		switch code {
		case lzwClear:
			codebook = make(map[uint32][]byte, 4096)
			for i := 0; i < 256; i++ {
				codebook[uint32(i)] = []byte{byte(i)}
			}
			codebook[lzwClear] = nil
			codebook[lzwEOI] = nil
			prefix = nil
			width = 9
			codesize = uint32(lzwInitCodeSize)
		case lzwEOI:
			// Align to the next byte boundary and stop. This stream format has
			// a single page terminated by EOI.
			if r.pos%8 != 0 {
				r.pos += 8 - r.pos%8
			}
			return out, nil
		default:
			for codesize >= (1 << uint(width)) {
				width++
			}
			var ret []byte
			if v, ok := codebook[code]; ok {
				ret = v
				if prefix != nil {
					codebook[uint32(len(codebook))] = append(append([]byte{}, prefix...), ret[0])
				}
			} else {
				// The "KωKωK" special case: the next code is not in the table yet.
				ret = append(append([]byte{}, prefix...), prefix[0])
				codebook[uint32(len(codebook))] = ret
			}
			prefix = ret
			out = append(out, ret...)
		}
	}
	return out, nil
}

// lzwCompress encodes data using the Netgear LZW stream format.
func lzwCompress(data []byte) []byte {
	dict := make(map[string]uint32, 4096)
	for i := 0; i < 256; i++ {
		dict[string([]byte{byte(i)})] = uint32(i)
	}
	next := uint32(lzwInitCodeSize)

	cps := make([]uint32, 0, len(data)/2+64)
	cps = append(cps, lzwClear)

	w := make([]byte, 0, 16)
	for _, c := range data {
		wc := make([]byte, len(w)+1)
		copy(wc, w)
		wc[len(w)] = c
		if _, ok := dict[string(wc)]; ok {
			w = wc
			continue
		}
		cps = append(cps, dict[string(w)])
		if next < lzwMaxCode {
			dict[string(wc)] = next
			next++
		} else {
			cps = append(cps, lzwClear)
			dict = make(map[string]uint32, 4096)
			for i := 0; i < 256; i++ {
				dict[string([]byte{byte(i)})] = uint32(i)
			}
			next = uint32(lzwInitCodeSize)
		}
		w = []byte{c}
	}
	if len(w) > 0 {
		cps = append(cps, dict[string(w)])
	}
	cps = append(cps, lzwEOI)

	return packCodepoints(cps)
}

// packCodepoints packs LZW codepoints into a MSB-first, variable-width byte
// stream (9 bits growing to 12, resetting to 9 on CLEAR/EOI).
func packCodepoints(cps []uint32) []byte {
	var out []byte
	var tail []bool
	codesize := uint32(lzwInitCodeSize)
	width := 9
	for _, pt := range cps {
		for i := width - 1; i >= 0; i-- {
			tail = append(tail, (pt>>uint(i))&1 == 1)
		}
		codesize++
		switch pt {
		case lzwClear, lzwEOI:
			if pt == lzwEOI {
				for len(tail)%8 != 0 {
					tail = append(tail, false)
				}
			}
			width = 9
			codesize = uint32(lzwInitCodeSize)
		default:
			if codesize >= (1 << uint(width)) {
				width++
			}
		}
		for len(tail) >= 8 {
			var b byte
			for i := 0; i < 8; i++ {
				b <<= 1
				if tail[i] {
					b |= 1
				}
			}
			out = append(out, b)
			tail = tail[8:]
		}
	}
	if len(tail) > 0 {
		var b byte
		for _, bit := range tail {
			b <<= 1
			if bit {
				b |= 1
			}
		}
		b <<= uint(8 - len(tail))
		out = append(out, b)
	}
	return out
}

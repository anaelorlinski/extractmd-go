package doc

import (
	"encoding/binary"
	"strconv"
	"unicode/utf16"

	"github.com/anaelorlinski/extractmd-go/internal/wordnum"
)

const (
	sprmPIlvl = 0x260A
	sprmPIlfo = 0x460B
	sprmPIstd = 0x4600
)

// listRef is a paragraph's or style's list reference; -1 means not set
type listRef struct {
	ilfo, ilvl int
}

// readLists loads list definitions (PlfLst + LVLs) and list instances (PlfLfo) into a wordnum.Numbering
func (w *WordOleExtractor) readLists(buffer, tableBuffer []byte) {
	defer func() { _ = recover() }() // malformed list tables only cost the numbers
	w.numbering = wordnum.New()
	if len(buffer) < 0x2F2 {
		return
	}
	fcLst := binary.LittleEndian.Uint32(buffer[0x02E2:])
	lcbLst := binary.LittleEndian.Uint32(buffer[0x02E6:])
	fcLfo := binary.LittleEndian.Uint32(buffer[0x02EA:])
	lcbLfo := binary.LittleEndian.Uint32(buffer[0x02EE:])
	if lcbLst < 2 || int(fcLst) >= len(tableBuffer) {
		return
	}

	// PlfLst: cLst LSTF structures, then the LVLs of every list in the same order
	tb := tableBuffer
	cLst := int(int16(binary.LittleEndian.Uint16(tb[fcLst:])))
	pos := int(fcLst) + 2
	type lstf struct {
		lsid   string
		simple bool
	}
	lists := make([]lstf, 0, cLst)
	for i := 0; i < cLst; i++ {
		lists = append(lists, lstf{
			lsid:   strconv.Itoa(int(int32(binary.LittleEndian.Uint32(tb[pos:])))),
			simple: tb[pos+26]&0x01 != 0,
		})
		pos += 28
	}
	for _, l := range lists {
		n := 9
		if l.simple {
			n = 1
		}
		levels := map[int]*wordnum.Level{}
		for ilvl := 0; ilvl < n; ilvl++ {
			var lv *wordnum.Level
			lv, pos = parseLVL(tb, pos)
			levels[ilvl] = lv
		}
		w.numbering.Abstracts[l.lsid] = levels
	}

	// PlfLfo: lfoMac LFO structures, then one LFOData per LFO with its level overrides
	if lcbLfo < 4 || int(fcLfo) >= len(tb) {
		return
	}
	lfoMac := int(int32(binary.LittleEndian.Uint32(tb[fcLfo:])))
	pos = int(fcLfo) + 4
	clfolvl := make([]int, lfoMac)
	for i := 0; i < lfoMac; i++ {
		numID := strconv.Itoa(i + 1) // paragraphs refer to LFOs 1-based
		w.numbering.NumToAbs[numID] = strconv.Itoa(int(int32(binary.LittleEndian.Uint32(tb[pos:]))))
		clfolvl[i] = int(tb[pos+12])
		pos += 16
	}
	for i := 0; i < lfoMac; i++ {
		pos += 4 // cp
		numID := strconv.Itoa(i + 1)
		for j := 0; j < clfolvl[i]; j++ {
			start := int(int32(binary.LittleEndian.Uint32(tb[pos:])))
			flags := tb[pos+4]
			ilvl := int(flags & 0x0F)
			pos += 8
			if flags&0x10 != 0 { // fStartAt
				w.numbering.SetStart(numID, ilvl, start)
			}
			if flags&0x20 != 0 { // fFormatting: the level is redefined for this instance
				var lv *wordnum.Level
				lv, pos = parseLVL(tb, pos)
				w.overrideLevel(numID, ilvl, lv)
			}
		}
	}
}

// overrideLevel gives a list instance its own copy of the definition with one level replaced
func (w *WordOleExtractor) overrideLevel(numID string, ilvl int, lv *wordnum.Level) {
	abs := w.numbering.NumToAbs[numID]
	copyID := abs + "/" + numID
	if _, ok := w.numbering.Abstracts[copyID]; !ok {
		levels := map[int]*wordnum.Level{}
		for k, v := range w.numbering.Abstracts[abs] {
			levels[k] = v
		}
		w.numbering.Abstracts[copyID] = levels
		w.numbering.NumToAbs[numID] = copyID
	}
	w.numbering.Abstracts[copyID][ilvl] = lv
}

// parseLVL reads an LVL (LVLF, grpprlPapx, grpprlChpx, xst) and returns the position after it
func parseLVL(tb []byte, pos int) (*wordnum.Level, int) {
	lv := &wordnum.Level{
		Start:  int(int32(binary.LittleEndian.Uint32(tb[pos:]))),
		Format: nfcFormat(tb[pos+4]),
	}
	cbChpx, cbPapx := int(tb[pos+24]), int(tb[pos+25])
	pos += 28 + cbPapx + cbChpx
	cch := int(binary.LittleEndian.Uint16(tb[pos:]))
	pos += 2
	text := make([]uint16, cch)
	for i := range text {
		text[i] = binary.LittleEndian.Uint16(tb[pos+2*i:])
	}
	pos += 2 * cch
	// Characters 0-8 are placeholders for the numbers of levels 1-9
	var b []rune
	for _, r := range utf16.Decode(text) {
		if r < 9 {
			b = append(b, '%', rune('1'+r))
		} else {
			b = append(b, r)
		}
	}
	lv.Text = string(b)
	return lv, pos
}

func nfcFormat(nfc byte) string {
	switch nfc {
	case 1:
		return "upperRoman"
	case 2:
		return "lowerRoman"
	case 3:
		return "upperLetter"
	case 4:
		return "lowerLetter"
	case 22:
		return "decimalZero"
	case 23:
		return "bullet"
	case 255:
		return "none"
	}
	return "decimal"
}

// listOf resolves a paragraph's list reference; ilfo and ilvl are inherited independently along style bases
func (w *WordOleExtractor) listOf(run paragraphRun) (numID string, ilvl int, ok bool) {
	ilfo, lvl := run.list.ilfo, run.list.ilvl
	istd := run.istd
	for depth := 0; depth < 10 && (ilfo < 0 || lvl < 0) && int(istd) < len(w.styles); depth++ {
		st := w.styles[istd]
		if ilfo < 0 {
			ilfo = st.list.ilfo
		}
		if lvl < 0 {
			lvl = st.list.ilvl
		}
		if st.base == 0x0FFF {
			break
		}
		istd = st.base
	}
	if ilfo <= 0 || ilfo > 2047 {
		return "", 0, false
	}
	return strconv.Itoa(ilfo), max(lvl, 0), true
}

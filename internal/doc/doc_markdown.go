package doc

import (
	"encoding/binary"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

const (
	sprmPOutLvl    = 0x2640
	sprmPFInTable  = 0x2416
	sprmPFTtp      = 0x2417
	sprmPFInnerTtp = 0x244C
	sprmTDefTable  = 0xD608
	noOutlineLevel = 9
)

// paragraphRun is a range of the WordDocument stream (file positions) sharing paragraph properties
type paragraphRun struct {
	fcStart, fcEnd uint32
	istd           uint16
	outline        int // 0-8, or noOutlineLevel
	inTable        bool
	rowEnd         bool
	list           listRef
}

type docStyle struct {
	sti      uint16 // built-in style identifier: 1-9 = heading 1-9, 19-27 = toc 1-9
	base     uint16 // istdBase, 0x0FFF = none
	name     string
	outline  int
	hasLevel bool
	list     listRef
}

func newStyle() docStyle {
	return docStyle{base: 0x0FFF, outline: noOutlineLevel, list: listRef{-1, -1}}
}

func (w *WordOleExtractor) recordParagraph(fcStart, fcEnd uint32, grpPrlAndIstd []byte) {
	run := paragraphRun{fcStart: fcStart, fcEnd: fcEnd, istd: binary.LittleEndian.Uint16(grpPrlAndIstd), outline: noOutlineLevel, list: listRef{-1, -1}}
	processSprms(grpPrlAndIstd, 2, func(buffer []byte, offset int, sprm uint16, _ uint16, _ uint8, _ uint8, _ uint8) {
		if offset >= len(buffer) {
			return
		}
		switch sprm {
		case sprmPOutLvl:
			run.outline = int(buffer[offset])
		case sprmPFInTable:
			run.inTable = buffer[offset] != 0
		case sprmPFTtp, sprmPFInnerTtp:
			run.rowEnd = run.rowEnd || buffer[offset] != 0
		case sprmTDefTable:
			run.rowEnd = true
		case sprmPIlvl:
			run.list.ilvl = int(buffer[offset])
		case sprmPIstd:
			if offset+1 < len(buffer) {
				run.istd = binary.LittleEndian.Uint16(buffer[offset:])
			}
		case sprmPIlfo:
			if offset+1 < len(buffer) {
				run.list.ilfo = int(int16(binary.LittleEndian.Uint16(buffer[offset:])))
			}
		}
	})
	w.paragraphs = append(w.paragraphs, run)
}

// readStyles parses the style sheet (STSH) for style names, built-in identifiers, bases and outline levels
func (w *WordOleExtractor) readStyles(buffer, tableBuffer []byte) {
	defer func() { _ = recover() }() // a malformed style sheet only costs the headings
	fc := binary.LittleEndian.Uint32(buffer[0x00A2:])
	lcb := binary.LittleEndian.Uint32(buffer[0x00A6:])
	if lcb < 4 || uint64(fc)+uint64(lcb) > uint64(len(tableBuffer)) {
		return
	}
	stsh := tableBuffer[fc : fc+lcb]
	cbStshi := int(binary.LittleEndian.Uint16(stsh))
	cstd := int(binary.LittleEndian.Uint16(stsh[2:]))
	cbBase := int(binary.LittleEndian.Uint16(stsh[4:]))
	pos := 2 + cbStshi
	for i := 0; i < cstd && pos+2 <= len(stsh); i++ {
		cbStd := int(binary.LittleEndian.Uint16(stsh[pos:]))
		pos += 2
		st := newStyle()
		if cbStd > 0 && pos+cbStd <= len(stsh) {
			st = parseStd(stsh[pos:pos+cbStd], cbBase)
		}
		w.styles = append(w.styles, st)
		pos += cbStd
	}
}

func parseStd(std []byte, cbBase int) docStyle {
	st := newStyle()
	if len(std) < 10 || cbBase < 10 || cbBase > len(std) {
		return st
	}
	st.sti = binary.LittleEndian.Uint16(std) & 0x0FFF
	w2 := binary.LittleEndian.Uint16(std[2:])
	stk := w2 & 0x000F
	st.base = w2 >> 4
	cupx := int(binary.LittleEndian.Uint16(std[4:]) & 0x000F)

	pos := cbBase
	if pos+2 > len(std) {
		return st
	}
	cch := int(binary.LittleEndian.Uint16(std[pos:]))
	pos += 2
	if pos+2*cch > len(std) {
		return st
	}
	name := make([]uint16, cch)
	for i := range name {
		name[i] = binary.LittleEndian.Uint16(std[pos+2*i:])
	}
	st.name = string(utf16.Decode(name))
	pos += 2*cch + 2 // name and its terminating zero

	// Paragraph styles: the first UPX holds the istd and the paragraph sprms
	if stk != 1 || cupx < 1 {
		return st
	}
	if pos%2 == 1 {
		pos++
	}
	if pos+2 > len(std) {
		return st
	}
	cbUpx := int(binary.LittleEndian.Uint16(std[pos:]))
	pos += 2
	if cbUpx < 2 || pos+cbUpx > len(std) {
		return st
	}
	processSprms(std[pos:pos+cbUpx], 2, func(buffer []byte, offset int, sprm uint16, _ uint16, _ uint8, _ uint8, _ uint8) {
		if offset >= len(buffer) {
			return
		}
		switch sprm {
		case sprmPOutLvl:
			st.outline = int(buffer[offset])
			st.hasLevel = true
		case sprmPIlvl:
			st.list.ilvl = int(buffer[offset])
		case sprmPIlfo:
			if offset+1 < len(buffer) {
				st.list.ilfo = int(int16(binary.LittleEndian.Uint16(buffer[offset:])))
			}
		}
	})
	return st
}

// headingLevel returns the 0-based heading level of a style, or -1
func (w *WordOleExtractor) headingLevel(istd uint16) int {
	for depth := 0; depth < 10 && int(istd) < len(w.styles); depth++ {
		st := w.styles[istd]
		if st.sti >= 1 && st.sti <= 9 {
			return int(st.sti) - 1
		}
		if st.hasLevel {
			if st.outline < noOutlineLevel {
				return st.outline
			}
			return -1
		}
		if st.base == 0x0FFF {
			break
		}
		istd = st.base
	}
	return -1
}

func (w *WordOleExtractor) isTOCStyle(istd uint16) bool {
	if int(istd) >= len(w.styles) {
		return false
	}
	st := w.styles[istd]
	name := strings.ToLower(st.name)
	return (st.sti >= 19 && st.sti <= 27) || strings.HasPrefix(name, "toc") || strings.HasPrefix(name, "table of figures")
}

func (w *WordOleExtractor) paragraphAt(fc uint32) paragraphRun {
	i := sort.Search(len(w.paragraphs), func(i int) bool { return w.paragraphs[i].fcEnd > fc })
	if i < len(w.paragraphs) && w.paragraphs[i].fcStart <= fc {
		return w.paragraphs[i]
	}
	return paragraphRun{outline: noOutlineLevel, list: listRef{-1, -1}}
}

// filePosAt maps a character position of the main text to its WordDocument stream offset
func (w *WordOleExtractor) filePosAt(cp int) uint32 {
	for _, p := range w.pieces {
		if cp >= p.StartCp && cp < p.EndCp {
			return uint32(p.StartFilePos + (cp-p.StartCp)*p.Bpc)
		}
	}
	return 0
}

type fieldFrame struct {
	inCode bool
	code   strings.Builder
	drop   bool
}

// bodyMarkdown renders the main text with headings from paragraph styles, tables as rows,
// field codes removed and tables of contents dropped
func (w *WordOleExtractor) bodyMarkdown() string {
	if len(w.paragraphs) == 0 || len(w.styles) == 0 {
		return ""
	}
	sort.Slice(w.paragraphs, func(i, j int) bool { return w.paragraphs[i].fcStart < w.paragraphs[j].fcStart })

	var out []string
	var para strings.Builder
	var cells []string
	var cellParts []string
	tableRows := 0
	var fields []*fieldFrame

	emitting := func() bool {
		for _, f := range fields {
			if f.inCode || f.drop {
				return false
			}
		}
		return true
	}
	endTable := func() {
		tableRows = 0
	}
	emitRow := func() {
		para.Reset()
		if len(cellParts) > 0 {
			cells = append(cells, strings.Join(cellParts, "<br>"))
			cellParts = nil
		}
		if strings.TrimSpace(strings.Join(cells, "")) != "" {
			out = append(out, "| "+strings.Join(cells, " | ")+" |")
			if tableRows == 0 {
				out = append(out, "|"+strings.Repeat(" --- |", len(cells)))
			}
			tableRows++
		}
		cells = nil
	}
	finishParagraph := func(cp int, inCell bool) {
		text := strings.Join(strings.Fields(para.String()), " ")
		para.Reset()
		run := w.paragraphAt(w.filePosAt(cp))
		// Word counts empty numbered paragraphs too, so advance the counters before skipping anything
		var label string
		var ilvl int
		numbered := false
		if numID, lvl, ok := w.listOf(run); ok && w.numbering != nil {
			ilvl = lvl
			label, numbered = w.numbering.Next(numID, lvl)
		}
		if inCell || run.inTable {
			if text != "" {
				if numbered && label != "" {
					text = label + " " + text
				}
				cellParts = append(cellParts, text)
			}
			return
		}
		if text == "" || w.isTOCStyle(run.istd) {
			return
		}
		if len(cells) > 0 || len(cellParts) > 0 {
			emitRow() // row whose end mark wasn't recognised
		}
		if tableRows > 0 {
			endTable()
		}
		level := run.outline
		if level >= noOutlineLevel {
			level = w.headingLevel(run.istd)
		}
		switch {
		case level >= 0 && level < noOutlineLevel && len(text) <= 200:
			if numbered && label != "" && label != "-" {
				text = label + " " + text
			}
			text = strings.Repeat("#", min(level+1, 6)) + " " + text
		case numbered && label != "":
			text = strings.Repeat("  ", ilvl) + label + " " + text
		}
		out = append(out, text)
	}

	cp := 0
	end := w.boundaries.CcpText
	for _, p := range w.pieces {
		units := utf16.Encode([]rune(p.Text))
		for i, u := range units {
			cp = p.StartCp + i
			if cp >= end {
				break
			}
			switch u {
			case 0x13:
				fields = append(fields, &fieldFrame{inCode: true})
			case 0x14:
				if n := len(fields); n > 0 {
					f := fields[n-1]
					f.inCode = false
					code := strings.ToUpper(strings.TrimSpace(f.code.String()))
					f.drop = strings.HasPrefix(code, "TOC") || (n > 1 && fields[n-2].drop)
				}
			case 0x15:
				if n := len(fields); n > 0 {
					fields = fields[:n-1]
				}
			case 0x0D:
				finishParagraph(cp, false)
			case 0x07: // end of cell, or end of row when the paragraph carries row properties
				if w.paragraphAt(w.filePosAt(cp)).rowEnd {
					emitRow()
					continue
				}
				finishParagraph(cp, true)
				cells = append(cells, strings.Join(cellParts, "<br>"))
				cellParts = nil
			case 0x0A: // end of table row (row-end marks were turned into "\n" by writeParagraphProperties)
				emitRow()
			case 0x0B, 0x09, 0xA0:
				if emitting() {
					para.WriteByte(' ')
				}
			case 0x0C, 0x0E: // page / column break
				if emitting() {
					para.WriteByte(' ')
				}
			case 0x1E:
				if emitting() {
					para.WriteByte('-')
				}
			default:
				if u < 0x20 || u == 0x1F {
					continue
				}
				if n := len(fields); n > 0 && fields[n-1].inCode {
					fields[n-1].code.WriteRune(rune(u))
					continue
				}
				if emitting() {
					para.WriteString(string(utf16.Decode([]uint16{u})))
				}
			}
		}
	}
	if para.Len() > 0 {
		finishParagraph(cp, false)
	}
	if len(cells) > 0 || len(cellParts) > 0 {
		emitRow()
	}

	// Rows of one table and items of one list are consecutive lines; other blocks are separated by a blank line
	var b strings.Builder
	for i, line := range out {
		if i > 0 {
			prev := out[i-1]
			switch {
			case strings.HasPrefix(line, "|") && strings.HasPrefix(prev, "|"),
				listItem.MatchString(line) && listItem.MatchString(prev):
				b.WriteString("\n")
			default:
				b.WriteString("\n\n")
			}
		}
		b.WriteString(line)
	}
	return b.String()
}

var listItem = regexp.MustCompile(`^\s*(- |\S{1,12}[.)] )`)

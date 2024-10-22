package pdfmd

import (
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/anaelorlinski/extractmd-go/internal/pagemark"
	"github.com/ledongthuc/pdf"
)

type pdfLine struct {
	text   string
	y      float64
	size   float64
	bold   bool
	header bool // repeated page header/footer
}

var (
	pdfDigits        = regexp.MustCompile(`\d+`)
	pdfNumberPrefix  = regexp.MustCompile(`^(\d+(?:\.\d+)*)\.?\s+(\S.*)$`)
	pdfPageNumberish = regexp.MustCompile(`(?i)^(page\s*)?\d+(\s*(/|of)\s*\d+)?$`)
	pdfTOCLine       = regexp.MustCompile(`(\.\s*){8,}[\divxlc\s-]*$`)
)

// Convert extracts the text of a PDF as Markdown. Headings come from the PDF bookmarks when present,
// otherwise from font size and bold numbered lines; repeated page headers/footers are dropped.
// Pages the parser can't read are skipped and reported to log.
func Convert(path string, log *slog.Logger) (md string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf parser panic: %v", r)
		}
	}()
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	outline := map[string]int{}
	var walk func(o pdf.Outline, depth int)
	walk = func(o pdf.Outline, depth int) {
		if t := pdfNorm(o.Title); depth > 0 && t != "" {
			if _, ok := outline[t]; !ok {
				outline[t] = depth
			}
		}
		for _, c := range o.Child {
			walk(c, depth+1)
		}
	}
	walk(r.Outline(), 0)

	var pages [][]pdfLine
	var pageNums []int
	for i := 1; i <= r.NumPage(); i++ {
		if lines, ok := pdfReadPage(r, i, log); ok {
			pages = append(pages, lines)
			pageNums = append(pageNums, i)
		}
	}
	markRepeatedLines(pages)

	body := pdfBodySize(pages)
	sizeLevel := pdfHeadingSizes(pages, body)

	var out []string
	var para []string
	var lastY, lastSize float64
	flush := func() {
		if len(para) > 0 {
			out = append(out, strings.Join(para, " "))
			para = nil
		}
	}
	for pi, lines := range pages {
		flush()
		out = append(out, pagemark.Marker(pageNums[pi]))
		for _, l := range lines {
			if l.header || pdfPageNumberish.MatchString(l.text) || pdfTOCLine.MatchString(l.text) {
				continue
			}
			if level := pdfHeadingLevel(l, outline, sizeLevel, body); level > 0 {
				flush()
				out = append(out, strings.Repeat("#", min(level, 6))+" "+l.text)
				lastY = 0
				continue
			}
			gap := lastY - l.y
			if len(para) > 0 && (gap > 1.8*math.Max(l.size, lastSize) || gap < 0 || math.Abs(l.size-lastSize) > 1) {
				flush()
			}
			if n := len(para); n > 0 && strings.HasSuffix(para[n-1], "-") && l.text != "" && l.text[0] >= 'a' && l.text[0] <= 'z' {
				para[n-1] = strings.TrimSuffix(para[n-1], "-") + l.text
			} else {
				para = append(para, l.text)
			}
			lastY, lastSize = l.y, l.size
		}
	}
	flush()
	return strings.Join(out, "\n\n") + "\n", nil
}

// pdfReadPage extracts the lines of one page; a page the parser can't read is skipped instead of failing the document
func pdfReadPage(r *pdf.Reader, i int, log *slog.Logger) (lines []pdfLine, ok bool) {
	defer func() {
		if e := recover(); e != nil {
			log.Warn("PDF page skipped", "page", i, "error", e)
			ok = false
		}
	}()
	p := r.Page(i)
	if p.V.IsNull() {
		return nil, false
	}
	return pdfPageLines(pdfUnrotate(p.Content().Text)), true
}

// pdfUnrotate turns a page whose text mostly runs vertically (rotated by 90°) into horizontal lines
func pdfUnrotate(texts []pdf.Text) []pdf.Text {
	var up, down, total int
	for _, t := range texts {
		if t.S == "" {
			continue
		}
		total++
		switch {
		case math.Abs(t.Angle-math.Pi/2) < 0.1:
			up++
		case math.Abs(t.Angle+math.Pi/2) < 0.1:
			down++
		}
	}
	if (up+down)*2 < total {
		return texts
	}
	out := make([]pdf.Text, 0, len(texts))
	for _, t := range texts {
		switch {
		case up >= down && math.Abs(t.Angle-math.Pi/2) < 0.1: // bottom to top: the first line has the smallest x
			t.X, t.Y = t.Y, -t.X
		case down > up && math.Abs(t.Angle+math.Pi/2) < 0.1:
			t.X, t.Y = -t.Y, t.X
		default:
			continue // text in the other orientations would land in the wrong place
		}
		out = append(out, t)
	}
	return out
}

// pdfPageLines groups the glyphs of a page into lines, top to bottom
func pdfPageLines(texts []pdf.Text) []pdfLine {
	items := make([]pdf.Text, 0, len(texts))
	type cell struct {
		s    string
		x, y int
	}
	seen := map[cell][]pdf.Text{}
	for _, t := range texts {
		// The parser appends a "\n" pseudo-glyph after each TJ run, which can fall inside a word
		if t.S == "" || t.S == "\n" || t.FontSize <= 0 {
			continue
		}
		// Fake bold draws the same glyph several times with sub-point offsets (narrowest real glyphs are ~0.2 em apart)
		x, y := int(math.Floor(t.X)), int(math.Floor(t.Y))
		tol := 0.1 * t.FontSize
		dup := false
		for dx := -1; dx <= 1 && !dup; dx++ {
			for dy := -1; dy <= 1 && !dup; dy++ {
				for _, o := range seen[cell{t.S, x + dx, y + dy}] {
					if math.Abs(o.X-t.X) < tol && math.Abs(o.Y-t.Y) < tol {
						dup = true
						break
					}
				}
			}
		}
		if dup && t.S != " " {
			continue
		}
		seen[cell{t.S, x, y}] = append(seen[cell{t.S, x, y}], t)
		items = append(items, t)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Y > items[j].Y })

	var lines []pdfLine
	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && items[start].Y-items[end].Y < 0.5*items[start].FontSize {
			end++
		}
		row := items[start:end]
		sort.SliceStable(row, func(i, j int) bool { return row[i].X < row[j].X })

		var b strings.Builder
		var chars, boldChars int
		var sizeSum float64
		prevEnd := math.Inf(-1)
		for _, t := range row {
			gap := t.X - prevEnd
			switch {
			case gap > 3*t.FontSize && b.Len() > 0:
				b.WriteString(" | ") // wide gap: separate columns (tables)
			case gap > 0.15*t.FontSize && b.Len() > 0 && !strings.HasSuffix(b.String(), " ") && t.S != " ":
				b.WriteByte(' ')
			}
			b.WriteString(t.S)
			prevEnd = t.X + t.W
			n := len([]rune(t.S))
			chars += n
			sizeSum += t.FontSize * float64(n)
			if f := strings.ToLower(t.Font); strings.Contains(f, "bold") || strings.Contains(f, "heavy") || strings.Contains(f, "black") {
				boldChars += n
			}
		}
		if text := strings.Join(strings.Fields(strings.Map(pdfCleanRune, b.String())), " "); text != "" {
			lines = append(lines, pdfLine{
				text: text,
				y:    row[0].Y,
				size: math.Round(sizeSum/float64(chars)*2) / 2,
				bold: boldChars*10 >= chars*8,
			})
		}
		start = end
	}
	return lines
}

// markRepeatedLines flags lines at the top or bottom of pages that repeat on many pages (headers/footers)
func markRepeatedLines(pages [][]pdfLine) {
	if len(pages) < 3 {
		return
	}
	const edge = 3
	key := func(l pdfLine) string { return pdfDigits.ReplaceAllString(strings.ToLower(l.text), "#") }
	count := map[string]int{}
	for _, lines := range pages {
		seen := map[string]bool{}
		for i, l := range lines {
			if i < edge || i >= len(lines)-edge {
				if k := key(l); !seen[k] {
					seen[k] = true
					count[k]++
				}
			}
		}
	}
	threshold := max(3, len(pages)/2)
	kept := map[string]bool{}
	for _, lines := range pages {
		for i := range lines {
			if (i < edge || i >= len(lines)-edge) && count[key(lines[i])] >= threshold {
				// Keep the first copy: running headers often hold the document title or table column names
				if k := key(lines[i]); !kept[k] {
					kept[k] = true
					continue
				}
				lines[i].header = true
			}
		}
	}
}

// pdfBodySize is the most common font size, weighted by text length
func pdfBodySize(pages [][]pdfLine) float64 {
	weight := map[float64]int{}
	for _, lines := range pages {
		for _, l := range lines {
			if !l.header {
				weight[l.size] += len(l.text)
			}
		}
	}
	best, bestW := 0.0, -1
	for s, w := range weight {
		if w > bestW || (w == bestW && s < best) {
			best, bestW = s, w
		}
	}
	return best
}

// pdfHeadingSizes maps font sizes clearly larger than the body text to heading levels (largest = 1)
func pdfHeadingSizes(pages [][]pdfLine, body float64) map[float64]int {
	seen := map[float64]bool{}
	for _, lines := range pages {
		for _, l := range lines {
			if !l.header && l.size >= body*1.15 && len(l.text) <= 150 {
				seen[l.size] = true
			}
		}
	}
	sizes := make([]float64, 0, len(seen))
	for s := range seen {
		sizes = append(sizes, s)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(sizes)))
	levels := map[float64]int{}
	for i, s := range sizes {
		levels[s] = min(i+1, 4)
	}
	return levels
}

func pdfHeadingLevel(l pdfLine, outline map[string]int, sizeLevel map[float64]int, body float64) int {
	if len(l.text) > 150 {
		return 0
	}
	if len(outline) > 0 {
		// Bookmarks can also point at diagram labels, so require some emphasis
		if !l.bold && l.size < body*1.1 && !pdfNumberPrefix.MatchString(l.text) {
			return 0
		}
		t := pdfNorm(l.text)
		if level, ok := outline[t]; ok {
			return level
		}
		if m := pdfNumberPrefix.FindStringSubmatch(l.text); m != nil {
			if level, ok := outline[pdfNorm(m[2])]; ok {
				return level
			}
		}
		return 0
	}
	if strings.HasSuffix(l.text, ".") || strings.HasSuffix(l.text, ",") {
		return 0
	}
	if level, ok := sizeLevel[l.size]; ok {
		return level
	}
	// Bold body-size line starting with a section number such as "2.1.3 Title"
	if l.bold && l.size >= body && len(l.text) <= 100 {
		if m := pdfNumberPrefix.FindStringSubmatch(l.text); m != nil && len(m[1]) < 12 {
			return min(strings.Count(m[1], ".")+1, 6)
		}
	}
	return 0
}

func pdfNorm(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// pdfCleanRune drops undecodable glyphs and control characters (e.g. symbol-font bullets)
func pdfCleanRune(r rune) rune {
	if r == '\uFFFD' || (r < 0x20 && r != '\t' && r != '\n') || r == 0x7F {
		return -1
	}
	return r
}

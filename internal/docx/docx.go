package docx

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/anaelorlinski/extractmd-go/internal/pagemark"
	"github.com/anaelorlinski/extractmd-go/internal/wordnum"
)

const (
	nsWordML = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsMC     = "http://schemas.openxmlformats.org/markup-compatibility/2006"
)

// xnode is a minimal XML tree used by the DOCX converter.
type xnode struct {
	space, local string
	attrs        []xml.Attr
	children     []*xnode
	text         string
}

func (n *xnode) is(local string) bool { return n.space == nsWordML && n.local == local }

func (n *xnode) attr(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) child(local string) *xnode {
	for _, c := range n.children {
		if c.is(local) {
			return c
		}
	}
	return nil
}

func parseXMLTree(r io.Reader) (*xnode, error) {
	dec := xml.NewDecoder(r)
	root := &xnode{}
	stack := []*xnode{root}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return root, nil
		}
		if err != nil {
			return nil, err
		}
		cur := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			// Fallback duplicates the Choice content (e.g. text boxes as VML)
			if t.Name.Space == nsMC && t.Name.Local == "Fallback" {
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			n := &xnode{space: t.Name.Space, local: t.Name.Local, attrs: t.Attr}
			cur.children = append(cur.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if cur.space == nsWordML && (cur.local == "t" || cur.local == "instrText" || cur.local == "delText") {
				cur.text += string(t)
			}
		}
	}
}

type docxStyle struct {
	name, basedOn  string
	outline        int // -1 when not set
	numID, numIlvl string
}

type docxConverter struct {
	styles    map[string]*docxStyle
	abstracts map[string]map[int]*wordnum.Level
	numToAbs  map[string]string
	// numStyleLink indirection: abstractNum -> numbering style name, and the abstractNum/num that defines it
	absStyleRef   map[string]string
	styleLinkAbs  map[string]string
	numStyleNumID map[string]string
	num           *wordnum.Numbering

	page        int
	useRendered bool // count w:lastRenderedPageBreak (Word's layout at last save)
	pages       bool // emit page markers
}

// Convert converts a .docx file to Markdown: headings (with their numbering), lists, tables and footnotes.
// The table of contents, page headers/footers, comments and tracked deletions are dropped.
func Convert(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open docx: %w", err)
	}
	defer zr.Close()

	parts := map[string]*xnode{}
	savedPages := 0
	for _, f := range zr.File {
		switch f.Name {
		case "docProps/app.xml":
			if rc, err := f.Open(); err == nil {
				if data, err := io.ReadAll(io.LimitReader(rc, 1<<20)); err == nil {
					if m := appPagesRe.FindSubmatch(data); m != nil {
						savedPages, _ = strconv.Atoi(string(m[1]))
					}
				}
				rc.Close()
			}
		case "word/document.xml", "word/styles.xml", "word/numbering.xml", "word/footnotes.xml":
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			tree, err := parseXMLTree(rc)
			rc.Close()
			if err != nil {
				return "", fmt.Errorf("parse %s: %w", f.Name, err)
			}
			parts[f.Name] = tree
		}
	}
	doc := parts["word/document.xml"]
	if doc == nil {
		return "", fmt.Errorf("word/document.xml not found")
	}

	c := &docxConverter{
		styles:        map[string]*docxStyle{},
		abstracts:     map[string]map[int]*wordnum.Level{},
		numToAbs:      map[string]string{},
		absStyleRef:   map[string]string{},
		styleLinkAbs:  map[string]string{},
		numStyleNumID: map[string]string{},
		num:           wordnum.New(),
	}
	c.loadStyles(parts["word/styles.xml"])
	c.loadNumbering(parts["word/numbering.xml"])
	c.num.Abstracts = c.abstracts
	for id, abs := range c.numToAbs {
		c.num.NumToAbs[id] = c.resolveAbs(abs)
	}

	var out []string
	var body *xnode
	for _, n := range doc.children {
		if n.is("document") {
			body = n.child("body")
		}
	}
	if body != nil {
		// Page numbers come from the layout Word saved; only trust it when it adds up to the saved page count
		c.useRendered = true
		before, after := c.pageBreaks(body)
		c.pages = savedPages > 1 && before+after+1 == savedPages
		c.page = 1
		if c.pages {
			out = append(out, pagemark.Marker(1))
		}
		out = c.blocks(body, out)
	}

	if fn := parts["word/footnotes.xml"]; fn != nil {
		var notes []string
		for _, root := range fn.children {
			for _, note := range root.children {
				if !note.is("footnote") || (note.attr("type") != "" && note.attr("type") != "normal") {
					continue
				}
				var sb strings.Builder
				for _, p := range note.children {
					if p.is("p") {
						sb.WriteString(c.inlineText(p, nil))
						sb.WriteString(" ")
					}
				}
				if t := cleanSpaces(sb.String()); t != "" {
					notes = append(notes, "- "+t)
				}
			}
		}
		if len(notes) > 0 {
			out = append(out, "## Footnotes", strings.Join(notes, "\n"))
		}
	}

	return joinBlocks(out), nil
}

// joinBlocks separates blocks with a blank line, keeping consecutive list items together.
func joinBlocks(blocks []string) string {
	var sb strings.Builder
	prevList := false
	for i, b := range blocks {
		isList := listItemRe.MatchString(b)
		if i > 0 {
			if isList && prevList {
				sb.WriteString("\n")
			} else {
				sb.WriteString("\n\n")
			}
		}
		sb.WriteString(b)
		prevList = isList
	}
	return sb.String() + "\n"
}

var listItemRe = regexp.MustCompile(`^\s*(- |\S{1,12}[.)] )`)

func (c *docxConverter) loadStyles(root *xnode) {
	if root == nil {
		return
	}
	for _, styles := range root.children {
		for _, s := range styles.children {
			if s.is("style") && s.attr("type") == "numbering" {
				if ppr := s.child("pPr"); ppr != nil {
					if np := ppr.child("numPr"); np != nil {
						if id := np.child("numId"); id != nil {
							c.numStyleNumID[s.attr("styleId")] = id.attr("val")
						}
					}
				}
				continue
			}
			if !s.is("style") || s.attr("type") != "paragraph" {
				continue
			}
			st := &docxStyle{outline: -1}
			if n := s.child("name"); n != nil {
				st.name = strings.ToLower(n.attr("val"))
			}
			if b := s.child("basedOn"); b != nil {
				st.basedOn = b.attr("val")
			}
			if ppr := s.child("pPr"); ppr != nil {
				if o := ppr.child("outlineLvl"); o != nil {
					if v, err := strconv.Atoi(o.attr("val")); err == nil {
						st.outline = v
					}
				}
				if np := ppr.child("numPr"); np != nil {
					if id := np.child("numId"); id != nil {
						st.numID = id.attr("val")
					}
					if il := np.child("ilvl"); il != nil {
						st.numIlvl = il.attr("val")
					}
				}
			}
			c.styles[s.attr("styleId")] = st
		}
	}
}

func (c *docxConverter) loadNumbering(root *xnode) {
	if root == nil {
		return
	}
	for _, numbering := range root.children {
		for _, n := range numbering.children {
			switch {
			case n.is("abstractNum"):
				absID := n.attr("abstractNumId")
				if l := n.child("styleLink"); l != nil {
					c.styleLinkAbs[l.attr("val")] = absID
				}
				if l := n.child("numStyleLink"); l != nil {
					c.absStyleRef[absID] = l.attr("val")
				}
				levels := map[int]*wordnum.Level{}
				for _, l := range n.children {
					if !l.is("lvl") {
						continue
					}
					ilvl, _ := strconv.Atoi(l.attr("ilvl"))
					lv := &wordnum.Level{Format: "decimal", Start: 1}
					if f := l.child("numFmt"); f != nil {
						lv.Format = f.attr("val")
					}
					if t := l.child("lvlText"); t != nil {
						lv.Text = t.attr("val")
					}
					if s := l.child("start"); s != nil {
						lv.Start, _ = strconv.Atoi(s.attr("val"))
					}
					levels[ilvl] = lv
				}
				c.abstracts[absID] = levels
			case n.is("num"):
				id := n.attr("numId")
				if a := n.child("abstractNumId"); a != nil {
					c.numToAbs[id] = a.attr("val")
				}
				for _, o := range n.children {
					if !o.is("lvlOverride") {
						continue
					}
					if so := o.child("startOverride"); so != nil {
						ilvl, _ := strconv.Atoi(o.attr("ilvl"))
						v, _ := strconv.Atoi(so.attr("val"))
						c.num.SetStart(id, ilvl, v)
					}
				}
			}
		}
	}
}

// resolveAbs follows numStyleLink references to the abstractNum that actually defines the levels.
func (c *docxConverter) resolveAbs(absID string) string {
	for i := 0; i < 5; i++ {
		ref, ok := c.absStyleRef[absID]
		if !ok {
			return absID
		}
		next, ok := c.styleLinkAbs[ref]
		if !ok {
			next = c.numToAbs[c.numStyleNumID[ref]]
		}
		if next == "" || next == absID {
			return absID
		}
		absID = next
	}
	return absID
}

// styleChain resolves an inherited property along basedOn links.
func (c *docxConverter) styleChain(id string, f func(*docxStyle) bool) {
	for i := 0; id != "" && i < 20; i++ {
		st := c.styles[id]
		if st == nil || f(st) {
			return
		}
		id = st.basedOn
	}
}

func (c *docxConverter) headingLevel(styleID string) int {
	level := -1
	c.styleChain(styleID, func(st *docxStyle) bool {
		if st.outline >= 0 && st.outline < 9 {
			level = st.outline
			return true
		}
		if strings.HasPrefix(st.name, "heading ") {
			if v, err := strconv.Atoi(strings.TrimPrefix(st.name, "heading ")); err == nil && v >= 1 && v <= 9 {
				level = v - 1
				return true
			}
		}
		return false
	})
	return level
}

func (c *docxConverter) isTOCStyle(styleID string) bool {
	st := c.styles[styleID]
	return st != nil && (strings.HasPrefix(st.name, "toc ") || st.name == "toc heading" || st.name == "table of figures")
}

// numbering returns the list label (e.g. "2.1.3", "-", "a)") for a numbered paragraph.
func (c *docxConverter) numbering(ppr *xnode, styleID string) (label string, ilvl int, ok bool) {
	numID, lvl := "", ""
	// numId and ilvl are inherited independently (e.g. Heading2 sets only ilvl, numId comes from Heading1)
	c.styleChain(styleID, func(st *docxStyle) bool {
		if numID == "" {
			numID = st.numID
		}
		if lvl == "" {
			lvl = st.numIlvl
		}
		return numID != "" && lvl != ""
	})
	if ppr != nil {
		if np := ppr.child("numPr"); np != nil {
			if id := np.child("numId"); id != nil {
				numID = id.attr("val")
			}
			if il := np.child("ilvl"); il != nil {
				lvl = il.attr("val")
			}
		}
	}
	if numID == "" || numID == "0" {
		return "", 0, false
	}
	ilvl, _ = strconv.Atoi(lvl)
	label, ok = c.num.Next(numID, ilvl)
	return label, ilvl, ok
}

// blocks converts the block-level children of body/cell/sdtContent into Markdown blocks.
func (c *docxConverter) blocks(parent *xnode, out []string) []string {
	for _, n := range parent.children {
		switch {
		case n.is("p"):
			out = c.paragraph(n, out)
		case n.is("tbl"):
			before, after := c.pageBreaks(n)
			out = c.newPage(out, before)
			if t := c.table(n); t != "" {
				out = append(out, t)
			}
			out = c.newPage(out, after)
		case n.is("sdt"):
			if content := n.child("sdtContent"); content != nil {
				out = c.blocks(content, out)
			}
		case n.is("customXml"):
			out = c.blocks(n, out)
		}
	}
	return out
}

func (c *docxConverter) paragraph(p *xnode, out []string) []string {
	before, after := c.pageBreaks(p)
	out = c.newPage(out, before)
	out = c.paragraphBlocks(p, out)
	return c.newPage(out, after)
}

// newPage advances the page counter by n and records the page start
func (c *docxConverter) newPage(out []string, n int) []string {
	if n <= 0 || !c.pages {
		return out
	}
	c.page += n
	return append(out, pagemark.Marker(c.page))
}

// appPagesRe reads the page count Word saved in docProps/app.xml
var appPagesRe = regexp.MustCompile(`<Pages>\s*(\d+)\s*</Pages>`)

// pageBreaks counts the page breaks in an element, split into those before its first text and the rest
func (c *docxConverter) pageBreaks(n *xnode) (before, after int) {
	seenText := false
	var walk func(*xnode)
	walk = func(x *xnode) {
		for _, ch := range x.children {
			switch {
			case ch.is("t") && strings.TrimSpace(ch.text) != "":
				seenText = true
			case ch.is("del"), ch.is("txbxContent"):
				continue
			case c.useRendered && ch.is("lastRenderedPageBreak"):
				if seenText {
					after++
				} else {
					before++
				}
			}
			walk(ch)
		}
	}
	walk(n)
	return before, after
}

func (c *docxConverter) paragraphBlocks(p *xnode, out []string) []string {
	ppr := p.child("pPr")
	styleID := ""
	if ppr != nil {
		if ps := ppr.child("pStyle"); ps != nil {
			styleID = ps.attr("val")
		}
	}
	if c.isTOCStyle(styleID) {
		return out
	}
	var extra []string
	text := cleanSpaces(c.inlineText(p, &extra))

	level := c.headingLevel(styleID)
	if ppr != nil {
		if o := ppr.child("outlineLvl"); o != nil {
			if v, err := strconv.Atoi(o.attr("val")); err == nil && v < 9 {
				level = v
			}
		}
	}
	// Word counts empty numbered paragraphs, except one that only holds a section break
	// and one whose paragraph mark is a tracked deletion (it merges into the next paragraph)
	var label string
	var ilvl int
	var numbered bool
	markDeleted := ppr != nil && ppr.child("rPr") != nil && ppr.child("rPr").child("del") != nil
	if !markDeleted && (text != "" || ppr == nil || ppr.child("sectPr") == nil) {
		label, ilvl, numbered = c.numbering(ppr, styleID)
	}

	if text != "" {
		switch {
		case level >= 0 && len(text) <= 200:
			if numbered && label != "" && label != "-" {
				text = label + " " + text
			}
			out = append(out, strings.Repeat("#", min(level+1, 6))+" "+text)
		case numbered && label != "":
			out = append(out, strings.Repeat("  ", ilvl)+label+" "+text)
		default:
			out = append(out, text)
		}
	}
	return append(out, extra...)
}

// inlineText collects the text of a paragraph; text box paragraphs are returned separately in extra.
func (c *docxConverter) inlineText(n *xnode, extra *[]string) string {
	var sb strings.Builder
	var walk func(*xnode)
	walk = func(x *xnode) {
		for _, ch := range x.children {
			if ch.space != nsWordML {
				walk(ch)
				continue
			}
			switch ch.local {
			case "t":
				sb.WriteString(ch.text)
			case "tab", "br", "cr":
				sb.WriteString(" ")
			case "noBreakHyphen":
				sb.WriteString("-")
			case "pPr", "rPr", "del", "delText", "instrText", "footnoteReference", "endnoteReference", "commentReference":
			case "txbxContent":
				if extra != nil {
					*extra = c.blocks(ch, *extra)
				}
			default:
				walk(ch)
			}
		}
	}
	walk(n)
	return sb.String()
}

func (c *docxConverter) table(tbl *xnode) string {
	var rows [][]string
	for _, tr := range tbl.children {
		if !tr.is("tr") {
			continue
		}
		var row []string
		for _, tc := range tr.children {
			if !tc.is("tc") {
				continue
			}
			row = append(row, c.cellText(tc))
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	empty := true
	var sb strings.Builder
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		for _, cell := range r {
			if cell != "" {
				empty = false
			}
		}
		sb.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			sb.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	if empty {
		return ""
	}
	return strings.TrimRight(sb.String(), "\n")
}

// cellText flattens a table cell (paragraphs, nested tables) to a single Markdown table cell.
func (c *docxConverter) cellText(tc *xnode) string {
	var parts []string
	var collect func(*xnode)
	collect = func(x *xnode) {
		for _, ch := range x.children {
			switch {
			case ch.is("p"):
				if t := cleanSpaces(c.inlineText(ch, nil)); t != "" {
					parts = append(parts, t)
				}
			case ch.is("tbl"), ch.is("tr"), ch.is("tc"), ch.is("sdt"), ch.is("sdtContent"), ch.is("customXml"):
				collect(ch)
			}
		}
	}
	collect(tc)
	return strings.ReplaceAll(strings.Join(parts, "<br>"), "|", `\|`)
}

var spaceRe = regexp.MustCompile(`[ \t\x{00a0}]+`)

func cleanSpaces(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

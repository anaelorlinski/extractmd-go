package extractmd

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes data to a file in a temporary directory and returns its path.
func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// docxFile builds a .docx file with the given document body XML and a style "Heading1" at outline level 0.
func docxFile(t *testing.T, body string) []byte {
	t.Helper()
	const w = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   `<?xml version="1.0"?><w:document ` + w + `><w:body>` + body + `</w:body></w:document>`,
		"word/styles.xml": `<?xml version="1.0"?><w:styles ` + w + `>` +
			`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style>` +
			`</w:styles>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range parts {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func paragraph(style, text string) string {
	ppr := ""
	if style != "" {
		ppr = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
	}
	return `<w:p>` + ppr + `<w:r><w:t>` + text + `</w:t></w:r></w:p>`
}

// pdfFile builds a PDF whose pages each show one line of text in Helvetica.
func pdfFile(pages ...string) []byte {
	n := len(pages)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"", // page tree, filled below
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}
	var kids []string
	for _, text := range pages {
		page, content := len(objs)+1, len(objs)+2
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", content),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n)

	var buf bytes.Buffer
	offsets := make([]int, len(objs)+1)
	buf.WriteString("%PDF-1.4\n")
	for i, obj := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

func TestDetectFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want Format
	}{
		{"a.docx", docxFile(t, ""), DOCX},
		{"a.doc", []byte("\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1rest"), DOC},
		{"a.pdf", pdfFile("x"), PDF},
		{"renamed.doc", pdfFile("x"), PDF},            // the content decides, not the extension
		{"junk.pdf", []byte("junk\n%PDF-1.4\n"), PDF}, // header after leading garbage
		{"a.txt", []byte("hello"), Unknown},
		{"empty", nil, Unknown},
	} {
		got, err := DetectFormat(writeFile(t, tc.name, tc.data))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
		} else if got != tc.want {
			t.Errorf("%s: format = %v, want %v", tc.name, got, tc.want)
		}
	}
	if _, err := DetectFormat(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file: no error")
	}
}

func TestConvertDOCX(t *testing.T) {
	path := writeFile(t, "a.docx", docxFile(t, paragraph("Heading1", "Scope")+paragraph("", "Some text.")))
	md, err := ConvertFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Scope\n\nSome text.\n"; md != want {
		t.Errorf("markdown = %q, want %q", md, want)
	}
}

func TestConvertPDF(t *testing.T) {
	path := writeFile(t, "a.pdf", pdfFile("First page", "Second page"))
	md, err := ConvertFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{PageMarker(1), "First page", PageMarker(2), "Second page"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown %q lacks %q", md, want)
		}
	}
	if strings.Index(md, "Second page") < strings.Index(md, PageMarker(2)) {
		t.Errorf("markdown %q: second page text before its marker", md)
	}
}

func TestConvertUnsupported(t *testing.T) {
	if _, err := ConvertFile(writeFile(t, "a.txt", []byte("hello"))); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := ConvertDOC(writeFile(t, "a.doc", []byte("not ole"))); err == nil {
		t.Error("ConvertDOC on a non-OLE file: no error")
	}
}

func TestPageMarker(t *testing.T) {
	if got := PageMarker(12); got != "<!-- page 12 -->" {
		t.Errorf("PageMarker(12) = %q", got)
	}
}

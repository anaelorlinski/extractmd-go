package doc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrNotOLE is returned for a file that is not an OLE compound document.
var ErrNotOLE = errors.New("not an OLE compound document")

// Extract reads the text parts of a Word 97-2003 (.doc) file.
func Extract(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	head := make([]byte, 2)
	if _, err := f.ReadAt(head, 0); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint16(head) != 0xD0CF {
		return nil, ErrNotOLE
	}
	return NewWordOleExtractor().Extract(f)
}

// Convert returns the Markdown of a .doc file: the body with its headings, lists and tables, then the page
// headers and footers, footnotes and endnotes. The body is plain text when its structure can't be read.
func Convert(path string) (string, error) {
	d, err := Extract(path)
	if err != nil {
		return "", fmt.Errorf("failed to extract DOC: %w", err)
	}
	var b strings.Builder
	if d.Markdown != "" {
		b.WriteString(d.GetMarkdown(nil))
	} else {
		b.WriteString(d.GetBody(nil))
	}
	b.WriteString("\n")
	b.WriteString(d.GetHeaders(nil))
	b.WriteString(d.GetFootnotes(nil))
	b.WriteString(d.GetEndnotes(nil))
	return b.String(), nil
}

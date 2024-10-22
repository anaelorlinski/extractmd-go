// Package extractmd converts documents to Markdown for search and retrieval.
//
// Word (.docx and .doc) and PDF files keep their structure: headings with their numbering, lists, tables
// and footnotes. When the page layout is known, each page starts with a marker line (see PageMarker), so
// that chunks of the Markdown can be traced back to their pages.
package extractmd

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/anaelorlinski/extractmd-go/internal/doc"
	"github.com/anaelorlinski/extractmd-go/internal/docx"
	"github.com/anaelorlinski/extractmd-go/internal/pagemark"
	"github.com/anaelorlinski/extractmd-go/internal/pdfmd"
)

// Format is a document format, detected from the file content.
type Format int

const (
	Unknown Format = iota
	DOCX           // Word 2007 and later (Office Open XML)
	DOC            // Word 97-2003 (OLE compound document)
	PDF
)

func (f Format) String() string {
	switch f {
	case DOCX:
		return "docx"
	case DOC:
		return "doc"
	case PDF:
		return "pdf"
	}
	return "unknown"
}

// ErrUnsupportedFormat is returned by ConvertFile for a file in no supported format.
var ErrUnsupportedFormat = errors.New("extractmd: unsupported format")

// DetectFormat returns the format of a file from its first bytes, whatever its extension.
func DetectFormat(path string) (Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return Unknown, err
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return Unknown, err
	}
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return DOCX, nil
	case bytes.HasPrefix(head, []byte("\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1")):
		return DOC, nil
	// The PDF header may follow some garbage within the first 1024 bytes
	case bytes.Contains(head, []byte("%PDF-")):
		return PDF, nil
	}
	return Unknown, nil
}

// Option configures a conversion.
type Option func(*options)

type options struct {
	logger *slog.Logger
}

// WithLogger reports recoverable problems, such as a PDF page that can't be read and is skipped.
// They are discarded by default.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

func newOptions(opts []Option) *options {
	o := &options{logger: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// ConvertFile converts a file to Markdown according to its detected format.
func ConvertFile(path string, opts ...Option) (string, error) {
	format, err := DetectFormat(path)
	if err != nil {
		return "", err
	}
	switch format {
	case DOCX:
		return ConvertDOCX(path, opts...)
	case DOC:
		return ConvertDOC(path, opts...)
	case PDF:
		return ConvertPDF(path, opts...)
	}
	return "", ErrUnsupportedFormat
}

// ConvertDOCX converts a Word .docx file. The table of contents, page headers and footers, comments and
// tracked deletions are left out. Page markers are present only when the page breaks Word saved add up to
// the saved page count.
func ConvertDOCX(path string, opts ...Option) (string, error) {
	return docx.Convert(path)
}

// ConvertDOC converts a Word 97-2003 .doc file: the body, then the page headers and footers, footnotes and
// endnotes. It has no page markers.
func ConvertDOC(path string, opts ...Option) (string, error) {
	return doc.Convert(path)
}

// ConvertPDF converts a PDF file. Headings come from the bookmarks when present, otherwise from font sizes
// and bold numbered lines. Repeated page headers and footers, page numbers and table of contents lines are
// left out. Every page starts with a page marker.
func ConvertPDF(path string, opts ...Option) (string, error) {
	return pdfmd.Convert(path, newOptions(opts).logger)
}

// PageMarker returns the line that starts page n (1-based) in the Markdown: "<!-- page n -->".
// It renders as nothing in Markdown viewers.
func PageMarker(n int) string {
	return pagemark.Marker(n)
}

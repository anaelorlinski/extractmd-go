# extractmd-go

Converts Word (`.docx`, `.doc`) and PDF documents to Markdown for search and retrieval (RAG), keeping headings, lists, tables and page markers. Pure Go.

```go
md, err := extractmd.ConvertFile("spec.pdf")
```

It depends on a patched [ledongthuc/pdf](https://github.com/ledongthuc/pdf) (`pdf.Text.Angle` and other fixes) and does not compile against upstream: add to your `go.mod`

```
replace github.com/ledongthuc/pdf => github.com/anaelorlinski/ledongthuc-pdf patchstack-1
```

then run `go mod tidy` to pin it.

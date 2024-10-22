# extractmd-go

Converts Word (`.docx`, `.doc`) and PDF documents to Markdown for search and retrieval (RAG), keeping headings, lists, tables and page markers. Pure Go.

```go
md, err := extractmd.ConvertFile("spec.pdf")
```

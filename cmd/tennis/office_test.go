package main

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func zipOf(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testDocx is a Word document holding the given body XML inside w:body.
func testDocx(t *testing.T, body string) []byte {
	t.Helper()
	return zipOf(t, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`,
	})
}

// TestDocxText: paragraphs become lines, runs join, a tab and a line break
// are the whitespace they stand for, a paragraph with nothing left in it is
// one blank line, and what Word shows struck through or as a field code is
// not what the document says.
func TestDocxText(t *testing.T) {
	doc := testDocx(t, `
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Tulum trip</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">We stayed at </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>Hotel Esencia</w:t></w:r><w:r><w:t>.</w:t></w:r></w:p>
<w:p><w:r><w:t>taxi</w:t><w:tab/><w:t>842</w:t><w:br/><w:t>dinner</w:t></w:r></w:p>
<w:p><w:del><w:r><w:delText>old price</w:delText></w:r></w:del><w:r><w:fldChar w:fldCharType="begin"/><w:instrText>PAGE</w:instrText></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell one</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>cell two</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
	got, err := docxText(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := "Tulum trip\nWe stayed at Hotel Esencia.\ntaxi 842\ndinner\n\ncell one\ncell two"
	if got != want {
		t.Errorf("docxText:\n%q\nwant:\n%q", got, want)
	}
	if _, err := docxText(zipOf(t, map[string]string{"notes/a.md": "x"})); err == nil {
		t.Error("a zip of notes passed as a Word document")
	}
}

// TestPptxText: a deck is its slides in order — numeric order, so slide 10
// does not come before slide 2 — each under its own heading.
func TestPptxText(t *testing.T) {
	slide := func(lines ...string) string {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody>`)
		for _, l := range lines {
			b.WriteString(`<a:p><a:r><a:t>` + l + `</a:t></a:r></a:p>`)
		}
		b.WriteString(`</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`)
		return b.String()
	}
	deck := zipOf(t, map[string]string{
		"ppt/slides/slide10.xml":            slide("Ten"),
		"ppt/slides/slide2.xml":             slide("Two", "second line"),
		"ppt/slides/_rels/slide2.xml.rels":  `<Relationships/>`,
		"ppt/slideLayouts/slideLayout1.xml": slide("layout text, not a slide"),
	})
	got, err := pptxText(deck)
	if err != nil {
		t.Fatal(err)
	}
	want := "## Slide 2\n\nTwo\nsecond line\n\n## Slide 10\n\nTen"
	if got != want {
		t.Errorf("pptxText:\n%q\nwant:\n%q", got, want)
	}
}

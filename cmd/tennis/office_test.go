package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
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

// TestDocxTextBoxes: Word writes a text box twice, as the modern shape and
// the old one in the two branches of an mc:AlternateContent; it is read
// once. Its paragraphs are lines of their own, after the line of the
// paragraph it is anchored in rather than run into the middle of it, and a
// box with only the old shape is still read.
func TestDocxTextBoxes(t *testing.T) {
	box := func(lines ...string) string {
		var b strings.Builder
		b.WriteString(`<w:txbxContent>`)
		for _, l := range lines {
			b.WriteString(`<w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:t>` + l + `</w:t></w:r></w:p>`)
		}
		b.WriteString(`</w:txbxContent>`)
		return b.String()
	}
	both := func(lines ...string) string {
		return `<w:r><mc:AlternateContent><mc:Choice Requires="wps"><w:drawing><wp:anchor distT="0" distB="0"><wp:extent cx="1" cy="1"/>` +
			`<a:graphic><a:graphicData uri="http://schemas.microsoft.com/office/word/2010/wordprocessingShape"><wps:wsp><wps:txbx>` + box(lines...) +
			`</wps:txbx><wps:bodyPr/></wps:wsp></a:graphicData></a:graphic></wp:anchor></w:drawing></mc:Choice>` +
			`<mc:Fallback><w:pict><v:shape id="Text Box 1"><v:textbox>` + box(lines...) + `</v:textbox></v:shape></w:pict></mc:Fallback></mc:AlternateContent></w:r>`
	}
	doc := zipOf(t, map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape" xmlns:v="urn:schemas-microsoft-com:vml" mc:Ignorable="wps"><w:body>
<w:p><w:r><w:t xml:space="preserve">Body </w:t></w:r>` + both("Callout text", "Second callout line") + `<w:r><w:t>paragraph.</w:t></w:r></w:p>
<w:p><w:r><w:t>Next paragraph.</w:t></w:r></w:p>
<w:p><w:r><w:t>Old box:</w:t></w:r><w:r><mc:AlternateContent><mc:Fallback><w:pict><v:shape><v:textbox>` + box("Fallback only") + `</v:textbox></v:shape></w:pict></mc:Fallback></mc:AlternateContent></w:r></w:p>
<w:p><w:r><w:t>Plain old box:</w:t></w:r><w:r><w:pict><v:shape><v:textbox>` + box("VML") + `</v:textbox></v:shape></w:pict></w:r><w:r><w:t>after</w:t></w:r></w:p>
</w:body></w:document>`,
	})
	got, err := docxText(doc)
	want := "Body paragraph.\nCallout text\nSecond callout line\nNext paragraph.\nOld box:\nFallback only\nPlain old box:after\nVML"
	if err != nil || got != want {
		t.Errorf("docxText:\n%q, %v\nwant:\n%q", got, err, want)
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

// bomb is a zip whose members are written by fill, deflated as they are
// written, so a test can build one that unpacks to far more than it holds
// without holding all of it.
func bomb(t *testing.T, parts map[string]func(w io.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, fill := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		fill(w)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// repeat is a fill that writes head, n copies of unit, and tail.
func repeat(head, unit, tail string, n int) func(io.Writer) {
	return func(w io.Writer) {
		io.WriteString(w, head)
		b := []byte(unit)
		for range n {
			w.Write(b)
		}
		io.WriteString(w, tail)
	}
}

// allocated is how many bytes fn asked the heap for, all told, collected
// since or not.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// allocBound is what reading one document past the text cap may cost. The
// bombs below cost gigabytes when a reader built their whole text before
// the cap was checked.
const allocBound = 100 << 20

// readsWithin reads a document with read, wanting err from the reader
// itself and an allocation under bound.
func readsWithin(t *testing.T, name string, read func([]byte) (string, error), body []byte, want error, bound uint64) string {
	t.Helper()
	var (
		text string
		err  error
	)
	n := allocated(func() { text, err = read(body) })
	if !errors.Is(err, want) {
		t.Errorf("%s (%dKB zipped): got %v, want %v", name, len(body)>>10, err, want)
	}
	if n > bound {
		t.Errorf("%s (%dKB zipped): reading it allocated %dMB, over %dMB", name, len(body)>>10, n>>20, bound>>20)
	}
	return text
}

// TestOfficeTextStopsAtTheCap: a Word file or deck whose text unpacks past
// the text cap is refused as soon as the text passes it, at a cost set by
// the cap and not by what the zip unpacks to.
func TestOfficeTextStopsAtTheCap(t *testing.T) {
	para := strings.Repeat("lorem ipsum dolor sit amet ", 40) // about 1KB
	doc := bomb(t, map[string]func(io.Writer){
		"word/document.xml": repeat(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`,
			`<w:p><w:r><w:t>`+para+`</w:t></w:r></w:p>`, `</w:body></w:document>`, 48<<10),
	})
	readsWithin(t, "bomb.docx", docxText, doc, errTextCap, allocBound)

	slides := map[string]func(io.Writer){}
	for i := 1; i <= 40; i++ {
		slides[fmt.Sprintf("ppt/slides/slide%d.xml", i)] = repeat(
			`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody>`,
			`<a:p><a:r><a:t>`+para+`</a:t></a:r></a:p>`, `</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, 1<<10)
	}
	readsWithin(t, "bomb.pptx", pptxText, bomb(t, slides), errTextCap, allocBound)
}

// TestZipMembersShareOneBudget: what a zip's members unpack to is held to
// one cap across all of them, exactly at the cap is within it, and once a
// member has gone past, every read after it fails too.
func TestZipMembersShareOneBudget(t *testing.T) {
	body := zipOf(t, map[string]string{"half": strings.Repeat("a", 512), "rest": strings.Repeat("b", 512), "more": "c"})
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	u := &unpacking{left: 1024}
	read := func(name string) error {
		rc, err := u.open(zipPart(zr, name))
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.ReadAll(rc)
		return err
	}
	for _, name := range []string{"half", "rest"} {
		if err := read(name); err != nil {
			t.Fatalf("%s, within the budget: %v", name, err)
		}
	}
	if err := read("more"); !errors.Is(err, errUnpackCap) {
		t.Errorf("a byte past the budget: got %v", err)
	}
	if err := read("half"); !errors.Is(err, errUnpackCap) {
		t.Errorf("a read after the budget was spent: got %v", err)
	}

	// Every reader of a zip reads through it, the parts that are not text
	// included.
	defer func(was int64) { unpackCap = was }(unpackCap)
	unpackCap = 1 << 20
	filler := repeat("", "<!-- filler -->", "", 100<<10) // 1.5MB
	for name, parts := range map[string]map[string]func(io.Writer){
		"big.docx": {"word/document.xml": filler},
		"big.pptx": {"ppt/slides/slide1.xml": filler},
		"big.xlsx": {"xl/workbook.xml": filler},
		"big.epub": {"META-INF/container.xml": filler},
	} {
		if _, err := fileText(entry(name, bomb(t, parts))); !errors.Is(err, errUnpackCap) {
			t.Errorf("%s: got %v, want %v", name, err, errUnpackCap)
		}
	}
}

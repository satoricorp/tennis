package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// entry is a fileEntry over bytes in memory, the shape a zip member has.
func entry(name string, body []byte) fileEntry {
	return fileEntry{name: name, size: int64(len(body)), open: func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}}
}

// onDisk is a fileEntry for a real file, the shape a directory add produces
// and the one an external converter needs.
func onDisk(t *testing.T, name string, body []byte) fileEntry {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return fileEntry{name: name, path: p, size: int64(len(body)), open: func() (io.ReadCloser, error) { return os.Open(p) }}
}

// testPDF is the smallest PDF that says something: one page, one line of
// text in a built-in font, an uncompressed content stream, and a correct
// cross-reference table so a strict reader does not have to rebuild one.
func testPDF(t *testing.T) []byte {
	t.Helper()
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		"",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	content := "BT /F1 24 Tf 72 700 Td (Hotel Esencia in Tulum) Tj ET"
	objects[3] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

// TestFileTextGate: every kind of file goes through one gate, and the gate
// has four answers — text, a reason, silence for media, and a cap.
func TestFileTextGate(t *testing.T) {
	if text, err := fileText(entry("a.md", []byte("# hi"))); err != nil || text != "# hi" {
		t.Errorf("plain text: %q, %v", text, err)
	}
	if text, err := fileText(entry("script.go", []byte("package main"))); err != nil || text != "package main" {
		t.Errorf("an unknown extension with text in it should be read as text: %q, %v", text, err)
	}
	if _, err := fileText(entry("IMG_0042.JPG", []byte("\xff\xd8\xff"))); !errors.Is(err, errNotText) {
		t.Errorf("a photo should be passed over quietly, got %v", err)
	}
	if _, err := fileText(entry("blob.bin", []byte("\x00\x01\x02"))); err == nil || err.Error() != "binary content" {
		t.Errorf("an unknown binary should be reported as one, got %v", err)
	}
	if text, err := fileText(entry("budget.xlsx", testWorkbook(t))); err != nil || !strings.Contains(text, "Rent (monthly)") {
		t.Errorf("a workbook should come out as its cells: %q, %v", text, err)
	}
	if _, err := fileText(entry("broken.xlsx", []byte("not a workbook"))); err == nil {
		t.Error("an unreadable workbook was accepted")
	}
	if _, err := fileText(entry("empty.docx", testDocx(t, ""))); err == nil || !strings.Contains(err.Error(), "no text") {
		t.Errorf("a document with nothing in it should say so, got %v", err)
	}
	big := fileEntry{name: "huge.txt", size: maxSeedFileSize + 1}
	if _, err := fileText(big); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("oversized text should hit the cap before being read, got %v", err)
	}
	bigDoc := fileEntry{name: "huge.pdf", size: maxDocumentSize + 1}
	if _, err := fileText(bigDoc); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("an oversized document should hit the document cap, got %v", err)
	}
	if text, err := fileText(entry("page.html", []byte("<html><head><title>x</title></head><body><p>Hello &amp; welcome</p></body></html>"))); err != nil || text != "x\n\nHello & welcome" {
		t.Errorf("html: %q, %v", text, err)
	}
}

func TestHidden(t *testing.T) {
	for p, want := range map[string]bool{
		"notes/auth.md": false, ".secret.md": true, "a/.cache/x.md": true, ".git/config": true,
		"a/b/c.txt": false, "../up.md": false, "dot.in.name.md": false,
	} {
		if got := hidden(p); got != want {
			t.Errorf("hidden(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestHTMLText: a page's words, without the parts of a page that are not
// words — scripts, styles, comments — with its title kept, its blocks on
// their own lines, and a non-breaking space treated as the space it is.
func TestHTMLText(t *testing.T) {
	page := `<!doctype html><html><head><title>Trip</title><style>p{color:red}</style>
<script>var x = "<p>not text</p>";</script></head>
<body><!-- a note --><h1>Tulum</h1><p>We stayed at <b>Hotel&nbsp;Esencia</b> &mdash; 9 June.</p>
<ul><li>taxi 842</li><li>dinner</li></ul></body></html>`
	got, err := htmlText([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	want := "Trip\n\nTulum\n\nWe stayed at Hotel Esencia — 9 June.\n\ntaxi 842\n\ndinner"
	if got != want {
		t.Errorf("htmlText:\n%q\nwant:\n%q", got, want)
	}
}

// TestSpotlightText: the one attribute wanted out of an importer dump, with
// its escapes undone.
func TestSpotlightText(t *testing.T) {
	dump := "Imported '/x/plan.pdf'\n{\n    kMDItemContentType = \"com.adobe.pdf\";\n" +
		`    kMDItemTextContent = "Tulum trip\nCaf\U00e9 \"Esencia\" \Ud83d\Ude00 tab\there A\101";` + "\n" +
		"    kMDItemTitle = \"<null>\";\n}\n"
	got := spotlightText(dump)
	want := "Tulum trip\nCafé \"Esencia\" 😀 tab\there AA"
	if got != want {
		t.Errorf("spotlightText:\n%q\nwant:\n%q", got, want)
	}
	if got := spotlightText("    kMDItemTextContent = \"<null>\";\n"); got != "" {
		t.Errorf("a null attribute should be no text, got %q", got)
	}
	if got := spotlightText("    kMDItemContentType = \"public.png\";\n"); got != "" {
		t.Errorf("a dump without the attribute should be no text, got %q", got)
	}
}

// TestExternalReaders runs the converters this machine has against known
// files, and skips where it has none. It is the test that a PDF actually
// reads, which no fixture of the parser's own can give.
func TestExternalReaders(t *testing.T) {
	pdf := onDisk(t, "plan.pdf", testPDF(t))

	t.Run("pdftotext", func(t *testing.T) {
		if _, err := exec.LookPath("pdftotext"); err != nil {
			t.Skip("pdftotext not installed")
		}
		got, err := readPDF(pdf)
		if err != nil || !strings.Contains(got, "Hotel Esencia in Tulum") {
			t.Errorf("readPDF via pdftotext: %q, %v", got, err)
		}
	})

	if runtime.GOOS != "darwin" {
		t.Skip("the rest are macOS converters")
	}
	t.Run("spotlight reads a pdf", func(t *testing.T) {
		got, err := readWithSpotlight(pdf)
		if err != nil || !strings.Contains(got, "Hotel Esencia in Tulum") {
			t.Errorf("readWithSpotlight: %q, %v", got, err)
		}
	})
	t.Run("spotlight reads a zip member through a temp file", func(t *testing.T) {
		got, err := readWithSpotlight(entry("plan.pdf", testPDF(t)))
		if err != nil || !strings.Contains(got, "Hotel Esencia in Tulum") {
			t.Errorf("readWithSpotlight on an in-memory entry: %q, %v", got, err)
		}
	})
	t.Run("textutil reads rtf", func(t *testing.T) {
		rtf := onDisk(t, "plan.rtf", []byte(`{\rtf1\ansi\deff0 {\fonttbl {\f0 Helvetica;}}\f0\fs24 Hotel Esencia in Tulum\par}`))
		got, err := readWithTextutil(rtf)
		if err != nil || !strings.Contains(got, "Hotel Esencia in Tulum") {
			t.Errorf("readWithTextutil: %q, %v", got, err)
		}
	})
	t.Run("garbage with a document's name is a reason, not a document", func(t *testing.T) {
		_, err := fileText(onDisk(t, "broken.pdf", []byte("not a pdf")))
		if err == nil {
			t.Error("garbage passed through the gate without complaint")
		}
	})
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
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
	for name, body := range map[string]string{"__init__.py": "", "blank.txt": "  \n\n \t\r\n", "bom.md": "\ufeff\n", "Icon\r": ""} {
		if _, err := fileText(entry(name, []byte(body))); !errors.Is(err, errNotText) {
			t.Errorf("%q holds no text, and should be passed over quietly; got %v", name, err)
		}
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

// TestEPUBText: a book is its spine's pages in spine order — not the
// manifest's order, not the zip's — with each href found relative to the
// package file. The table of contents is left out though the spine lists it,
// and so is the title every page repeats in its head.
func TestEPUBText(t *testing.T) {
	page := func(body string) string {
		return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Tulum</title></head><body>` + body + `</body></html>`
	}
	book := zipOf(t, map[string]string{
		"mimetype": "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Tulum</dc:title></metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="arrive" href="text/the%20arrival.xhtml" media-type="application/xhtml+xml"/>
    <item id="depart" href="text/departure.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="nav"/><itemref idref="depart"/><itemref idref="arrive"/></spine>
</package>`,
		"OEBPS/nav.xhtml":              page(`<nav><ol><li>Contents</li></ol></nav>`),
		"OEBPS/text/departure.xhtml":   page(`<h1>Leaving</h1><p>We left Austin on the 9 June flight.</p>`),
		"OEBPS/text/the arrival.xhtml": page(`<h1>Arriving</h1><p>Hotel&nbsp;Esencia, Tulum.</p>`),
	})
	want := "Leaving\n\nWe left Austin on the 9 June flight.\n\nArriving\n\nHotel Esencia, Tulum."
	got, err := epubText(book)
	if err != nil || got != want {
		t.Errorf("epubText:\n%q, %v\nwant:\n%q", got, err, want)
	}
	if got, err := fileText(entry("guide.epub", book)); err != nil || got != want {
		t.Errorf("an .epub should be read by tennis itself, not handed to the OS: %q, %v", got, err)
	}
	if _, err := epubText(zipOf(t, map[string]string{"OEBPS/text/departure.xhtml": "x"})); err == nil {
		t.Error("a zip with no container.xml passed as a book")
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

// withIgnoreCase sets whether .gitignore patterns ignore case for one test.
func withIgnoreCase(t *testing.T, on bool) {
	t.Helper()
	was := ignoreCase
	ignoreCase = on
	t.Cleanup(func() { ignoreCase = was })
}

// The .gitignore patterns people write, read the way git reads them: a bare
// name matches at any depth, a slash anchors it, a trailing slash means
// folders only, ** spans folders, the last match wins — a deeper file's over
// a shallower one's — and nothing inside an ignored folder comes back.
func TestGitignore(t *testing.T) {
	withIgnoreCase(t, false)
	fsys := fstest.MapFS{
		".gitignore": {Data: []byte("\ufeff/first.txt\n# what the build leaves\n*.log\n!keep.log\n/generated\ncoverage/\n" +
			"docs/**/draft-*.md\nsecret[0-9].txt\n*.tmp   \n\\#scratch.md\n\n" +
			"v[[:digit:]][[:digit:]].txt\n[[:alpha:]]_*.csv\nsp[[:space:]]ce.md\n[[:upper:]]x.md\n[!z-a]q.md\n")},
		"sub/.gitignore":       {Data: []byte("local.md\r\n!debug.log\r\n")},
		"generated/.gitignore": {Data: []byte("!api.go\n")},
	}
	want := map[string]string{
		"first.txt":            "first.txt", // after a byte-order mark
		"app.log":              "app.log",
		"ERROR.LOG":            "", // case counts, unless git is told otherwise
		"keep.log":             "",
		"sub/deep/trace.log":   "sub/deep/trace.log",
		"sub/debug.log":        "",
		"sub/local.md":         "sub/local.md",
		"local.md":             "",
		"generated/api.go":     "generated",
		"generated/v1/x.go":    "generated",
		"src/generated/api.go": "",
		"coverage/index.html":  "coverage",
		"src/coverage":         "", // a file, and the pattern is for folders
		"docs/draft-1.md":      "docs/draft-1.md",
		"docs/a/b/draft-2.md":  "docs/a/b/draft-2.md",
		"docs/final.md":        "",
		"secret1.txt":          "secret1.txt",
		"secretx.txt":          "",
		"notes.tmp":            "notes.tmp",
		"#scratch.md":          "#scratch.md",
		"v12.txt":              "v12.txt",
		"v1a.txt":              "",
		"q_2026.csv":           "q_2026.csv",
		"2_2026.csv":           "",
		"sp ce.md":             "sp ce.md",
		"spxce.md":             "",
		"Ax.md":                "Ax.md",
		"ax.md":                "",
		"bq.md":                "bq.md", // [z-a] holds z alone, so [!z-a] is all else
		"zq.md":                "",
		"README.md":            "",
	}
	for p := range want {
		fsys[p] = &fstest.MapFile{Data: []byte("x")}
	}
	got := ignoredIn(t, fsys)
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: ignored by %q, want %q", p, got[p], w)
		}
	}
}

// ignoredIn walks fsys as add does and says, for every file, what ignores
// it.
func ignoredIn(t *testing.T, fsys fs.FS) map[string]string {
	t.Helper()
	ig := newIgnores(fsys)
	got := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case d.IsDir():
			ig.dir(p)
		default:
			got[p] = ig.file(p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// On a Mac, git makes a repository ignore case, so "*.log" covers ERROR.LOG
// there.
func TestGitignoreIgnoresCaseOnAMac(t *testing.T) {
	if want := runtime.GOOS == "darwin"; ignoreCase != want {
		t.Errorf("ignoreCase is %v on %s, want %v", ignoreCase, runtime.GOOS, want)
	}
	withIgnoreCase(t, true)
	g := parseGitignore(".", []byte("*.log\nBuild/\n"))
	for _, name := range []string{"ERROR.LOG", "error.log", "Error.Log"} {
		if !g.rules[0].re.MatchString(name) {
			t.Errorf("*.log should match %s where case is ignored", name)
		}
	}
	if !g.rules[1].re.MatchString("build") {
		t.Error("Build/ should match build where case is ignored")
	}
}

// A pattern git can match nothing with — a bracket left open, a class with
// no such name, a backslash with nothing to escape — matches nothing here
// either, rather than its characters as themselves. The rest of the file
// still applies.
func TestGitignoreDropsWhatGitCannotMatch(t *testing.T) {
	withIgnoreCase(t, false)
	g := parseGitignore(".", []byte("[unclosed\n*.log\ntrail\\\n[[:nope:]]x\n[/]x\n[[:alpha:]\nkeep\\ \nspaced.md  \n"))
	var kept []string
	for _, r := range g.rules {
		kept = append(kept, r.re.String())
	}
	if len(g.rules) != 3 {
		t.Fatalf("kept %d rules, want *.log and the two spaced names: %q", len(g.rules), kept)
	}
	for i, name := range []string{"x.log", "keep ", "spaced.md"} {
		if !g.rules[i].re.MatchString(name) {
			t.Errorf("rule %d (%s) should match %q", i, g.rules[i].re, name)
		}
	}
	if g.rules[2].re.MatchString("spaced.md  ") {
		t.Error("trailing spaces are not part of a pattern unless escaped")
	}
}

// TestGitignoreAgreesWithGit lays out a repository, asks git which of its
// files are ignored, and checks the matcher names the same ones. git's own
// config and global excludes are kept out, since a walk reads neither.
func TestGitignoreAgreesWithGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	dir := writeTree(t, map[string]string{
		".gitignore": "\ufefffirst.txt\n# a comment\n*.log\n!keep.log\n/generated\ncoverage/\ndocs/**/draft-*.md\n" +
			"**/cache/*.bin\nlogs/**\nsecret[0-9].txt\n*.tmp   \nkeep\\ \n\\#scratch.md\n\\!bang.md\n" +
			"file[[:digit:]].txt\n[[:alpha:]]x.md\nsp[[:space:]]c.md\n[[:upper:]]w.md\n[[:punct:]]p.md\n" +
			"[!z-a]q.md\n[z-a]r.md\n[]]b.md\n[^a]n.md\n[a-c]g.md\n" +
			"[unclosed\ntrail\\\n[[:foo:]]bad.md\n[[:alpha:]\n",
		"sub/.gitignore":       "/local.md\r\n!debug.log\r\nnested/\r\n",
		"generated/.gitignore": "!api.go\n",
	})
	for _, name := range []string{
		"first.txt", "ERROR.LOG", "app.log", "keep.log", "sub/debug.log", "sub/x/trace.log",
		"sub/local.md", "sub/deep/local.md", "local.md", "sub/nested/n.md", "nested/n.md",
		"generated/api.go", "src/generated/api.go", "coverage/index.html", "coverage/lcov/report.txt",
		"docs/draft-1.md", "docs/a/b/draft-2.md", "docs/final.md", "a/cache/x.bin", "cache/y.bin", "a/cache/x.txt",
		"logs/today/x.txt", "secret1.txt", "secretx.txt", "notes.tmp", "keep ", "keep", "#scratch.md", "!bang.md",
		"file1.txt", "filex.txt", "ax.md", "1x.md", "sp c.md", "spxc.md", "Aw.md", "bw.md", "_p.md", "ap.md",
		"bq.md", "zq.md", "zr.md", "ar.md", "]b.md", "ab.md", "bn.md", "an.md", "Bg.md", "dg.md",
		"[unclosed", "trail", "trail\\", "bad.md", "fbad.md", "README.md",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	git := func(args ...string) (string, error) {
		cmd := exec.Command(gitPath, append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := cmd.Output()
		return string(out), err
	}
	if out, err := git("init", "-q", "--template="); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// git decides whether case matters when it makes the repository, by
	// looking at the filesystem; the matcher is held to the same answer.
	folds, _ := git("config", "--bool", "core.ignorecase")
	withIgnoreCase(t, strings.TrimSpace(folds) == "true")
	out, err := git("ls-files", "-o", "-i", "--exclude-standard", "-z")
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	byGit := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			byGit[p] = true
		}
	}
	if len(byGit) < 20 {
		t.Fatalf("git ignores only %d files, which is not the fixture: %v", len(byGit), sortedKeys(byGit))
	}
	for p, why := range ignoredIn(t, os.DirFS(dir)) {
		if (why != "") != byGit[p] {
			t.Errorf("%q: git ignores it: %v; tennis ignores it: %v (%q)", p, byGit[p], why != "", why)
		}
		delete(byGit, p)
	}
	for p := range byGit {
		t.Errorf("%q: git ignores a file the walk never saw", p)
	}
}

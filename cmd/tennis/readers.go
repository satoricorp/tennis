package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// What add reads. A file is text if its bytes are, whatever its extension;
// the formats that are not text on disk — a workbook, a Word file, a PDF —
// each have a reader that pulls the text out. The readers tennis can write
// with the standard library are built in. For the rest it asks the operating
// system, which on a Mac already knows how to read a PDF, a Pages document,
// or a Keynote deck, because Spotlight has to. What has no text to pull out
// — a photo, a song, a zip — is counted and passed over without comment.

// fileEntry is one file as the readers see it: enough to decide without
// reading it, and a way to read it when they do. path is set when the file
// is on disk, which is what an external tool needs; an entry inside a zip
// has none and is written out for the tool when one is called.
type fileEntry struct {
	name string
	path string
	size int64
	open func() (io.ReadCloser, error)
}

// maxDocumentSize caps a file that has a reader, the way maxSeedFileSize
// caps plain text. A workbook or PDF past this is a dataset or a scan, and
// whatever text comes out of one is still held to the plain-text cap, so no
// single file can swamp the index.
const maxDocumentSize = 100 << 20

// errNotText marks a file with no text to index — an image, a sound, an
// archive, a file with nothing in it. It is counted but not reported: a
// folder of photos is not a folder of mistakes.
var errNotText = errors.New("no text to index")

// unavailable says a reader exists for this format, but not on this machine.
type unavailable struct{ need string }

func (u unavailable) Error() string { return "no reader on this machine: " + u.need }

// readers, by extension. Anything not here is read as plain text if its
// bytes are text.
var readers = map[string]func(fileEntry) (string, error){
	".xlsx":  fromBody(xlsxText),
	".docx":  fromBody(docxText),
	".pptx":  fromBody(pptxText),
	".html":  fromBody(htmlText),
	".htm":   fromBody(htmlText),
	".xhtml": fromBody(htmlText),
	".epub":  fromBody(epubText),
	".pdf":   readPDF,

	".rtf":        readWithTextutil,
	".doc":        readWithTextutil,
	".odt":        readWithTextutil,
	".webarchive": readWithTextutil,
	".rtfd":       readWithTextutil, // a folder: see bundles

	".pages":   readWithSpotlight,
	".numbers": readWithSpotlight,
	".key":     readWithSpotlight,
	".xls":     readWithSpotlight,
	".ppt":     readWithSpotlight,
	".ods":     readWithSpotlight,
	".odp":     readWithSpotlight,
	".eml":     readWithSpotlight,
	".emlx":    readWithSpotlight,
}

// noText is what a folder holds besides documents. These are skipped
// quietly rather than opened and found binary, one warning at a time.
var noText = extSet(".png .jpg .jpeg .gif .heic .heif .webp .tiff .tif .bmp .ico .icns .svg .psd .ai .raw .cr2 .nef .dng " +
	".mp3 .m4a .wav .aac .flac .ogg .aiff .aif .wma " +
	".mp4 .mov .m4v .avi .mkv .webm .wmv .mpg .mpeg " +
	".zip .gz .tgz .tar .bz2 .xz .7z .rar .dmg .pkg .iso .jar .exe .dll .so .dylib .o .a " +
	".ttf .otf .woff .woff2 .sqlite .sqlite3 .db")

func extSet(list string) map[string]bool {
	out := map[string]bool{}
	for _, e := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }) {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out[e] = true
		}
	}
	return out
}

// hidden reports a dotfile or anything under a dot directory — .git,
// .DS_Store, .cache — which a person adding a folder does not mean.
func hidden(slashPath string) bool {
	for _, part := range strings.Split(slashPath, "/") {
		if len(part) > 1 && part[0] == '.' && part != ".." {
			return true
		}
	}
	return false
}

// passOver names the folders a code project fills with what nobody there
// wrote: fetched dependencies, build output, bytecode. One node_modules can
// outnumber the rest of a project a hundred to one, and every file in it
// would be read, indexed, and given a card — a summarizer call apiece when a
// key is set. A walk passes these over without comment, as it does a dot
// directory, but counts them, so the report says where the rest went. Like
// hidden, this applies below the folder named: `tennis add ./build` still
// reads ./build.
var passOver = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "dist": true,
	"build": true, "__pycache__": true, "site-packages": true,
}

// bundles are the folders a Mac shows as one thing, by extension: true for a
// document, false for anything else. A Pages, Numbers or Keynote file saved
// as a package, or rich text with its pictures (.rtfd), is one document to
// Finder and to Spotlight, and its parts — a document identifier, a build
// history, a preview — mean nothing alone. A walk lists the folder as one
// entry, read whole, by its path, with the reader its extension names. An
// app, a plug-in, a framework or a Photos library is no document at all,
// and is passed over and counted like node_modules. Inside a zip there is
// no path to hand a reader, so a document saved as a folder is passed over
// there too.
var bundles = map[string]bool{
	".pages": true, ".numbers": true, ".key": true, ".rtfd": true,
	".app": false, ".bundle": false, ".framework": false, ".photoslibrary": false,
}

// bundle reports whether a folder of this name is one thing to a Mac, and
// whether that thing is a document.
func bundle(name string) (isBundle, document bool) {
	document, isBundle = bundles[strings.ToLower(path.Ext(name))]
	return isBundle, document
}

// fileText is the one gate add and seed go through: the text that gets
// indexed for a file, or why there is none.
func fileText(f fileEntry) (string, error) {
	ext := strings.ToLower(filepath.Ext(f.name))
	if noText[ext] {
		return "", errNotText
	}
	read, ok := readers[ext]
	if !ok {
		return readPlain(f)
	}
	if f.size > maxDocumentSize {
		return "", fmt.Errorf("%.0fMB is over the %dMB cap", float64(f.size)/(1<<20), maxDocumentSize/(1<<20))
	}
	text, err := read(f)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", errors.New("no text in it")
	case len(text) > maxSeedFileSize:
		return "", fmt.Errorf("its text is more than the %dMB cap", maxSeedFileSize/(1<<20))
	}
	return text, nil
}

// readPlain is the default: the file's bytes, if they are text.
func readPlain(f fileEntry) (string, error) {
	if f.size > maxSeedFileSize {
		return "", fmt.Errorf("%.1fMB is over the %dMB cap", float64(f.size)/(1<<20), maxSeedFileSize/(1<<20))
	}
	body, err := readBody(f, maxSeedFileSize)
	if err != nil {
		return "", err
	}
	if isBinary(body) {
		return "", errors.New("binary content")
	}
	// An empty file, or one of blank lines, has nothing to find. Indexed, it
	// would be a document no search can reach and a card with nothing on
	// it — a summarizer call apiece when a key is set, for every
	// __init__.py and every Finder Icon\r in the folder.
	if strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff")) == "" {
		return "", errNotText
	}
	return string(body), nil
}

// readBody reads the whole file, refusing one that turns out longer than its
// size claimed. A zip header is a claim, not a measurement.
func readBody(f fileEntry, cap int64) ([]byte, error) {
	rc, err := f.open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, cap+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > cap {
		return nil, fmt.Errorf("larger than the %dMB cap, whatever its header says", cap/(1<<20))
	}
	return body, nil
}

func fromBody(read func([]byte) (string, error)) func(fileEntry) (string, error) {
	return func(f fileEntry) (string, error) {
		body, err := readBody(f, maxDocumentSize)
		if err != nil {
			return "", err
		}
		return read(body)
	}
}

// tidy settles extracted text into lines: runs of spaces collapsed, runs of
// blank lines reduced to one, nothing else touched.
func tidy(s string) string {
	var out []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// --- HTML -------------------------------------------------------------------

var (
	htmlDrop  = regexp.MustCompile(`(?is)<(script|style|noscript|template)\b.*?</(script|style|noscript|template)\s*>|<!--.*?-->`)
	htmlBlock = regexp.MustCompile(`(?i)</?(p|div|br|hr|li|ul|ol|h[1-6]|tr|td|th|table|section|article|header|footer|blockquote|pre|dd|dt|dl|figure|figcaption|nav|aside|main|form|fieldset|address|details|summary|title)\b[^>]*>`)
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
)

// htmlText is the page's words without its markup: scripts, styles, and
// comments dropped, block elements turned into line breaks, entities decoded.
// The title stays; it is often the best line on the page.
func htmlText(body []byte) (string, error) {
	if isBinary(body) {
		return "", errors.New("binary content")
	}
	s := htmlDrop.ReplaceAllString(string(body), " ")
	s = htmlBlock.ReplaceAllString(s, "\n")
	s = htmlTag.ReplaceAllString(s, " ")
	return tidy(html.UnescapeString(s)), nil
}

// --- EPUB -------------------------------------------------------------------

// epubHead is a page's head. Its title is for a browser's tab bar; a reading
// system never shows it, and in most books it repeats the chapter heading,
// or the book's own title, on every page.
var epubHead = regexp.MustCompile(`(?is)<head\b.*?</head\s*>`)

// epubText reads a book as its pages in reading order. An EPUB is a zip of
// XHTML: META-INF/container.xml names the package file, whose manifest says
// where each page is and whose spine says what order they are read in. Each
// page is read as any other page is, less its head. What the spine leaves
// out — stylesheets, images, the old NCX table of contents — is left out,
// and so is the newer table of contents, the navigation document, which
// many books put in the spine: its lines are the chapter headings over again.
func epubText(body []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("not a zip: %w", err)
	}
	parts := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		parts[f.Name] = f
	}
	open := func(name string) (io.ReadCloser, error) {
		f := parts[name]
		if f == nil {
			return nil, fmt.Errorf("no %s in the book", name)
		}
		return f.Open()
	}

	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := decodePart(open, "META-INF/container.xml", &container); err != nil {
		return "", err
	}
	if len(container.Rootfiles) == 0 || container.Rootfiles[0].Path == "" {
		return "", errors.New("META-INF/container.xml names no package file")
	}
	opf := container.Rootfiles[0].Path
	var pkg struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := decodePart(open, opf, &pkg); err != nil {
		return "", err
	}
	// An href is a URL relative to the package file's own folder.
	hrefs := make(map[string]string, len(pkg.Items))
	nav := map[string]bool{}
	for _, it := range pkg.Items {
		href, _, _ := strings.Cut(it.Href, "#")
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		hrefs[it.ID] = path.Join(path.Dir(opf), href)
		nav[it.ID] = slices.Contains(strings.Fields(it.Properties), "nav")
	}

	// Pages compress well, so the cap on the zip is no cap on what they
	// expand to; the pages together are held to the same cap the file was.
	var pages []string
	left := int64(maxDocumentSize)
	for _, ref := range pkg.Spine {
		name := hrefs[ref.IDRef]
		switch {
		case name == "":
			return "", fmt.Errorf("%s: the spine names %q, which the manifest does not list", opf, ref.IDRef)
		case nav[ref.IDRef]:
			continue
		}
		rc, err := open(name)
		if err != nil {
			return "", err
		}
		page, err := io.ReadAll(io.LimitReader(rc, left+1))
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if left -= int64(len(page)); left < 0 {
			return "", fmt.Errorf("its pages come to more than the %dMB cap", maxDocumentSize/(1<<20))
		}
		text, err := htmlText(epubHead.ReplaceAll(page, nil))
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if text != "" {
			pages = append(pages, text)
		}
	}
	return strings.Join(pages, "\n\n"), nil
}

// --- external readers --------------------------------------------------------

// toolTimeout bounds one conversion. A file that takes longer than this is a
// converter that has hung, and the import should move on without it.
const toolTimeout = 2 * time.Minute

var toolPaths sync.Map // tool name → resolved path, or "" when absent

// tool finds a converter on the path once, since the answer does not change
// during an import and a folder can hold thousands of files.
func tool(name string) string {
	if p, ok := toolPaths.Load(name); ok {
		return p.(string)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		p = ""
	}
	toolPaths.Store(name, p)
	return p
}

// run executes a converter on the file and returns what it printed, stdout
// and stderr apart. An entry inside a zip is written out first, since a
// converter wants a path.
func run(f fileEntry, args func(path string) []string) (stdout, stderr string, err error) {
	p := f.path
	if p == "" {
		tmp, err := os.CreateTemp("", "tennis-*"+filepath.Ext(f.name))
		if err != nil {
			return "", "", err
		}
		defer os.Remove(tmp.Name())
		rc, err := f.open()
		if err != nil {
			tmp.Close()
			return "", "", err
		}
		_, err = io.Copy(tmp, io.LimitReader(rc, maxDocumentSize+1))
		rc.Close()
		tmp.Close()
		if err != nil {
			return "", "", err
		}
		p = tmp.Name()
	}

	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	argv := args(p)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errs.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", "", fmt.Errorf("%s: %s", filepath.Base(argv[0]), firstLine(msg, 120))
	}
	return out.String(), errs.String(), nil
}

// readPDF prefers pdftotext, which is on most machines that have ever
// installed poppler and reads a PDF in reading order; a Mac without it has
// Spotlight's PDF importer. Anything else has to say what is missing.
func readPDF(f fileEntry) (string, error) {
	switch {
	case tool("pdftotext") != "":
		out, _, err := run(f, func(p string) []string { return []string{"pdftotext", "-enc", "UTF-8", p, "-"} })
		if err != nil {
			return "", err
		}
		return tidy(out), nil
	case runtime.GOOS == "darwin":
		return readWithSpotlight(f)
	}
	return "", unavailable{"install poppler-utils for pdftotext"}
}

// readWithTextutil covers the formats macOS's own converter reads: RTF, the
// old Word format, OpenDocument text, and Safari web archives.
func readWithTextutil(f fileEntry) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", unavailable{"this format is read with macOS textutil"}
	}
	out, _, err := run(f, func(p string) []string { return []string{"textutil", "-convert", "txt", "-stdout", p} })
	if err != nil {
		return "", err
	}
	return tidy(out), nil
}

// readWithSpotlight runs the Spotlight importer for the file's type in test
// mode, which prints the attributes it would have indexed, text included.
// It is the slowest reader here, a few hundred milliseconds a file, and the
// only one that reads Pages, Numbers, and Keynote.
func readWithSpotlight(f fileEntry) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", unavailable{"this format is read with macOS Spotlight"}
	}
	out, errs, err := run(f, func(p string) []string { return []string{"mdimport", "-t", "-d3", p} })
	if err != nil {
		return "", err
	}
	return tidy(spotlightText(out + "\n" + errs)), nil
}

// spotlightText picks the text attribute out of what `mdimport -t -d3`
// prints: an old-style property list, one attribute per line, strings in
// double quotes with C escapes and \Uxxxx for anything outside ASCII.
func spotlightText(dump string) string {
	for _, line := range strings.Split(dump, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "kMDItemTextContent = ")
		if !ok {
			continue
		}
		rest = strings.TrimSuffix(strings.TrimSpace(rest), ";")
		if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
			return ""
		}
		if v := rest[1 : len(rest)-1]; v != "<null>" {
			return plistUnescape(v)
		}
		return ""
	}
	return ""
}

// plistUnescape undoes the escaping of an old-style plist string: the C
// escapes, octal bytes, and \U followed by four hex digits — two of them in
// a row for a character outside the basic plane.
func plistUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := s[i]; {
		case e == 'n':
			b.WriteByte('\n')
		case e == 't':
			b.WriteByte('\t')
		case e == 'r':
			b.WriteByte('\r')
		case e == 'a', e == 'b', e == 'f', e == 'v':
			b.WriteByte(' ')
		case e == 'U' && i+4 < len(s):
			r, err := strconv.ParseUint(s[i+1:i+5], 16, 32)
			if err != nil {
				b.WriteByte('U')
				continue
			}
			i += 4
			if utf16.IsSurrogate(rune(r)) && i+6 < len(s) && s[i+1] == '\\' && s[i+2] == 'U' {
				if r2, err := strconv.ParseUint(s[i+3:i+7], 16, 32); err == nil {
					r = uint64(utf16.DecodeRune(rune(r), rune(r2)))
					i += 6
				}
			}
			b.WriteRune(rune(r))
		case e >= '0' && e <= '7':
			n, j := 0, i
			for ; j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7'; j++ {
				n = n*8 + int(s[j]-'0')
			}
			b.WriteByte(byte(n))
			i = j - 1
		default:
			b.WriteByte(e)
		}
	}
	return b.String()
}

// --- .gitignore --------------------------------------------------------------

// A folder that is a git repository has already said what in it is not
// worth keeping. A walk honours the .gitignore files it finds inside the
// folder it was given — not those above it, nor git's global excludes — with
// git's rules for the patterns people write: comments, "!" to re-include, a
// trailing slash for folders only, a leading or inner slash to anchor a
// pattern to its file's folder, and *, ?, [a-z], [[:digit:]] and ** as globs.

// gitignore is one .gitignore file: its patterns, in order, and the folder
// they are relative to.
type gitignore struct {
	dir   string // slash path from the root of the walk, "." for the root
	rules []ignoreRule
}

type ignoreRule struct {
	re      *regexp.Regexp
	base    bool // no slash in the pattern: match the last name, at any depth
	negate  bool // "!": a match brings the path back
	dirOnly bool // a trailing "/": folders only
}

// ignoreCase is whether a pattern matches a name whatever its case. git
// keeps this per repository, as core.ignorecase, and turns it on when it
// makes a repository on a filesystem that cannot tell README from readme —
// which a Mac's is, unless someone chose otherwise when formatting it. In
// such a repository "*.log" ignores ERROR.LOG, and a walk that read
// ERROR.LOG anyway would index what git keeps out. A folder need not be a
// repository for its .gitignore to be read, so there is no config to ask;
// the operating system stands in for the filesystem.
var ignoreCase = runtime.GOOS == "darwin"

func parseGitignore(dir string, body []byte) gitignore {
	g := gitignore{dir: dir}
	// git skips the byte-order mark some editors begin a file with; read as
	// part of the first pattern, it would keep that pattern from matching.
	body = bytes.TrimPrefix(body, []byte("\ufeff"))
	flags := ""
	if ignoreCase {
		flags = "(?i)"
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = trimTrailingSpaces(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' {
			continue
		}
		var r ignoreRule
		if line[0] == '!' {
			r.negate, line = true, line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		r.base = !strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		glob, ok := globRegexp(line)
		if !ok {
			continue // git matches nothing with it, and neither does this
		}
		re, err := regexp.Compile(flags + "^" + glob + "$")
		if err != nil {
			// A pattern this reader cannot follow is one git may well
			// honour; leaving it out reads a file git would not, which is
			// the side to err on.
			continue
		}
		r.re = re
		g.rules = append(g.rules, r)
	}
	return g
}

// trimTrailingSpaces drops the spaces a pattern ends with, as git does,
// unless a backslash keeps one: "a\ " ends in a space, "a\\ " does not.
func trimTrailingSpaces(s string) string {
	from := -1 // where the run of spaces at the end begins
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			if from < 0 {
				from = i
			}
		case '\\':
			if i++; i == len(s) {
				return s
			}
			from = -1
		default:
			from = -1
		}
	}
	if from >= 0 {
		return s[:from]
	}
	return s
}

// globRegexp turns a gitignore glob into a regular expression. * and ? stay
// within one name; ** as a whole segment spans any number of folders. ok is
// false for a glob git can match nothing with — a bracket never closed, a
// class with no such name, a backslash with nothing after it to escape —
// rather than reading the stray character as itself.
func globRegexp(glob string) (re string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			segment := strings.HasPrefix(glob[i:], "**") &&
				(i == 0 || glob[i-1] == '/') && (i+2 == len(glob) || glob[i+2] == '/')
			switch {
			case segment && i+2 == len(glob):
				b.WriteString(".*") // "logs/**": everything inside
				i++
			case segment:
				b.WriteString("(?:.*/)?") // "**/": any folders, or none
				i += 2
			default:
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			class, n := globClass(glob[i:])
			if n == 0 {
				return "", false
			}
			b.WriteString(class)
			i += n - 1
		case '\\':
			if i+1 == len(glob) {
				return "", false
			}
			i++
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		default:
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		}
	}
	return b.String(), true
}

// posixClasses are the classes git knows by name in a bracket — [[:digit:]],
// [[:space:]] — as Go spells them. A bracket never matches the "/" between
// folders, so the three that take it in are spelled out without it.
var posixClasses = map[string]string{
	"alnum": `[:alnum:]`, "alpha": `[:alpha:]`, "blank": `[:blank:]`,
	"cntrl": `[:cntrl:]`, "digit": `[:digit:]`, "lower": `[:lower:]`,
	"space": `[:space:]`, "upper": `[:upper:]`, "xdigit": `[:xdigit:]`,
	"punct": `\x{21}-\x{2e}\x{3a}-\x{40}\x{5b}-\x{60}\x{7b}-\x{7e}`,
	"graph": `\x{21}-\x{2e}\x{30}-\x{7e}`,
	"print": `\x{20}-\x{2e}\x{30}-\x{7e}`,
}

// globClass translates the bracket expression s starts with — [abc], [a-z],
// [!a-z], [[:alpha:]_] — the way git's wildmatch reads one, and says how
// many bytes of s it took. 0 means the glob it is in can match nothing: the
// bracket is never closed, names a class git has no name for, or holds
// nothing but "/". As in git, a "]" first in the bracket is itself, a range
// written backwards holds only its first character, and "[:" without a
// closing ":]" is an ordinary "[".
func globClass(s string) (string, int) {
	var b strings.Builder
	b.WriteByte('[')
	i := 1
	negated := i < len(s) && (s[i] == '!' || s[i] == '^')
	if negated {
		b.WriteString("^/")
		i++
	}
	members := 0
	add := func(lo, hi rune) {
		// Every character is written as a code point, so none is special.
		for _, r := range [][2]rune{{lo, min(hi, '/'-1)}, {max(lo, '/'+1), hi}} {
			if r[0] > r[1] {
				continue
			}
			if r[0] == r[1] {
				fmt.Fprintf(&b, `\x{%x}`, r[0])
			} else {
				fmt.Fprintf(&b, `\x{%x}-\x{%x}`, r[0], r[1])
			}
			members++
		}
	}
	// next reads the character at s[i], or the one a backslash escapes.
	next := func() (rune, bool) {
		if s[i] == '\\' {
			if i++; i == len(s) {
				return 0, false
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		return r, true
	}

	prev := rune(-1) // the last lone character, where a "-" starts a range
	for first := true; i < len(s); first = false {
		switch {
		case s[i] == ']' && !first:
			if members == 0 && !negated {
				return "", 0
			}
			b.WriteByte(']')
			return b.String(), i + 1
		case s[i] == '-' && prev >= 0 && i+1 < len(s) && s[i+1] != ']':
			i++
			hi, ok := next()
			if !ok {
				return "", 0
			}
			add(prev, hi)
			prev = -1
		case strings.HasPrefix(s[i:], "[:"):
			end := strings.IndexByte(s[i+2:], ']')
			if end < 0 {
				return "", 0
			}
			if name, isClass := strings.CutSuffix(s[i+2:i+2+end], ":"); end > 0 && isClass {
				class, known := posixClasses[name]
				if !known {
					return "", 0
				}
				b.WriteString(class)
				members++
				prev = -1
				i += 2 + end + 1
				continue
			}
			add('[', '[')
			prev = '['
			i++
		default:
			r, ok := next()
			if !ok {
				return "", 0
			}
			add(r, r)
			prev = r
		}
	}
	return "", 0
}

// ignores applies the .gitignore files one walk finds, the way git does:
// each folder's rules on top of its parent's, the last match winning, and
// nothing inside an ignored folder brought back, since git never looks in
// one. It relies on the walk reaching a folder before what is in it, which
// fs.WalkDir and filepath.WalkDir both do.
type ignores struct {
	fsys    fs.FS
	inForce map[string][]gitignore // by folder: the files that apply in it, outermost first
	under   map[string]string      // ignored folder → the outermost ignored folder it is in, itself if none
}

func newIgnores(fsys fs.FS) *ignores {
	return &ignores{fsys: fsys, inForce: map[string][]gitignore{}, under: map[string]string{}}
}

// dir is called as the walk enters a folder, by its slash path from the
// root. It returns the ignored folder this one is, or is inside, or "" —
// having read this folder's own .gitignore for what is in it.
func (g *ignores) dir(p string) string {
	if p != "." {
		if why := g.check(p, true); why != "" {
			g.under[p] = why
			return why
		}
	}
	rules := g.inForce[path.Dir(p)]
	if body, err := fs.ReadFile(g.fsys, path.Join(p, ".gitignore")); err == nil {
		rules = append(rules[:len(rules):len(rules)], parseGitignore(p, body))
	}
	g.inForce[p] = rules
	return ""
}

// file returns what makes a file ignored — the file itself, or the outermost
// ignored folder it is in — or "" when nothing does.
func (g *ignores) file(p string) string { return g.check(p, false) }

func (g *ignores) check(p string, isDir bool) string {
	parent := path.Dir(p)
	if why, ok := g.under[parent]; ok {
		return why
	}
	ignored := false
	for _, gi := range g.inForce[parent] {
		rel := p
		if gi.dir != "." {
			rel = strings.TrimPrefix(p, gi.dir+"/")
		}
		for _, r := range gi.rules {
			subject := rel
			if r.base {
				subject = path.Base(rel)
			}
			if (isDir || !r.dirOnly) && r.re.MatchString(subject) {
				ignored = !r.negate
			}
		}
	}
	if ignored {
		return p
	}
	return ""
}

package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Word and PowerPoint files are, like a workbook, zips of XML, and their
// text sits in the same shape: runs of <t> inside <p>. One walker reads both.

// docxText renders a Word document as its paragraphs, one per line. Tabs
// and line breaks inside a paragraph are kept; tracked deletions, field
// codes, headers, footers, and comments are not — they are not what the
// document says.
func docxText(body []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("not a zip: %w", err)
	}
	f := zipPart(zr, "word/document.xml")
	if f == nil {
		return "", errors.New("no word/document.xml in the document")
	}
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return paragraphs(rc, map[string]string{"tab": "\t", "br": "\n", "cr": "\n"})
}

// pptxText renders a deck slide by slide, under a heading per slide, each
// text frame's paragraphs one per line. Slides come in file order, which is
// the order they were made in and almost always the order they are shown.
func pptxText(body []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("not a zip: %w", err)
	}
	var slides []*zip.File
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slides = append(slides, f)
		}
	}
	if len(slides) == 0 {
		return "", errors.New("no slides in the deck")
	}
	sort.Slice(slides, func(i, j int) bool { return slideNumber(slides[i].Name) < slideNumber(slides[j].Name) })

	var b strings.Builder
	for _, f := range slides {
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		text, err := paragraphs(rc, map[string]string{"br": "\n"})
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("%s: %w", f.Name, err)
		}
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "## Slide %d\n\n%s\n\n", slideNumber(f.Name), text)
	}
	return strings.TrimSpace(b.String()), nil
}

func slideNumber(name string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml"))
	return n
}

func zipPart(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// paragraphs walks WordprocessingML or DrawingML and returns one line per
// paragraph: the text of every <t> inside a <p>, with the elements in breaks
// rendered as the whitespace they stand for. Only text inside a paragraph
// counts, which is what leaves field codes and deleted runs out.
func paragraphs(r io.Reader, breaks map[string]string) (string, error) {
	dec := xml.NewDecoder(r)
	var (
		lines  []string
		cur    strings.Builder
		depth  int // a text box nests paragraphs inside a paragraph
		inText bool
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "p":
				depth++
			case t.Name.Local == "t":
				inText = depth > 0
			case breaks[t.Name.Local] != "" && depth > 0:
				cur.WriteString(breaks[t.Name.Local])
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				if depth--; depth == 0 {
					lines = append(lines, cur.String())
					cur.Reset()
				}
			case "t":
				inText = false
			}
		case xml.CharData:
			if inText {
				cur.Write(t)
			}
		}
	}
	return tidy(strings.Join(lines, "\n")), nil
}

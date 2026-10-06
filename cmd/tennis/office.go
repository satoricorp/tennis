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
	rc, err := newUnpacking().open(f)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	text, err := paragraphs(rc, map[string]string{"tab": "\t", "br": "\n", "cr": "\n"}, maxSeedFileSize)
	if err != nil {
		return "", err
	}
	return tidy(text), nil
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

	// The slides together are held to the caps one document is.
	unpacked := newUnpacking()
	var b strings.Builder
	for _, f := range slides {
		rc, err := unpacked.open(f)
		if err != nil {
			return "", err
		}
		text, err := paragraphs(rc, map[string]string{"br": "\n"}, maxSeedFileSize-b.Len())
		rc.Close()
		switch {
		case overCap(err):
			return "", err
		case err != nil:
			return "", fmt.Errorf("%s: %w", f.Name, err)
		}
		if text = tidy(text); text == "" {
			continue
		}
		b.WriteString("## Slide " + strconv.Itoa(slideNumber(f.Name)) + "\n\n")
		b.WriteString(text)
		b.WriteString("\n\n")
		if b.Len() > maxSeedFileSize {
			return "", errTextCap
		}
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
// paragraph, untidied: the text of every <t> inside a <p>, with the elements
// in breaks rendered as the whitespace they stand for. Only text inside a
// paragraph counts, which is what leaves field codes and deleted runs out.
// The text is held to room bytes, past which it is errTextCap.
//
// A text box is paragraphs inside a paragraph, anchored somewhere in its
// runs. Its lines follow the line of the paragraph it sits in rather than
// break into the middle of it. Word writes a text box twice, as a modern
// shape and as an old one for readers that do not know the new, as the
// branches of an mc:AlternateContent; only the first branch is read.
func paragraphs(r io.Reader, breaks map[string]string, room int) (string, error) {
	type para struct {
		text   strings.Builder // its own text, when it is inside another
		inside strings.Builder // the lines of the paragraphs inside it
	}
	dec := xml.NewDecoder(r)
	var (
		out    strings.Builder
		paras  []*para // by depth, kept for the next paragraph that deep
		depth  int
		used   int
		chosen []bool // by open mc:AlternateContent: whether a branch was read
		inText bool
	)
	// line is where the innermost open paragraph's own text goes.
	line := func() *strings.Builder {
		if depth == 1 {
			return &out
		}
		return &paras[depth-1].text
	}
	spend := func(n int) error {
		if used += n; used > room {
			return errTextCap
		}
		return nil
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch name := t.Name.Local; {
			case name == "AlternateContent":
				chosen = append(chosen, false)
			case (name == "Choice" || name == "Fallback") && len(chosen) > 0:
				if chosen[len(chosen)-1] {
					err = dec.Skip()
				}
				chosen[len(chosen)-1] = true
			case name == "p":
				if depth++; len(paras) < depth {
					paras = append(paras, &para{})
				}
				p := paras[depth-1]
				p.text.Reset()
				p.inside.Reset()
			case name == "t":
				inText = depth > 0
			case breaks[name] != "" && depth > 0:
				if err = spend(len(breaks[name])); err == nil {
					line().WriteString(breaks[name])
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "AlternateContent":
				if len(chosen) > 0 {
					chosen = chosen[:len(chosen)-1]
				}
			case "p":
				if depth == 0 {
					continue
				}
				if err = spend(1); err != nil {
					break
				}
				p := paras[depth-1]
				to := &out
				if depth > 1 {
					to = &paras[depth-2].inside
					to.WriteString(p.text.String())
				}
				to.WriteByte('\n')
				to.WriteString(p.inside.String())
				depth--
			case "t":
				inText = false
			}
		case xml.CharData:
			if inText {
				if err = spend(len(t)); err == nil {
					line().Write(t)
				}
			}
		}
		if err != nil {
			return "", err
		}
	}
}

package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// An .xlsx workbook is a zip of XML, which makes it the one spreadsheet format
// readable with the standard library alone: a sheet is rows of cells, a cell
// is a value or an index into a shared string table, and that is all a search
// index needs. A spreadsheet library would be a larger dependency than the
// rest of this program, for a format whose text takes a page to pull out.
//
// What is kept: every sheet in workbook order under a heading carrying its
// name, one line per row, cells separated by tabs so a row reads as a row,
// and dates and times as dates and times — a date is stored as a day count
// and a time as a fraction of a day, and which cells are which is known only
// to the cell styles, so those are read for that one fact. What is dropped:
// everything else about formatting, and formulas, whose cached results are
// kept in their place.

// maxSheetCells bounds one workbook, the way maxSeedFileSize bounds a file.
// Sheet XML compresses well, so the cap on the zip is no cap on what it
// expands to; a workbook past this is a dataset, not a document.
const maxSheetCells = 1 << 20

// xlsxText renders a workbook as text, or says why it could not.
func xlsxText(body []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("not a zip: %w", err)
	}
	parts := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		parts[f.Name] = f
	}
	unpacked := newUnpacking()
	open := func(name string) (io.ReadCloser, error) {
		f := parts[name]
		if f == nil {
			return nil, fmt.Errorf("no %s in the workbook", name)
		}
		return unpacked.open(f)
	}

	// Sheet names and order come from the workbook; where each sheet lives
	// comes from its relationships part, the one place the two are tied.
	var book struct {
		Pr struct {
			Date1904 string `xml:"date1904,attr"`
		} `xml:"workbookPr"`
		Sheets []struct {
			Name string `xml:"name,attr"`
			// A workbook saved as Strict Open XML names its relationships
			// in the ISO standard's namespace rather than the transitional
			// one; it is otherwise the same where it matters here.
			RID       string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			StrictRID string `xml:"http://purl.oclc.org/ooxml/officeDocument/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := decodePart(open, "xl/workbook.xml", &book); err != nil {
		return "", err
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := decodePart(open, "xl/_rels/workbook.xml.rels", &rels); err != nil {
		return "", err
	}
	// A target is relative to the workbook's own folder unless it starts at
	// the root of the package.
	resolve := func(target string) string {
		if strings.HasPrefix(target, "/") {
			return strings.TrimPrefix(target, "/")
		}
		return path.Join("xl", target)
	}
	targets := make(map[string]string, len(rels.Rels))
	shared := "xl/sharedStrings.xml"
	for _, r := range rels.Rels {
		targets[r.ID] = resolve(r.Target)
		if strings.HasSuffix(r.Type, "/sharedStrings") {
			shared = resolve(r.Target)
		}
	}

	wb := &workbook{epoch1904: book.Pr.Date1904 == "1" || strings.EqualFold(book.Pr.Date1904, "true")}
	if parts[shared] != nil {
		rc, err := open(shared)
		if err != nil {
			return "", err
		}
		wb.strs, err = readSharedStrings(rc)
		rc.Close()
		switch {
		case overCap(err):
			return "", err
		case err != nil:
			return "", fmt.Errorf("%s: %w", shared, err)
		}
	}
	if wb.styles, err = numberStyles(open); err != nil {
		return "", err
	}

	for _, s := range book.Sheets {
		target := targets[cmp.Or(s.RID, s.StrictRID)]
		if target == "" {
			return "", fmt.Errorf("sheet %q has no part", s.Name)
		}
		rc, err := open(target)
		if err != nil {
			return "", err
		}
		err = wb.readSheet(rc, s.Name)
		rc.Close()
		switch {
		case overCap(err):
			return "", err
		case err != nil:
			return "", fmt.Errorf("sheet %q: %w", s.Name, err)
		}
	}
	return strings.TrimSpace(wb.out.String()), nil
}

func decodePart(open func(string) (io.ReadCloser, error), name string, v any) error {
	rc, err := open(name)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := xml.NewDecoder(rc).Decode(v); err != nil {
		if overCap(err) {
			return err
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// workbook is what reading a workbook's sheets needs from the rest of it,
// and the text they come to. The text is held to the text cap as it is
// written and the cells to maxSheetCells, across all the sheets.
type workbook struct {
	strs      []string
	styles    map[int]numKind
	epoch1904 bool
	cells     int
	out       strings.Builder
}

// readSharedStrings reads the table every repeated cell text points into. A
// string can be split into formatted runs, which are joined back together;
// phonetic guides for East Asian text are not part of the string and are
// skipped. Each string is made one line here, once, rather than at every
// cell that holds it. A table is written with the strings its cells hold,
// so one whose strings come to more than the text cap is a workbook whose
// text does.
func readSharedStrings(r io.Reader) ([]string, error) {
	dec := xml.NewDecoder(r)
	var (
		out      []string
		size     int
		cur      strings.Builder
		inString bool
		inText   bool
		phonetic int
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inString = true
				cur.Reset()
			case "rPh":
				phonetic++
			case "t":
				inText = inString && phonetic == 0
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				inString = false
				out = append(out, oneField(cur.String()))
				if len(out) > maxSheetCells {
					return nil, fmt.Errorf("more than %d strings", maxSheetCells)
				}
			case "rPh":
				phonetic--
			case "t":
				inText = false
			}
		case xml.CharData:
			if inText {
				if size += len(t); size > maxSeedFileSize {
					return nil, errTextCap
				}
				cur.Write(t)
			}
		}
	}
}

// readSheet writes one worksheet's rows onto the workbook's text, under a
// heading with its name, or nothing when it has no rows. Cells carry their
// column in the "r" attribute and empty cells are simply absent, so a value
// is placed by column rather than appended — a gap would otherwise shift
// everything after it one column left.
//
// Only the cells with something in them are kept. The tabs that put each in
// its column are written with the row and counted against the cap like the
// rest of its text, so a cell in column XFD costs a line of sixteen thousand
// tabs, not a row of sixteen thousand cells.
func (wb *workbook) readSheet(r io.Reader, name string) error {
	type cell struct {
		col int
		v   string
	}
	heading := "## " + name + "\n\n"
	dec := xml.NewDecoder(r)
	var (
		row     []cell
		last    = -1 // the column of the row's last cell, empty or not
		width   int  // the row's length as text, so far
		ref     string
		typ     string
		style   int
		val     strings.Builder
		inCell  bool
		inValue bool
		headed  bool
	)
	// fits says whether the text so far, the row so far, and n bytes more
	// are within the cap.
	fits := func(n int) bool {
		n += wb.out.Len() + width + 1
		if !headed {
			n += len(heading)
		}
		return n <= maxSeedFileSize
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			if headed {
				wb.out.WriteByte('\n')
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row, last, width = row[:0], -1, 0
			case "c":
				inCell, ref, typ, style = true, "", "", -1
				val.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						ref = a.Value
					case "t":
						typ = a.Value
					case "s":
						style, _ = strconv.Atoi(a.Value)
					}
				}
			case "v", "t":
				// A cached formula result or a number lives in v; an inline
				// string's runs live in t. The formula itself lives in f and
				// is not text anyone searches for.
				inValue = inCell
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				if !inCell {
					continue
				}
				inCell = false
				if wb.cells++; wb.cells > maxSheetCells {
					return fmt.Errorf("more than %d cells", maxSheetCells)
				}
				col := max(columnIndex(ref), last+1)
				last = col
				v := cellValue(typ, val.String(), wb.strs)
				if typ == "" || typ == "n" {
					v = wb.styles[style].render(v, wb.epoch1904)
				}
				if v == "" {
					continue
				}
				// The row as text grows by this value and a tab for each
				// column between it and the one before.
				prev := 0
				if len(row) > 0 {
					prev = row[len(row)-1].col
				}
				grown := col - prev + len(v)
				if !fits(grown) {
					return errTextCap
				}
				width += grown
				row = append(row, cell{col, v})
			case "row":
				if !slices.ContainsFunc(row, func(c cell) bool { return strings.TrimSpace(c.v) != "" }) {
					continue
				}
				if !headed {
					wb.out.WriteString(heading)
					headed = true
				}
				at := 0
				for _, c := range row {
					for ; at < c.col; at++ {
						wb.out.WriteByte('\t')
					}
					wb.out.WriteString(c.v)
				}
				wb.out.WriteByte('\n')
			}
		case xml.CharData:
			if inValue {
				if !fits(val.Len() + len(t)) {
					return errTextCap
				}
				val.Write(t)
			}
		}
	}
}

// cellValue resolves what a cell holds from its type: an index into the
// shared strings, a boolean, or the value as written — numbers, inline and
// formula strings, and errors all read fine as they are.
func cellValue(typ, raw string, strs []string) string {
	switch typ {
	case "s":
		i, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || i < 0 || i >= len(strs) {
			return ""
		}
		return strs[i]
	case "b":
		if strings.TrimSpace(raw) == "1" {
			return "TRUE"
		}
		return "FALSE"
	}
	return oneField(raw)
}

// oneField makes a cell's text one field on one line; a newline or tab
// inside it would read as a row or column break.
func oneField(v string) string {
	if !strings.ContainsAny(v, "\n\r\t") {
		return v
	}
	return strings.Join(strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' }), " ")
}

// maxColumns is how many columns a sheet has, A to XFD.
const maxColumns = 16384

// columnIndex turns the letters of a cell reference ("C7" → 2) into a
// zero-based column, or -1 when there are none. A reference past XFD, which
// no spreadsheet writes, is read as XFD.
func columnIndex(ref string) int {
	n := 0
	for i := 0; i < len(ref); i++ {
		ch := ref[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			n = min(n*26+int(ch-'A')+1, maxColumns)
		case ch >= 'a' && ch <= 'z':
			n = min(n*26+int(ch-'a')+1, maxColumns)
		default:
			return n - 1
		}
	}
	return n - 1
}

// numKind is what a cell's style makes of the number in it.
type numKind uint8

const (
	plainNumber numKind = iota
	dateNumber          // a day count: a date, with the time of day if it has one
	timeNumber          // the time of day alone, whatever day it is on
	hoursNumber         // a span of time, in hours: [h]:mm
)

func (k numKind) render(raw string, epoch1904 bool) string {
	switch k {
	case dateNumber:
		return excelDate(raw, epoch1904)
	case timeNumber:
		return excelTime(raw, false)
	case hoursNumber:
		return excelTime(raw, true)
	}
	return raw
}

// numberStyles reads the one fact styles.xml holds that is text: which cell
// styles format a number as a date or a time. A workbook with no styles
// part, or one that does not parse, still reads; its dates stay day counts.
func numberStyles(open func(string) (io.ReadCloser, error)) (map[int]numKind, error) {
	var st struct {
		NumFmts []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Xfs []struct {
			NumFmtID int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if err := decodePart(open, "xl/styles.xml", &st); err != nil {
		if overCap(err) {
			return nil, err
		}
		return nil, nil
	}
	custom := make(map[int]numKind, len(st.NumFmts))
	for _, f := range st.NumFmts {
		custom[f.ID] = formatKind(f.Code)
	}
	out := map[int]numKind{}
	for i, xf := range st.Xfs {
		k, ok := custom[xf.NumFmtID]
		if !ok {
			k = builtinKind(xf.NumFmtID)
		}
		if k != plainNumber {
			out[i] = k
		}
	}
	return out, nil
}

// builtinKind is what Excel's built-in number formats, which a workbook
// uses without declaring them, make of a number: 18 to 21 are h:mm and
// h:mm:ss with and without AM/PM, 32 and 33 the same in East Asian
// locales, 45 to 47 mm:ss, [h]:mm:ss and mmss.0; the rest of the ranges
// here are dates.
func builtinKind(id int) numKind {
	switch {
	case id >= 18 && id <= 21, id == 32, id == 33, id == 45, id == 47:
		return timeNumber
	case id == 46:
		return hoursNumber
	case id >= 14 && id <= 22, id >= 27 && id <= 36, id >= 50 && id <= 58:
		return dateNumber
	}
	return plainNumber
}

// formatKind reads a custom format code for what it renders, by its tokens
// outside quoted literals and the bracketed conditions, colours, and locale
// tags a code can carry: a year or day makes it a date; failing that, a
// bracketed [h], [m] or [s] makes it a span of hours, and an hour or second
// a time of day; an m with neither is a month.
func formatKind(code string) numKind {
	var date, hours, clock, month, inQuote bool
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c == '\\':
			i++ // an escaped literal character
		case inQuote:
			inQuote = c != '"'
		case c == '"':
			inQuote = true
		case c == '[':
			end := strings.IndexByte(code[i:], ']')
			if end < 0 {
				i = len(code)
				continue
			}
			if tag := strings.ToLower(code[i+1 : i+end]); tag != "" && strings.Trim(tag, "hms") == "" {
				hours = true
			}
			i += end
		case strings.IndexByte("ydYD", c) >= 0:
			date = true
		case strings.IndexByte("hsHS", c) >= 0:
			clock = true
		case c == 'm' || c == 'M':
			month = true
		}
	}
	switch {
	case date:
		return dateNumber
	case hours:
		return hoursNumber
	case clock:
		return timeNumber
	case month:
		return dateNumber
	}
	return plainNumber
}

// excelDate renders a serial day count the way the sheet would. Excel counts
// days from the start of 1900 and believes 1900 was a leap year, so serials
// past its imaginary 29 February are one day off from a plain count; some
// Mac workbooks count from 1904 instead and say so. A value that is not a
// plausible serial is left as written.
func excelDate(raw string, epoch1904 bool) string {
	serial, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || serial < 0 || serial >= 2958466 { // past 9999-12-31
		return raw
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	switch {
	case epoch1904:
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	case serial < 61:
		base = time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	days, frac := math.Modf(serial)
	t := base.AddDate(0, 0, int(days)).Add(time.Duration(math.Round(frac*86400)) * time.Second)
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04")
}

// excelTime renders the time a serial carries: the time of day, from its
// fraction of a day, or with span the whole of it as hours, as [h]:mm
// shows 1.5 as 36:00. Seconds are shown when there are any. A value that
// is not a plausible serial is left as written.
func excelTime(raw string, span bool) string {
	serial, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || serial < 0 || serial >= 2958466 {
		return raw
	}
	secs := int64(math.Round(serial * 86400))
	if !span {
		secs %= 86400
	}
	out := fmt.Sprintf("%02d:%02d", secs/3600, secs/60%60)
	if span {
		out = fmt.Sprintf("%d:%02d", secs/3600, secs/60%60)
	}
	if secs%60 != 0 {
		out += fmt.Sprintf(":%02d", secs%60)
	}
	return out
}

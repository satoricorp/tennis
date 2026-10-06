package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// testWorkbook is a three-sheet .xlsx in miniature, exercising the shapes a
// reader has to get right: shared strings including a formatted run and a
// phonetic guide, an inline string, a boolean, a number, a formula with a
// cached result, a gap column, an empty row, sheets whose relationship ids
// are out of order with their workbook order, one absolute and one relative
// part target, and cells whose style — built-in or custom — makes them dates
// or times.
func testWorkbook(t *testing.T) []byte {
	t.Helper()
	return zipOf(t, testWorkbookParts())
}

func testWorkbookParts() map[string]string {
	return map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Budget" sheetId="1" r:id="rId2"/><sheet name="Notes" sheetId="2" r:id="rId1"/><sheet name="Trips" sheetId="3" r:id="rId4"/></sheets>
</workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/xl/worksheets/sheet1.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet3.xml"/>
<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`,
		"xl/styles.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="3"><numFmt numFmtId="164" formatCode="yyyy\-mm\-dd\ hh:mm"/><numFmt numFmtId="165" formatCode="&quot;$&quot;#,##0.00;[Red]&quot;$&quot;#,##0.00"/><numFmt numFmtId="166" formatCode="[h]:mm"/></numFmts>
<cellXfs count="6"><xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="164"/><xf numFmtId="165"/><xf numFmtId="21"/><xf numFmtId="166"/></cellXfs>
</styleSheet>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="3" uniqueCount="3">
<si><t>Item</t></si>
<si><t>Amount</t></si>
<si><r><t xml:space="preserve">Rent </t></r><r><rPr><b/></rPr><t>(monthly)</t></r><rPh sb="0" eb="4"><t>ignored</t></rPh></si>
</sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="C2"><v>1200</v></c></row>
<row r="3"><c r="A3" t="inlineStr"><is><t>Paid</t></is></c><c r="B3" t="b"><v>1</v></c><c r="C3"><f>SUM(C2)</f><v>1200</v></c></row>
<row r="4"/>
</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="str"><v>two lines
in one cell</v></c></row>
</sheetData></worksheet>`,
		"xl/worksheets/sheet3.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" s="1"><v>46182</v></c><c r="B1" s="2"><v>46182.5</v></c><c r="C1" s="3"><v>842.5</v></c><c r="D1" s="0"><v>46182</v></c></row>
<row r="2"><c r="A2" s="4" t="n"><v>0.3958333333333333</v></c><c r="B2" s="5"><v>1.5</v></c></row>
</sheetData></worksheet>`,
	}
}

// TestXLSXTextRendersSheetsAsRows: what the index sees is every sheet under
// its name, one row per line, with cells placed by column so a gap does not
// shift the row.
func TestXLSXTextRendersSheetsAsRows(t *testing.T) {
	got, err := xlsxText(testWorkbook(t))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"## Budget",
		"",
		"Item\tAmount",
		"Rent (monthly)\t\t1200",
		"Paid\tTRUE\t1200",
		"",
		"## Notes",
		"",
		"two lines in one cell",
		"",
		"## Trips",
		"",
		"2026-06-09\t2026-06-09 12:00\t842.5\t46182",
		"09:30\t36:00",
	}, "\n")
	if got != want {
		t.Errorf("xlsxText rendered:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestXLSXTextReadsStrictWorkbooks: a workbook saved as Strict Open XML,
// which names its parts' relationships in the ISO namespace rather than the
// transitional one, reads as the same workbook saved the usual way.
func TestXLSXTextReadsStrictWorkbooks(t *testing.T) {
	parts := testWorkbookParts()
	for name, body := range parts {
		body = strings.ReplaceAll(body, "http://schemas.openxmlformats.org/spreadsheetml/2006/main", "http://purl.oclc.org/ooxml/spreadsheetml/main")
		parts[name] = strings.ReplaceAll(body, "http://schemas.openxmlformats.org/officeDocument/2006/relationships", "http://purl.oclc.org/ooxml/officeDocument/relationships")
	}
	if !strings.Contains(parts["xl/workbook.xml"], `xmlns:r="http://purl.oclc.org/ooxml/officeDocument/relationships"`) {
		t.Fatal("the fixture did not become Strict")
	}
	want, err := xlsxText(testWorkbook(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := xlsxText(zipOf(t, parts)); err != nil || got != want {
		t.Errorf("a Strict workbook: %v\n%s\n\nwant:\n%s", err, got, want)
	}
}

// TestXLSXTextRefusesWhatIsNotAWorkbook: a zip that is not a workbook, and
// bytes that are not a zip, are declined with a reason rather than indexed
// as an empty document.
func TestXLSXTextRefusesWhatIsNotAWorkbook(t *testing.T) {
	if _, err := xlsxText([]byte("just some text")); err == nil {
		t.Error("plain text passed as a workbook")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("notes/a.md")
	w.Write([]byte("hello"))
	zw.Close()
	if _, err := xlsxText(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "workbook.xml") {
		t.Errorf("a zip of notes passed as a workbook: %v", err)
	}
}

func TestColumnIndex(t *testing.T) {
	for ref, want := range map[string]int{
		"A1": 0, "C7": 2, "Z1": 25, "AA1": 26, "AB12": 27, "": -1, "7": -1,
		"XFD1": 16383, "XFE1": 16383, "ZZZZZ1": 16383, "ZZZZZZZZZZZZZZZZZZZZ1": 16383,
	} {
		if got := columnIndex(ref); got != want {
			t.Errorf("columnIndex(%q) = %d, want %d", ref, got, want)
		}
	}
}

// TestDateFormats: a date is only a date because its style says so, and a
// style says so with a built-in id or a custom code with date tokens in it —
// outside the quoted and bracketed parts, where "Red" and "$" are not days.
// A code with an hour or second and no year or day is a time of day, and
// one with a bracketed [h] a span of hours.
func TestDateFormats(t *testing.T) {
	for code, want := range map[string]numKind{
		"yyyy-mm-dd": dateNumber, "d-mmm-yy": dateNumber, "[$-409]mmmm d, yyyy": dateNumber, "mmmm": dateNumber,
		"yyyy-mm-dd h:mm:ss": dateNumber, "m/d/yy h:mm AM/PM": dateNumber,
		"h:mm": timeNumber, "h:mm:ss": timeNumber, "h:mm AM/PM": timeNumber, "hh:mm:ss.0": timeNumber, "mm:ss": timeNumber,
		"[h]:mm": hoursNumber, "[hh]:mm:ss": hoursNumber, "[mm]:ss": hoursNumber, "[Red][h]:mm": hoursNumber,
		"General": plainNumber, "0.00": plainNumber, "#,##0": plainNumber, "0%": plainNumber, "@": plainNumber,
		`"$"#,##0.00;[Red]"$"#,##0.00`: plainNumber, `"Due" 0`: plainNumber, `0 \d`: plainNumber, "[Red]0": plainNumber,
	} {
		if got := formatKind(code); got != want {
			t.Errorf("formatKind(%q) = %v, want %v", code, got, want)
		}
	}
	for id, want := range map[int]numKind{
		0: plainNumber, 4: plainNumber, 14: dateNumber, 18: timeNumber, 20: timeNumber, 21: timeNumber,
		22: dateNumber, 45: timeNumber, 46: hoursNumber, 49: plainNumber,
	} {
		if got := builtinKind(id); got != want {
			t.Errorf("builtinKind(%d) = %v, want %v", id, got, want)
		}
	}
	for serial, want := range map[string]string{
		"0.3958333333333333": "09:30", "0.3960069444444445": "09:30:15", "46182.75": "18:00", "0": "00:00",
		"0.99999999": "00:00", "not a number": "not a number", "-1": "-1",
	} {
		if got := excelTime(serial, false); got != want {
			t.Errorf("excelTime(%q) = %q, want %q", serial, got, want)
		}
	}
	for serial, want := range map[string]string{"1.5": "36:00", "1.510416666666667": "36:15", "0.3958333333333333": "9:30", "0.0000115740740741": "0:00:01"} {
		if got := excelTime(serial, true); got != want {
			t.Errorf("excelTime(%q, span) = %q, want %q", serial, got, want)
		}
	}
	for serial, want := range map[string]string{
		"1": "1900-01-01", "61": "1900-03-01", "46182": "2026-06-09", "46182.5": "2026-06-09 12:00",
		"not a number": "not a number", "-5": "-5", "99999999": "99999999",
	} {
		if got := excelDate(serial, false); got != want {
			t.Errorf("excelDate(%q) = %q, want %q", serial, got, want)
		}
	}
	if got := excelDate("0", true); got != "1904-01-01" {
		t.Errorf("1904 epoch: excelDate(0) = %q", got)
	}
}

// workbookOf is a one-sheet workbook around sheetData rows, with a shared
// string table when sst is not empty.
func workbookOf(t *testing.T, sst string, rows func(io.Writer)) []byte {
	t.Helper()
	parts := map[string]func(io.Writer){
		"xl/workbook.xml": repeat(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`, "", "", 0),
		"xl/_rels/workbook.xml.rels": repeat(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>`+
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>`+
			`</Relationships>`, "", "", 0),
		"xl/worksheets/sheet1.xml": func(w io.Writer) {
			io.WriteString(w, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
			rows(w)
			io.WriteString(w, `</sheetData></worksheet>`)
		},
	}
	if sst != "" {
		parts["xl/sharedStrings.xml"] = repeat(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`+sst+`</sst>`, "", "", 0)
	}
	return bomb(t, parts)
}

// TestXLSXStopsAtTheCap: the text a workbook comes to is counted as it is
// built, wherever it comes from — one shared string held by every cell, a
// shared string table past the cap, or the tabs that place a cell in its
// column — and a cell reference past XFD is read as XFD rather than as a
// row of millions of empty cells.
func TestXLSXStopsAtTheCap(t *testing.T) {
	megabyte := strings.Repeat("x", 1<<20)
	fanOut := workbookOf(t, `<si><t>`+megabyte+`</t></si>`, func(w io.Writer) {
		for r := 1; r <= 300; r++ {
			fmt.Fprintf(w, `<row r="%d"><c r="A%d" t="s"><v>0</v></c></row>`, r, r)
		}
	})
	readsWithin(t, "fan-out.xlsx", xlsxText, fanOut, errTextCap, allocBound)

	var table strings.Builder
	for i := range 12 {
		table.WriteString(`<si><t>` + strings.Repeat(string(rune('a'+i)), 1<<20) + `</t></si>`)
	}
	readsWithin(t, "strings.xlsx", xlsxText, workbookOf(t, table.String(), func(w io.Writer) {
		io.WriteString(w, `<row r="1"><c r="A1" t="s"><v>0</v></c></row>`)
	}), errTextCap, allocBound)

	padded := workbookOf(t, "", func(w io.Writer) {
		for r := 1; r <= 1000; r++ {
			fmt.Fprintf(w, `<row r="%d"><c r="A%d"><v>1</v></c><c r="XFD%d"><v>2</v></c></row>`, r, r, r)
		}
	})
	readsWithin(t, "padded.xlsx", xlsxText, padded, errTextCap, allocBound)

	far := workbookOf(t, "", func(w io.Writer) {
		io.WriteString(w, `<row r="1"><c r="A1" t="inlineStr"><is><t>near</t></is></c><c r="ZZZZZ1" t="inlineStr"><is><t>far</t></is></c></row>`)
		io.WriteString(w, `<row r="2"><c r="ZZZZZZZ2" t="inlineStr"><is><t>farther</t></is></c></row>`)
	})
	got := readsWithin(t, "far.xlsx", xlsxText, far, nil, 1<<20)
	tabs := strings.Repeat("\t", 16383)
	if want := "## S\n\nnear" + tabs + "far\n" + tabs + "farther"; got != want {
		t.Errorf("cells past XFD: got %d bytes, want %d", len(got), len(want))
	}
}

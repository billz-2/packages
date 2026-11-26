package streamxlsx

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/errors"
	"github.com/xuri/excelize/v2"
)

const (
	sheetPath    = "xl/worksheets/sheet1.xml"
	sheetDataEnd = "</sheetData>"
	// bufferSize is what the sheet XML collects before the deflate of its zip entry gets it.
	bufferSize = 64 << 10
)

var (
	bstrExp       = regexp.MustCompile(`_x[a-fA-F\d]{4}_`)
	bstrEscapeExp = regexp.MustCompile(`x[a-fA-F\d]{4}_`)
)

// sheetWriter writes an xlsx of one sheet to w as the rows come, in constant memory.
//
// excelize's StreamWriter keeps the rows in a temporary file, but File.Write and File.SaveAs build the whole
// zip in a bytes.Buffer before they write it (WriteToBuffer, in every version up to v2.11.1): an export of a
// million rows held ~200 MiB, growing with the rows. Here excelize makes the book with the header row only;
// its sheet XML is cut at </sheetData>, the rows go between the two halves into the zip entry as they come,
// in the XML the StreamWriter writes, and the other parts of the book are copied from it as they are.
type sheetWriter struct {
	zw       *zip.Writer
	sheet    *bufio.Writer
	template *zip.Reader
	tail     string
	columns  int
	row      int
}

func newSheetWriter(w io.Writer, headers []string) (*sheetWriter, error) {
	book, err := headerBook(headers)
	if err != nil {
		return nil, err
	}

	template, err := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	if err != nil {
		return nil, errors.Wrap(err, "read the header book")
	}

	head, tail, err := splitSheet(template)
	if err != nil {
		return nil, err
	}

	zw := zip.NewWriter(w)

	entry, err := zw.CreateHeader(&zip.FileHeader{Name: sheetPath, Method: zip.Deflate})
	if err != nil {
		return nil, errors.Wrap(err, "create the sheet entry")
	}

	sheet := bufio.NewWriterSize(entry, bufferSize)
	if _, err = sheet.WriteString(head); err != nil {
		return nil, errors.Wrap(err, "write the sheet head")
	}

	return &sheetWriter{zw: zw, sheet: sheet, template: template, tail: tail, columns: len(headers), row: 1}, nil
}

// headerBook is the xlsx excelize makes of the header row alone.
func headerBook(headers []string) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create stream writer")
	}

	cells := make([]interface{}, len(headers))
	for i, header := range headers {
		cells[i] = excelize.Cell{Value: header}
	}

	if err = sw.SetRow("A1", cells); err != nil {
		return nil, errors.Wrap(err, "stream writer headers set row failed")
	}

	if err = sw.Flush(); err != nil {
		return nil, errors.Wrap(err, "stream writer flush failed")
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, errors.Wrap(err, "write the header book")
	}

	return buf.Bytes(), nil
}

// splitSheet cuts the sheet XML of the template where the rows end.
func splitSheet(template *zip.Reader) (head, tail string, err error) {
	for _, file := range template.File {
		if file.Name != sheetPath {
			continue
		}

		r, err := file.Open()
		if err != nil {
			return "", "", errors.Wrap(err, "open the template sheet")
		}

		raw, err := io.ReadAll(r)
		_ = r.Close()

		if err != nil {
			return "", "", errors.Wrap(err, "read the template sheet")
		}

		xmlText := string(raw)

		end := strings.LastIndex(xmlText, sheetDataEnd)
		if end < 0 {
			return "", "", errors.New("the template sheet has no " + sheetDataEnd)
		}

		return xmlText[:end], xmlText[end:], nil
	}

	return "", "", errors.New("the template has no " + sheetPath)
}

// WriteRow writes the next row: the values of the columns from A, as the StreamWriter writes them.
func (s *sheetWriter) WriteRow(values []interface{}) error {
	if s.row >= excelize.TotalRows {
		return excelize.ErrMaxRows
	}

	if len(values) > excelize.MaxColumns {
		return excelize.ErrColumnNumber
	}

	s.row++
	rowNum := strconv.Itoa(s.row)

	_, _ = s.sheet.WriteString(`<row r="`)
	_, _ = s.sheet.WriteString(rowNum)
	_, _ = s.sheet.WriteString(`">`)

	for i, value := range values {
		name, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}

		writeCell(s.sheet, name+rowNum, value)
	}

	_, err := s.sheet.WriteString(`</row>`)

	return errors.Wrap(err, "write the row")
}

// Close ends the sheet, copies the other parts of the book and ends the zip. It does not close w.
func (s *sheetWriter) Close() error {
	if _, err := s.sheet.WriteString(s.tail); err != nil {
		return errors.Wrap(err, "write the sheet tail")
	}

	if err := s.sheet.Flush(); err != nil {
		return errors.Wrap(err, "flush the sheet")
	}

	for _, file := range s.template.File {
		if file.Name == sheetPath {
			continue
		}

		if err := s.zw.Copy(file); err != nil {
			return errors.Wrapf(err, "copy %s", file.Name)
		}
	}

	return errors.Wrap(s.zw.Close(), "close the zip")
}

// writeCell writes a cell as excelize's StreamWriter.SetRow writes excelize.Cell{Value: value}
// (setCellValFunc and writeCell of excelize v2.9.1), for the values rowToCells kept.
func writeCell(w *bufio.Writer, ref string, value interface{}) {
	var (
		typ, val, inline string
		preserve         bool
	)

	switch v := value.(type) {
	case nil:
	case string:
		typ = "inlineStr"
		inline, preserve = trimCellValue(v)
	case bool:
		typ, val = "b", "0"
		if v {
			val = "1"
		}
	case int:
		val = strconv.FormatInt(int64(v), 10)
	case int64:
		val = strconv.FormatInt(v, 10)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			typ = "inlineStr"
			inline, preserve = trimCellValue(fmt.Sprint(v))
		} else {
			val = strconv.FormatFloat(v, 'f', -1, 64)
		}
	case time.Time:
		typ = "inlineStr"
		inline, preserve = trimCellValue(v.Format("2006-01-02"))
	default:
		typ = "inlineStr"
		inline, preserve = trimCellValue(fmt.Sprintf("%v", v))
	}

	_, _ = w.WriteString(`<c r="`)
	_, _ = w.WriteString(ref)
	_, _ = w.WriteString(`"`)

	if typ != "" {
		_, _ = w.WriteString(` t="`)
		_, _ = w.WriteString(typ)
		_, _ = w.WriteString(`"`)
	}

	_, _ = w.WriteString(`>`)

	if val != "" {
		_, _ = w.WriteString(`<v>`)
		_ = xml.EscapeText(w, []byte(val))
		_, _ = w.WriteString(`</v>`)
	}

	if typ == "inlineStr" {
		_, _ = w.WriteString(`<is><t`)
		if preserve {
			_, _ = w.WriteString(` xml:space="preserve"`)
		}

		_, _ = w.WriteString(`>`)
		_, _ = w.WriteString(inline)
		_, _ = w.WriteString(`</t></is>`)
	}

	_, _ = w.WriteString(`</c>`)
}

// trimCellValue is excelize's trimCellValue(value, true): cut to the cell limit, escaped for XML with the
// line feeds kept, and the _xHHHH_ sequences escaped so that Excel shows them as they are.
func trimCellValue(value string) (string, bool) {
	if utf8.RuneCountInString(value) > excelize.TotalCellChars {
		value = string([]rune(value)[:excelize.TotalCellChars])
	}

	var preserve bool

	if value != "" {
		prefix, suffix := value[0], value[len(value)-1]
		for _, ascii := range []byte{9, 10, 13, 32} {
			if prefix == ascii || suffix == ascii {
				preserve = true

				break
			}
		}

		var buf strings.Builder

		_ = xml.EscapeText(&buf, []byte(value))
		value = strings.ReplaceAll(buf.String(), "&#xA;", "\n")
	}

	return bstrMarshal(value), preserve
}

// bstrMarshal is excelize's bstrMarshal.
func bstrMarshal(s string) string {
	var result strings.Builder

	matches, l, cursor := bstrExp.FindAllStringSubmatchIndex(s, -1), len(s), 0
	for _, match := range matches {
		result.WriteString(s[cursor:match[0]])
		subStr := s[match[0]:match[1]]

		if subStr == "_x005F_" {
			cursor = match[1]

			if match[1]+6 <= l && bstrEscapeExp.MatchString(s[match[1]:match[1]+6]) {
				if _, err := strconv.Unquote(`"\u` + s[match[1]+1:match[1]+5] + `"`); err == nil {
					result.WriteString(subStr + "x005F" + subStr)

					continue
				}
			}

			result.WriteString(subStr + "x005F_")

			continue
		}

		if bstrExp.MatchString(subStr) {
			cursor = match[1]
			if _, err := strconv.Unquote(`"\u` + s[match[0]+2:match[1]-1] + `"`); err == nil {
				result.WriteString("_x005F" + subStr)

				continue
			}
		}
	}

	if cursor < l {
		result.WriteString(s[cursor:])
	}

	return result.String()
}

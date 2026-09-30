package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// iocLangByColumn maps the Multilingual IOC World Bird List's column
// headers to the two-letter codes this app uses (see iso6391ByGBIFCode).
// Headers with no entry (the alternate "Chinese (Traditional)", "French
// (Gaudin)" and "Portuguese (Portuguese)" variants) are skipped: each
// language gets exactly one IOC column, the general-purpose one.
var iocLangByColumn = map[string]string{
	"English": "en", "Catalan": "ca", "Chinese": "zh", "Croatian": "hr",
	"Czech": "cs", "Danish": "da", "Dutch": "nl", "Esperanto": "eo",
	"Finnish": "fi", "French": "fr", "German": "de", "Italian": "it",
	"Japanese": "ja", "Lithuanian": "lt", "Norwegian": "nb", "Polish": "pl",
	"Portuguese (Lusophone)": "pt", "Russian": "ru", "Serbian": "sr",
	"Slovak": "sk", "Spanish": "es", "Swedish": "sv", "Turkish": "tr",
	"Ukrainian": "uk", "Afrikaans": "af", "Arabic": "ar", "Belarusian": "be",
	"Bulgarian": "bg", "Estonian": "et", "Greek": "el", "Hebrew": "he",
	"Hungarian": "hu", "Icelandic": "is", "Indonesian": "id", "Korean": "ko",
	"Latvian": "lv", "Macedonian": "mk", "Malayalam": "ml",
	"Northern Sami": "se", "Persian": "fa", "Romanian": "ro",
	"Slovenian": "sl", "Thai": "th",
}

// loadIOCNames reads a Multilingual IOC World Bird List .xlsx and returns
// its common names as names[lang][scientific name]. The IOC list is
// CC BY 3.0 (https://www.worldbirdnames.org), one curated name per species
// per language, so unlike GBIF's pooled vernacular records there is
// nothing to vote on.
func loadIOCNames(path string) (map[string]map[string]string, error) {
	rows, err := readXLSXFirstSheet(path)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: empty sheet", path)
	}

	// Header row: one column named "IOC_<version>" holds the scientific
	// name; every other header that appears in iocLangByColumn is a language.
	sciCol := ""
	langCols := map[string]string{} // column letter -> lang code
	for col, header := range rows[0] {
		header = strings.TrimSpace(header)
		if strings.HasPrefix(header, "IOC_") {
			sciCol = col
		} else if lang, ok := iocLangByColumn[header]; ok {
			langCols[col] = lang
		}
	}
	if sciCol == "" {
		return nil, fmt.Errorf("%s: no IOC_<version> scientific-name column in header row", path)
	}

	out := map[string]map[string]string{}
	for _, row := range rows[1:] {
		sci := strings.TrimSpace(row[sciCol])
		if sci == "" {
			continue
		}
		for col, lang := range langCols {
			name := strings.TrimSpace(row[col])
			if name == "" {
				continue
			}
			if out[lang] == nil {
				out[lang] = map[string]string{}
			}
			out[lang][sci] = name
		}
	}
	return out, nil
}

var cellColumn = regexp.MustCompile(`^[A-Z]+`)

// readXLSXFirstSheet returns the first worksheet's rows as column letter ->
// cell text. An .xlsx is a zip of XML parts; this handles only what the IOC
// file uses (shared-string and inline-string cells), which avoids a
// spreadsheet dependency for one file.
func readXLSXFirstSheet(path string) ([]map[string]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}

	shared, err := readSharedStrings(files["xl/sharedStrings.xml"])
	if err != nil {
		return nil, err
	}
	sheet := files["xl/worksheets/sheet1.xml"]
	if sheet == nil {
		return nil, fmt.Errorf("%s: no xl/worksheets/sheet1.xml", path)
	}
	rc, err := sheet.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var doc struct {
		Rows []struct {
			Cells []struct {
				Ref    string `xml:"r,attr"`
				Type   string `xml:"t,attr"`
				Value  string `xml:"v"`
				Inline struct {
					Text []string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.NewDecoder(rc).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse sheet1.xml: %w", err)
	}

	rows := make([]map[string]string, 0, len(doc.Rows))
	for _, r := range doc.Rows {
		row := map[string]string{}
		for _, c := range r.Cells {
			col := cellColumn.FindString(c.Ref)
			switch c.Type {
			case "s":
				i, err := strconv.Atoi(c.Value)
				if err != nil || i < 0 || i >= len(shared) {
					continue
				}
				row[col] = shared[i]
			case "inlineStr":
				row[col] = strings.Join(c.Inline.Text, "")
			default:
				row[col] = c.Value
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// readSharedStrings returns xl/sharedStrings.xml's entries in order. An entry
// is either a plain <t> or several rich-text <r><t> runs; phonetic runs
// (<rPh>) are excluded by only reading direct t and r/t children.
func readSharedStrings(f *zip.File) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Items []struct {
			T    []string `xml:"t"`
			Runs []struct {
				T string `xml:"t"`
			} `xml:"r"`
		} `xml:"si"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse sharedStrings.xml: %w", err)
	}
	out := make([]string, len(doc.Items))
	for i, it := range doc.Items {
		var b strings.Builder
		for _, t := range it.T {
			b.WriteString(t)
		}
		for _, r := range it.Runs {
			b.WriteString(r.T)
		}
		out[i] = b.String()
	}
	return out, nil
}

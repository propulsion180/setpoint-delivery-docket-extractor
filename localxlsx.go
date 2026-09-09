package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

const localSheetName = "Sheet1"

// noPartNoLabel is written into the Part No cell for a row whose source
// docket had no part number at all (only an item description). Using a
// fixed, recognisable label rather than leaving the cell blank makes it
// obvious to a human why the row lacks a code, and lets matchKeyForRow
// recognise the row again on a later docket with the same item.
const noPartNoLabel = "REVIEW: no part number -- matched by item name"

// matchKeyForRow returns the key used to match a row against others: the
// Part No when present, or "item:<item text>" when it isn't (or when the
// Part No cell holds noPartNoLabel from a previous run).
func matchKeyForRow(partNo, item string) string {
	partNo = strings.TrimSpace(partNo)
	if partNo == "" || partNo == noPartNoLabel {
		return "item:" + strings.TrimSpace(item)
	}
	return partNo
}

// tableState is the current headers and data rows of a job file's table.
type tableState struct {
	Headers []string
	Rows    [][]string
}

// jobFileName returns the standard filename for a job's docket summary.
func jobFileName(jobNo string) string {
	return fmt.Sprintf("%s-docket-summary.xlsx", jobNo)
}

// parseDocketID splits a docket ID like "35214-8" into job number "35214"
// and slip number "8" -- splitting on the FIRST hyphen, not the last. A
// stray mark near the docket number on a scanned/printed docket can OCR as
// an extra trailing hyphen-like character (observed on a real docket:
// "35214-21 -" instead of "35214-21"), and splitting on the last hyphen in
// that case would wrongly fold the extra character into the job number.
// Splitting on the first hyphen avoids that, since a real docket ID never
// has a hyphen within the job number itself.
//
// Each half is validated as purely numeric. Rather than erroring out (which
// would silently drop the docket), an invalid half is flagged instead: a
// bad job number still gets a file, just named with a "REVIEW-" prefix; a
// bad slip number still gets written, just with its column header marked
// for review. Nothing is silently skipped or silently trusted.
func parseDocketID(docketID string) (jobNo, slipNo string, jobFlagged, slipFlagged bool) {
	trimmed := strings.TrimSpace(docketID)
	idx := strings.Index(trimmed, "-")
	if idx == -1 {
		// No hyphen at all -- treat the whole thing as a suspect job
		// number with an empty slip number, and flag both.
		return "REVIEW-" + sanitizeForFilename(trimmed), "", true, true
	}

	jobPart := strings.TrimSpace(trimmed[:idx])
	slipPart := strings.TrimSpace(trimmed[idx+1:])

	jobNo, slipNo = jobPart, slipPart

	if !isDigitsOnly(jobPart) {
		jobFlagged = true
		jobNo = "REVIEW-" + sanitizeForFilename(jobPart)
	}
	if !isDigitsOnly(slipPart) {
		slipFlagged = true
		slipNo = sanitizeForFilename(slipPart)
	}

	return jobNo, slipNo, jobFlagged, slipFlagged
}

// isDigitsOnly reports whether s is non-empty and contains only ASCII digits.
func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeForFilename strips anything that isn't a letter, digit, hyphen,
// or underscore, so a stray/unexpected character can't produce an invalid
// or surprising filename.
func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

// columnLetter converts a zero-based column index to an Excel column
// letter: 0 -> A, 25 -> Z, 26 -> AA, etc.
func columnLetter(index int) string {
	letters := ""
	index++ // switch to 1-based for the algorithm below
	for index > 0 {
		index--
		letters = string(rune('A'+(index%26))) + letters
		index /= 26
	}
	return letters
}

// classifyQuantity checks a quantity string and returns either a clean
// float64 for normal writing, or a flagged text string for anything
// suspicious (negative, or not a valid number). Negative values most often
// come from OCR misreading a hand-drawn strikethrough mark as a minus sign
// on a crossed-out line item -- rather than guess whether that's really
// what happened, this writes a visible flag instead of a number, which
// also means it's automatically excluded from the Slip Total's SUM (which
// ignores text cells).
func classifyQuantity(raw string) (value interface{}, flagged bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	num, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return fmt.Sprintf("REVIEW: %q -- couldn't parse as a number", raw), true
	}
	if num < 0 {
		return fmt.Sprintf("REVIEW: %s -- negative, possibly a crossed-out line", raw), true
	}
	return num, false
}

// setQuantityCell writes a value to a cell as-is -- a float64 for a clean
// quantity, or a string for a flagged anomaly (see classifyQuantity).
func setQuantityCell(f *excelize.File, sheet, cell string, value interface{}) {
	f.SetCellValue(sheet, cell, value)
}

// resolveOrCreateLocalJobFile opens the job's spreadsheet from outputDir if
// it exists, or creates a fresh one with the standard headers if it doesn't.
func resolveOrCreateLocalJobFile(outputDir, jobNo string) (*excelize.File, string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, "", err
	}
	path := filepath.Join(outputDir, jobFileName(jobNo))

	if _, err := os.Stat(path); err == nil {
		f, err := excelize.OpenFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("failed to open existing job file: %w", err)
		}
		return f, path, nil
	}

	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", localSheetName); err != nil {
		return nil, "", err
	}
	f.SetCellValue(localSheetName, "A1", "Part No")
	f.SetCellValue(localSheetName, "B1", "Item Name")
	f.SetCellValue(localSheetName, "C1", "Slip Total")
	f.SetColWidth(localSheetName, "A", "A", 32.14)
	f.SetColWidth(localSheetName, "B", "B", 108.43)
	f.SetColWidth(localSheetName, "C", "C", 15.29)

	return f, path, nil
}

// readLocalTableState reads all rows from the sheet; the first row is
// treated as headers.
func readLocalTableState(f *excelize.File) (*tableState, error) {
	rows, err := f.GetRows(localSheetName)
	if err != nil {
		return nil, err
	}
	state := &tableState{}
	for i, row := range rows {
		if i == 0 {
			state.Headers = row
		} else {
			state.Rows = append(state.Rows, row)
		}
	}
	if state.Headers == nil {
		state.Headers = []string{"Part No", "Item Name", "Slip Total"}
	}
	return state, nil
}

// recalcWithLibreOffice forces LibreOffice to open, fully recalculate, and
// resave the file. This is necessary because excelize (like most libraries
// that write formulas programmatically) writes the formula string but no
// cached result -- so without this step, formulas display as 0 in any
// viewer that trusts cached values rather than recalculating on open.
//
// LibreOffice's --convert-to reliably fails if the output path is the exact
// same file it's reading from (a known quirk, not specific to this
// project), so this converts into a temporary directory first, then moves
// the recalculated file over the original.
func recalcWithLibreOffice(path string) error {
	tmpDir, err := os.MkdirTemp("", "recalc-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("soffice", "--headless", "--calc", "--convert-to", "xlsx", "--outdir", tmpDir, path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("libreoffice recalc failed: %w (output: %s)", err, string(output))
	}

	recalculatedPath := filepath.Join(tmpDir, filepath.Base(path))
	if _, err := os.Stat(recalculatedPath); err != nil {
		return fmt.Errorf("recalculated file not found at %s (output: %s)", recalculatedPath, string(output))
	}

	if err := os.Rename(recalculatedPath, path); err != nil {
		return fmt.Errorf("failed to move recalculated file into place: %w", err)
	}

	return nil
}

// addDocketToLocalJobFile reads/writes a local .xlsx file via excelize,
// adding one docket's parts as a new column (and new rows for any parts
// never seen before in this job).
func addDocketToLocalJobFile(outputDir string, docket *DeliveryDocket) error {
	docketID := docket.Header["docketno"]
	jobNo, slipNo, jobFlagged, slipFlagged := parseDocketID(docketID)
	if jobFlagged {
		log.Printf("  ANOMALY parsing docket ID %q: job number portion doesn't look purely numeric -- using %q and flagging the file for review\n", docketID, jobNo)
	}
	if slipFlagged {
		log.Printf("  ANOMALY parsing docket ID %q: slip number portion doesn't look purely numeric (%q) -- flagging for review\n", docketID, slipNo)
	}

	date := docket.Header["date"]
	slipLabel := fmt.Sprintf("%s - Slip %s", date, slipNo)
	if slipFlagged {
		slipLabel += " (REVIEW - check docket number)"
	}

	f, path, err := resolveOrCreateLocalJobFile(outputDir, jobNo)
	if err != nil {
		return err
	}
	defer f.Close()

	state, err := readLocalTableState(f)
	if err != nil {
		return err
	}

	// Idempotency guard.
	for _, h := range state.Headers {
		if h == slipLabel {
			fmt.Printf("  docket %s already recorded in %s, skipping\n", docketID, path)
			return nil
		}
	}

	// matchKey determines how a row is matched against existing rows: by
	// Part No when present, or by item text when it isn't (Document
	// Intelligence returns no part number at all for lines where the
	// source docket genuinely left it blank). noPartNoLabel is written
	// into the Part No cell for such rows so it's visibly obvious why --
	// and is recognised here too, so a row created this way still matches
	// correctly on a later docket with the same blank-part-number item.
	existingRow := make(map[string]int) // matchKey -> 0-based index in state.Rows
	for i, row := range state.Rows {
		if len(row) > 1 {
			existingRow[matchKeyForRow(row[0], row[1])] = i
		}
	}

	docketQty := make(map[string]interface{}) // matchKey -> classified quantity value
	var newParts [][]string                   // rows never seen before: [PartNo, Item, Quantity]; PartNo may be ""
	for _, r := range docket.Rows {
		if len(r) < 3 {
			continue
		}
		partNo := strings.TrimSpace(r[0])
		item := strings.TrimSpace(r[1])
		key := matchKeyForRow(partNo, item)

		value, flagged := classifyQuantity(r[2])
		docketQty[key] = value
		if flagged {
			log.Printf("  ANOMALY on docket %s, part %s: %v\n", docketID, partNo, value)
		}
		if partNo == "" {
			log.Printf("  no part number for %q on docket %s, matching by item name instead\n", item, docketID)
		}
		if _, ok := existingRow[key]; !ok {
			newParts = append(newParts, r)
		}
	}

	newColIndex := len(state.Headers) // 0-based index of the new docket column
	newColLetter := columnLetter(newColIndex)

	// 1. Write the new column's header, and quantities for existing rows.
	f.SetCellValue(localSheetName, newColLetter+"1", slipLabel)
	for i, row := range state.Rows {
		key := matchKeyForRow(row[0], row[1])
		if qty, ok := docketQty[key]; ok {
			excelRow := i + 2
			cell := fmt.Sprintf("%s%d", newColLetter, excelRow)
			setQuantityCell(f, localSheetName, cell, qty)
		}
	}

	// 2. Add rows for parts never seen before in this job file.
	nextRow := len(state.Rows) + 2
	for i, part := range newParts {
		excelRow := nextRow + i
		partNoCell := part[0]
		if strings.TrimSpace(partNoCell) == "" {
			partNoCell = noPartNoLabel
		}
		f.SetCellValue(localSheetName, fmt.Sprintf("A%d", excelRow), partNoCell) // Part No
		f.SetCellValue(localSheetName, fmt.Sprintf("B%d", excelRow), part[1])    // Item Name
		cell := fmt.Sprintf("%s%d", newColLetter, excelRow)
		value, _ := classifyQuantity(part[2]) // already logged during docketQty build above
		setQuantityCell(f, localSheetName, cell, value)
	}

	// 3. Refresh Slip Total formulas for every row.
	totalRows := len(state.Rows) + len(newParts)
	firstDocketCol := columnLetter(3) // "D"
	lastDocketCol := newColLetter
	for i := 0; i < totalRows; i++ {
		excelRow := i + 2
		formula := fmt.Sprintf("SUM(%s%d:%s%d)", firstDocketCol, excelRow, lastDocketCol, excelRow)
		cell := fmt.Sprintf("C%d", excelRow)
		if err := f.SetCellFormula(localSheetName, cell, formula); err != nil {
			return fmt.Errorf("failed to set formula on %s: %w", cell, err)
		}
	}

	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("failed to save job file: %w", err)
	}

	if err := recalcWithLibreOffice(path); err != nil {
		// Not fatal -- the file is saved and correct, just with stale
		// (zero) cached formula values until something recalculates it.
		fmt.Printf("  warning: recalculation failed, formulas may show as 0 until manually recalculated: %v\n", err)
	}

	fmt.Printf("  saved %s\n", path)
	return nil
}

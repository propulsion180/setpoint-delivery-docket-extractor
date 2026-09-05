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
// and slip number "8".
func parseDocketID(docketID string) (jobNo string, slipNo string, err error) {
	idx := strings.LastIndex(docketID, "-")
	if idx == -1 {
		return "", "", fmt.Errorf("docket ID %q has no dash separator", docketID)
	}
	return docketID[:idx], docketID[idx+1:], nil
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

// addDocketToLocalJobFile reads/writes a local .xlsx file via excelize,
// adding one docket's parts as a new column (and new rows for any parts
// never seen before in this job).
func addDocketToLocalJobFile(outputDir string, docket *DeliveryDocket) error {
	docketID := docket.Header["docketno"]
	jobNo, slipNo, err := parseDocketID(docketID)
	if err != nil {
		return err
	}
	date := docket.Header["date"]
	slipLabel := fmt.Sprintf("%s - Slip %s", date, slipNo)

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

	existingRow := make(map[string]int) // Part No -> 0-based index in state.Rows
	for i, row := range state.Rows {
		if len(row) > 0 {
			existingRow[strings.TrimSpace(row[0])] = i
		}
	}

	docketQty := make(map[string]interface{})
	var newParts [][]string
	for _, r := range docket.Rows {
		if len(r) < 3 {
			continue
		}
		partNo := strings.TrimSpace(r[0])
		value, flagged := classifyQuantity(r[2])
		docketQty[partNo] = value
		if flagged {
			log.Printf("  ANOMALY on docket %s, part %s: %v\n", docketID, partNo, value)
		}
		if _, ok := existingRow[partNo]; !ok {
			newParts = append(newParts, r)
		}
	}

	newColIndex := len(state.Headers) // 0-based index of the new docket column
	newColLetter := columnLetter(newColIndex)

	// 1. Write the new column's header, and quantities for existing rows.
	f.SetCellValue(localSheetName, newColLetter+"1", slipLabel)
	for i, row := range state.Rows {
		partNo := strings.TrimSpace(row[0])
		if qty, ok := docketQty[partNo]; ok {
			excelRow := i + 2
			cell := fmt.Sprintf("%s%d", newColLetter, excelRow)
			setQuantityCell(f, localSheetName, cell, qty)
		}
	}

	// 2. Add rows for parts never seen before in this job file.
	nextRow := len(state.Rows) + 2
	for i, part := range newParts {
		excelRow := nextRow + i
		f.SetCellValue(localSheetName, fmt.Sprintf("A%d", excelRow), part[0]) // Part No
		f.SetCellValue(localSheetName, fmt.Sprintf("B%d", excelRow), part[1]) // Item Name
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

// recalcWithLibreOffice forces LibreOffice to open, fully recalculate, and
// resave the file in place. This is necessary because excelize (like most
// libraries that write formulas programmatically) writes the formula string
// but no cached result -- so without this step, formulas display as 0 in
// any viewer that trusts cached values rather than recalculating on open.
func recalcWithLibreOffice(path string) error {
	dir := filepath.Dir(path)
	cmd := exec.Command("soffice", "--headless", "--calc", "--convert-to", "xlsx", "--outdir", dir, path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("libreoffice recalc failed: %w (output: %s)", err, string(output))
	}
	return nil
}

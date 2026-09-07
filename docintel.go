package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const docIntelAPIVersion = "2024-11-30"

type analyzeOperation struct {
	Status        string        `json:"status"`
	AnalyzeResult analyzeResult `json:"analyzeResult"`
}

type analyzeResult struct {
	Tables []docTable `json:"tables"`
}

type docTable struct {
	RowCount    int       `json:"rowCount"`
	ColumnCount int       `json:"columnCount"`
	Cells       []docCell `json:"cells"`
}

type docCell struct {
	RowIndex    int    `json:"rowIndex"`
	ColumnIndex int    `json:"columnIndex"`
	Content     string `json:"content"`
	Kind        string `json:"kind"` // "columnHeader" for header cells, empty/null for data cells
}

// DeliveryDocket holds everything extracted from one docket PDF: the
// key-value header fields (Docket No, Date, Job No, Job Name, Site Address,
// etc.) and the parts table rows ([PartNo, Item, Quantity] per row).
type DeliveryDocket struct {
	Header map[string]string
	Rows   [][]string
}

// extractDocketFromPDF submits a PDF to Document Intelligence's Layout model
// and returns both the header key-value fields and the parts table rows.
func extractDocketFromPDF(pdfPath string) (*DeliveryDocket, error) {
	endpoint := os.Getenv("DOC_INTEL_ENDPOINT")
	key := os.Getenv("DOC_INTEL_KEY")

	pdfBytes, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read pdf: %w", err)
	}

	analyzeURL := fmt.Sprintf("%sdocumentintelligence/documentModels/prebuilt-layout:analyze?api-version=%s",
		endpoint, docIntelAPIVersion)

	var resp *http.Response
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequest("POST", analyzeURL, bytes.NewReader(pdfBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Ocp-Apim-Subscription-Key", key)
		req.Header.Set("Content-Type", "application/pdf")

		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}

		wait := 25 * time.Second
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if secs, err := strconv.Atoi(retryAfter); err == nil {
				wait = time.Duration(secs) * time.Second
			}
		}
		resp.Body.Close()
		log.Printf("  rate limited submitting to Document Intelligence, waiting %v before retrying\n", wait)
		time.Sleep(wait)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("analyze request failed (%d): %s", resp.StatusCode, string(body))
	}

	operationURL := resp.Header.Get("Operation-Location")
	if operationURL == "" {
		return nil, fmt.Errorf("no Operation-Location header in response")
	}

	// Poll until done, with a sane timeout. Extended to 5 minutes to leave
	// room for rate-limit backoffs on the free tier.
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		pollReq, err := http.NewRequest("GET", operationURL, nil)
		if err != nil {
			return nil, err
		}
		pollReq.Header.Set("Ocp-Apim-Subscription-Key", key)

		pollResp, err := http.DefaultClient.Do(pollReq)
		if err != nil {
			return nil, err
		}

		body, err := io.ReadAll(pollResp.Body)
		pollResp.Body.Close()
		if err != nil {
			return nil, err
		}

		if pollResp.StatusCode == http.StatusTooManyRequests {
			wait := 25 * time.Second
			if retryAfter := pollResp.Header.Get("Retry-After"); retryAfter != "" {
				if secs, err := strconv.Atoi(retryAfter); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
			log.Printf("  rate limited by Document Intelligence, waiting %v before retrying\n", wait)
			time.Sleep(wait)
			continue
		}

		if pollResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("poll failed (%d): %s", pollResp.StatusCode, string(body))
		}

		var op analyzeOperation
		if err := json.Unmarshal(body, &op); err != nil {
			return nil, err
		}

		switch op.Status {
		case "succeeded":
			docket := &DeliveryDocket{
				Header: extractHeaderTable(op.AnalyzeResult.Tables),
				Rows:   extractPartsTable(op.AnalyzeResult.Tables),
			}
			if docket.Rows == nil {
				return nil, fmt.Errorf("no 3-column parts table found in analysis result")
			}
			return docket, nil
		case "failed":
			return nil, fmt.Errorf("document intelligence analysis failed: %s", string(body))
			// "running" / "notStarted" -> keep polling
		}
	}

	return nil, fmt.Errorf("timed out waiting for analysis to complete")
}

// extractPartsTable finds the parts table and returns its data rows as
// [PartNo, Item, Quantity]. Rather than trusting raw column indices --
// which Document Intelligence can misalign between the header row and data
// rows when it detects a spurious empty column (this happens on dockets
// with very few rows) -- each row's non-empty cells are compressed
// left-to-right and matched positionally. This is more robust than
// indexing by column number or by header text position, since header and
// data rows have been observed to disagree about which raw column index a
// given field lives in.
//
// Two table shapes are recognised:
//   - 3 header fields (Part No / Item / Quantity): the normal case.
//   - 2 header fields, where the first normalizes to something containing
//     "item" (e.g. "Part No. Item" merged into one cell) and the second is
//     "Quantity": this happens when a docket genuinely has no part number
//     for a line -- Document Intelligence merges the empty Part No column
//     into the Item column. PartNo is set to "" for these rows; the caller
//     is expected to fall back to matching by item name.
func extractPartsTable(tables []docTable) [][]string {
	for _, table := range tables {
		grid := cellGrid(table)
		if len(grid) == 0 {
			continue
		}

		header := compressNonEmpty(grid[0])

		var mode string
		switch len(header) {
		case 3:
			if normalizeKey(header[0]) != "partno" || normalizeKey(header[2]) != "quantity" {
				continue
			}
			mode = "partNoItemQty"
		case 2:
			if !strings.Contains(normalizeKey(header[0]), "item") || normalizeKey(header[1]) != "quantity" {
				continue
			}
			mode = "itemQtyOnly"
		default:
			continue
		}

		var rows [][]string
		for r := 1; r < len(grid); r++ {
			compressed := compressNonEmpty(grid[r])
			if len(compressed) == 0 {
				continue // fully blank row
			}

			switch mode {
			case "partNoItemQty":
				if len(compressed) != 3 {
					log.Printf("  warning: parts table row %d has %d non-empty cells (expected 3), skipping to avoid misaligned data: %v\n",
						r, len(compressed), compressed)
					continue
				}
				rows = append(rows, compressed)
			case "itemQtyOnly":
				if len(compressed) != 2 {
					log.Printf("  warning: parts table row %d has %d non-empty cells (expected 2), skipping to avoid misaligned data: %v\n",
						r, len(compressed), compressed)
					continue
				}
				rows = append(rows, []string{"", compressed[0], compressed[1]}) // no part number
			}
		}
		return rows
	}
	return nil
}

// cellGrid converts a docTable's flat cell list into a full row/column
// grid, including the header row (index 0), sized rowCount x columnCount.
func cellGrid(table docTable) [][]string {
	grid := make([][]string, table.RowCount)
	for i := range grid {
		grid[i] = make([]string, table.ColumnCount)
	}
	for _, cell := range table.Cells {
		if cell.RowIndex < table.RowCount && cell.ColumnIndex < table.ColumnCount {
			grid[cell.RowIndex][cell.ColumnIndex] = cell.Content
		}
	}
	return grid
}

// compressNonEmpty returns only the non-blank values from a row, in order,
// dropping empty cells entirely rather than preserving their position.
func compressNonEmpty(row []string) []string {
	var out []string
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// extractHeaderTable finds the 2-column key-value table (Docket No, Date,
// Job No, Job Name, Site, Site Address, Site Phone, Site Contact) and
// returns it as a map keyed by a normalized version of the left-hand label
// (spaces and trailing punctuation stripped, lowercased) -- e.g. "Docket
// No .:" becomes key "docketno", "Job Name:" becomes key "jobname".
func extractHeaderTable(tables []docTable) map[string]string {
	header := make(map[string]string)

	for _, table := range tables {
		if table.ColumnCount != 2 {
			continue
		}
		rows := tableToRows(table)
		for _, row := range rows {
			if len(row) < 2 {
				continue
			}
			key := normalizeKey(row[0])
			if key != "" {
				header[key] = row[1]
			}
		}
		return header // assume only one 2-column table on the page
	}

	return header
}

// tableToRows converts a docTable's flat cell list into an ordered 2D slice
// of row values, excluding any header row (cells with Kind == "columnHeader").
func tableToRows(table docTable) [][]string {
	rows := make([][]string, table.RowCount)
	for i := range rows {
		rows[i] = make([]string, table.ColumnCount)
	}

	hasHeader := false
	for _, cell := range table.Cells {
		if cell.Kind == "columnHeader" {
			hasHeader = true
		}
		if cell.RowIndex < table.RowCount && cell.ColumnIndex < table.ColumnCount {
			rows[cell.RowIndex][cell.ColumnIndex] = cell.Content
		}
	}

	startRow := 0
	if hasHeader {
		startRow = 1
	}
	return rows[startRow:]
}

// normalizeKey strips spaces and trailing punctuation (colons, periods) and
// lowercases, so OCR artifacts like "Docket No .:" and a clean "Docket No:"
// both normalize to the same lookup key.
func normalizeKey(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.TrimRight(s, ".:")
	return s
}

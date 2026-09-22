package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"
)

func saveDeltaLink(link string) error {
	return os.WriteFile("deltalink.txt", []byte(link), 0600)
}

func loadDeltaLink() (string, error) {
	data, err := os.ReadFile("deltalink.txt")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type deltaResponse struct {
	Value     []map[string]interface{} `json:"value"`
	NextLink  string                   `json:"@odata.nextLink"`
	DeltaLink string                   `json:"@odata.deltaLink"`
}

func pollForNewMail(ctx context.Context, tokenSource oauth2.TokenSource) {
	for {
		log.Println("polling")
		if err := checkMail(ctx, tokenSource); err != nil {
			log.Println("poll error:", err)
		}
		time.Sleep(1 * time.Minute)
	}
}

func checkMail(ctx context.Context, tokenSource oauth2.TokenSource) error {
	token, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}

	url, err := loadDeltaLink()
	if err != nil {
		//url = "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages/delta"
		url = "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=latest"
	}

	for url != "" {
		log.Println("fetching: ", url)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Prefer", "odata.maxpagesize=999")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("graph returned %d: %s", resp.StatusCode, string(body))
		}

		var result deltaResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return err
		}

		for _, msg := range result.Value {
			subject, _ := msg["subject"].(string)
			hasAttachments, _ := msg["hasAttachments"].(bool)
			id, _ := msg["id"].(string)
			if hasAttachments {
				log.Printf("New Message with attachment %s (%s)\n", subject, id)
				paths, err := downloadAttachments(token.AccessToken, id)
				if err != nil {
					log.Printf("failed to download attachments for %s: %v\n", id, err)
					continue
				}

				for _, path := range paths {
					log.Printf(" -> converting and conduction ocr OCR: %s\n", path)

					docket, err := extractDocketFromPDF(path)
					if err != nil {
						log.Printf("failed to extract docket from %s: %v\n", path, err)
						continue
					}

					log.Printf(" docket %s / job %s \n,", docket.Header["docketno"], docket.Header["jobno"])
					for _, row := range docket.Rows {
						log.Printf("  part row: %v\n", row)
						// TODO: write [docket.Header["docketno"], row[0], row[1], row[2]] to SharePoint
					}

					outputDir := os.Getenv("LOCAL_JOB_FILES_DIR")
					if outputDir == "" {
						outputDir = "./local-job-files"
					}
					if err := addDocketToLocalJobFile(outputDir, docket); err != nil {
						log.Printf("  failed to write job spreadsheet: %v\n", err)
						continue
					}

				}
			}
		}

		if result.NextLink != "" {
			url = result.NextLink
		} else {
			url = ""
			if result.DeltaLink != "" {
				saveDeltaLink(result.DeltaLink)
			}
		}
	}
	return nil
}

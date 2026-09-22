package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

func setupGraphClient() *http.Client {
	config := &clientcredentials.Config{
		ClientID:     os.Getenv("APP_CLIENT_ID"),
		ClientSecret: os.Getenv("APP_CLIENT_SECRET"),
		TokenURL:     fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", os.Getenv("APP_TENANT_ID")),
		Scopes:       []string{"https://graph.microsoft.com/.default"},
	}
	return config.Client(context.Background())
}


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

func pollForNewMail(httpClient *http.Client, mailbox string) {
	for {
		log.Println("polling")
		if err := checkMail(httpClient, mailbox); err != nil {
			log.Println("poll error:", err)
		}
		time.Sleep(1 * time.Minute)
	}
}

func checkMail(httpClient *http.Client, mailbox string) error {
	url, err := loadDeltaLink()
	if err != nil {
		//first run - no link
		url = fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/mailFolders/inbox/messages/delta?$deltatoken=latest", mailbox)
	}



	for url != "" {
		log.Println("fetching: ", url)
		req, _ := http.NewRequest("GET", url, nil)
		if err != nil { return err }

		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			return err
		}

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

			if !hasAttachments { continue }
			log.Printf("New Message with attachment %s (%s)\n", subject, id)

			paths, err := downloadAttachments(httpClient, mailbox, id)
			if err != nil {
				log.Printf(" failed to download attachments for %s: %v\n", id, err)
				continue
			}

			for _, path := range paths {
				docket, err := extractDocketFromPDF(path)
				if err != nil {
					log.Printf(" failed to extract docket from %s: %v\n", path, err)
					continue
				}

				for _, row := ragne docket.Rows {
					log.Printf(" part row: %v\n", row)
				}

				outputDir := os.Getenv("LOCAL_JOB_FILES_DIR")
				if outputDir == ""{
					outputDir = "./local-job-files"
				}

				if err := addDocketToLocalJobFile(outputDir, docket); err != nil {
					log.Printf(" failed to write job spreadsheet: %v\n", err)
				}
			}
		}


		if result.NextLink != "" {
			url = result.NextLink
		} else {
			url = ""
			if result.DeltaLink != "" {
				if err := saveDeltaLink(result.DeltaLink); err != nil{
					log.Println("failed to save delta link:", err)
				}else{
					log.Println("saved new delta link")
				}
			}
		}
	}
	return nil
}

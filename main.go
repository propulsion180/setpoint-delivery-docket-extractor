package main

import (
	"encoding/json"

	//"go/token"
	"log"
	"os"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2"
)

var oauthConfig *oauth2.Config

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, using system environment")
	}

	mailbox := os.Getenv("SHARED_MAILBOX_EMAIL")

	if mailbox == "" {
		log.Fatal("shared mailbox email not set")
	}

	httpClient := *setupGraphClient()
	pollForNewMail(&httpClient, mailbox)
}

func saveToken(token *oauth2.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}

	return os.WriteFile("token.json", data, 0600)
}

func loadToken() (*oauth2.Token, error) {
	data, err := os.ReadFile("token.json")
	if err != nil {
		return nil, err
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}

	return &token, nil
}

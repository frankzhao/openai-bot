package main

import (
	"cloud.google.com/go/storage"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"github.com/frankzhao/openai-go"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	logger "github.com/rs/zerolog/log"
	"github.com/slack-go/slack"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var openaiToken string
var slackToken string
var sendToSlack bool
var openAI *openai.Client
var debug bool
var gcsBucket string

func generateImage(prompt string, cmd *slack.SlashCommand, bucket string) {
	img, err := openAI.GenerateImage(prompt, openai.RESPONSE_FORMAT_BASE64, "256x256", 1)
	if err != nil {
		logger.Error().Msgf("Error requesting image: %v", err)
	}

	// Decode the generated base64 image.
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(img.Data[0].B64Data))

	// Upload to GCS
	ctx := context.Background()
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		logger.Error().Msgf("storage.NewClient: %v", err)
	}
	defer gcs.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Second*50)
	defer cancel()
	object := fmt.Sprintf("dalle_%s.png", uuid.NewString())
	o := gcs.Bucket(bucket).Object(object)
	o = o.If(storage.Conditions{DoesNotExist: true})

	// Upload the image to GCS.
	wc := o.NewWriter(ctx)
	if _, err = io.Copy(wc, reader); err != nil {
		logger.Error().Msgf("io.Copy: %v", err)
	}
	if err := wc.Close(); err != nil {
		logger.Error().Msgf("Writer.Close: %v", err)
	}
	logger.Info().Msgf("Blob %v uploaded.\n", object)

	// Set the GCS image public.
	acl := gcs.Bucket(bucket).Object(object).ACL()
	if err := acl.Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
		logger.Error().Msgf("ACLHandle.Set: %v", err)
	}
	logger.Info().Msgf("Blob %v is now publicly accessible.\n", object)

	publicUrl := fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucket, object)

	// Post message to slack webhook response.
	titleBlock := slack.NewTextBlockObject(slack.PlainTextType, prompt, false, false)
	imageBlock := slack.NewImageBlock(publicUrl, prompt, "image1", titleBlock)
	blocks := slack.Blocks{BlockSet: []slack.Block{imageBlock}}
	msg := slack.WebhookMessage{Blocks: &blocks, ResponseType: slack.ResponseTypeInChannel}
	if sendToSlack {
		err = slack.PostWebhook(cmd.ResponseURL, &msg)
		if err != nil {
			logger.Error().Msgf("Failed to post to slack: %v", err)
		}
	}
}

func completeText(prompt string, cmd *slack.SlashCommand) {
	res, err := openAI.CompleteText(prompt, openai.MODEL_GPT_DAVINCI, rand.Float32(), 256)
	if err != nil {
		logger.Error().Msgf("Error requesting text completion: %v", err)
		return
	}

	// Post message to slack webhook response.
	textBlock := slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf(">%s\n%s", prompt, res.Choices[0].Text), false, false)
	section := slack.NewSectionBlock(textBlock, nil, nil)
	blocks := slack.Blocks{BlockSet: []slack.Block{section}}
	msg := slack.WebhookMessage{Blocks: &blocks, ResponseType: slack.ResponseTypeInChannel}
	if sendToSlack {
		err = slack.PostWebhook(cmd.ResponseURL, &msg)
		if err != nil {
			logger.Error().Msgf("Failed to post to slack: %v", err)
		}
	}
}

func completeCode(prompt string, cmd *slack.SlashCommand) {
	res, err := openAI.CompleteText(prompt, openai.MODEL_CODEX_DAVINCI, rand.Float32(), 256)
	if err != nil {
		logger.Error().Msgf("Error requesting text completion: %v", err)
		return
	}

	// Post message to slack webhook response.
	textBlock := slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf(">%s\n%s", prompt, res.Choices[0].Text), false, false)
	section := slack.NewSectionBlock(textBlock, nil, nil)
	blocks := slack.Blocks{BlockSet: []slack.Block{section}}
	msg := slack.WebhookMessage{Blocks: &blocks, ResponseType: slack.ResponseTypeInChannel}
	if sendToSlack {
		err = slack.PostWebhook(cmd.ResponseURL, &msg)
		if err != nil {
			logger.Error().Msgf("Failed to post to slack: %v", err)
		}
	}
}

func handleSlackCommand(w http.ResponseWriter, req *http.Request) {
	slashCommand, err := slack.SlashCommandParse(req)
	if err != nil {
		logger.Error().Msgf("Error parsing slack command: %v", req)
	}
	cmd := slashCommand.Text
	logger.Info().Msgf("Received slack command: %v", cmd)

	// Non timesheet related commands.
	if strings.HasPrefix(cmd, "dalle") {
		prompt := strings.TrimSpace(strings.TrimPrefix(cmd, "dalle"))
		go generateImage(prompt, &slashCommand, gcsBucket)
	} else if strings.HasPrefix(cmd, "gpt") {
		prompt := strings.TrimSpace(strings.TrimPrefix(cmd, "gpt"))
		go completeText(prompt, &slashCommand)
	} else if strings.HasPrefix(cmd, "code") {
		prompt := strings.TrimSpace(strings.TrimPrefix(cmd, "code"))
		go completeCode(prompt, &slashCommand)
	} else {
		// Unknown command.
		logger.Error().Msgf("Unknown command: '%s'", cmd)
		if sendToSlack {
			w.Write([]byte(fmt.Sprintf("Unknown command: '%s'", cmd)))
		}
	}
}

func main() {
	flag.StringVar(&slackToken, "slackToken", "", "Token for Slack API.")
	flag.BoolVar(&sendToSlack, "sendToSlack", false, "Send notifications to Slack")
	flag.StringVar(&openaiToken, "openAIToken", "", "Token for Slack API.")
	flag.StringVar(&gcsBucket, "gcs", "", "GCS bucket for dalle image retention.")
	flag.BoolVar(&debug, "debug", false, "Debug logging")
	flag.Parse()

	// Environment variables will override flags
	if val, exists := os.LookupEnv("SLACK_TOKEN"); exists {
		slackToken = val
	}
	if val, exists := os.LookupEnv("SEND_TO_SLACK"); exists {
		sendToSlack, _ = strconv.ParseBool(val)
	}
	if val, exists := os.LookupEnv("OPEN_AI_TOKEN"); exists {
		openaiToken = val
	}
	if val, exists := os.LookupEnv("GCS_BUCKET"); exists {
		gcsBucket = val
	}
	if val, exists := os.LookupEnv("BOT_DEBUG"); exists {
		debug, _ = strconv.ParseBool(val)
	}

	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	slack.New(slackToken, slack.OptionDebug(true))
	openAI = openai.New(openaiToken)

	http.HandleFunc("/slack_command", handleSlackCommand)

	serve()
}

func serve() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		logger.Info().Msgf("Defaulting to port %s", port)
	}

	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal(nil)
	}
}

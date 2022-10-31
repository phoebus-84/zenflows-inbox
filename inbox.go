//go:build ignore

package main

import (
	"context"
	_ "embed"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis/v8"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"github.com/rs/cors"
)

type Config struct {
	Redis string
	Port  int
	Host  string
}

type Message struct {
	Sender    string   `json:"sender"`
	Receivers []string `json:"receivers"`
}

type Inbox struct {
	rds *redis.Client
	ctx context.Context
}

//go:embed zenflows-crypto/src/verify_graphql.zen
var VERIFY string

func enableCors(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
}

func (inbox *Inbox) sendHandler(w http.ResponseWriter, r *http.Request) {
	// Setup json response
	w.Header().Set("Content-Type", "application/json")
	enableCors(&w)
	result := map[string]interface{}{
		"success": false,
	}
	defer json.NewEncoder(w).Encode(result)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		result["success"] = false
		result["error"] = "Could not read the body of the request"
		return
	}
	zenroomData := ZenroomData{
		Gql:            b64.StdEncoding.EncodeToString(body),
		EdDSASignature: r.Header.Get("zenflows-sign"),
	}

	// Read a message object, I need the receivers
	var message Message
	err = json.Unmarshal(body, &message)
	if err != nil {
		result["success"] = false
		result["error"] = err.Error()
		return
	}

	if len(message.Receivers) == 0 {
		result["success"] = false
		result["error"] = "No receivers"
		return
	}

	zenroomData.requestPublicKey(message.Sender)
	err = zenroomData.isAuth()
	if err != nil {
		result["success"] = false
		result["error"] = err.Error()
		return
	}

	// For each receiver put the message in the inbox
	count := 0
	for i := 0; i < len(message.Receivers); i++ {
		err := inbox.rds.SAdd(inbox.ctx, message.Receivers[i], body).Err()
		log.Printf("Added message for: %s", message.Receivers[i])
		if err == nil {
			count = count + 1
		}
	}
	result["success"] = true
	result["count"] = count
	return
}

type ReadMessages struct {
	RequestId int    `json:"request_id"`
	Receiver  string `json:"receiver"`
}

func (inbox *Inbox) readHandler(w http.ResponseWriter, r *http.Request) {
	// Setup json response
	w.Header().Set("Content-Type", "application/json")
	enableCors(&w)
	result := map[string]interface{}{
		"success": false,
	}
	defer json.NewEncoder(w).Encode(result)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		result["success"] = false
		result["error"] = err.Error()
		return
	}

	// Verify signature request
	zenroomData := ZenroomData{
		Gql:            b64.StdEncoding.EncodeToString(body),
		EdDSASignature: r.Header.Get("zenflows-sign"),
	}
	var readMessage ReadMessages
	err = json.Unmarshal(body, &readMessage)
	if err != nil {
		result["success"] = false
		result["error"] = err.Error()
		return
	}
	zenroomData.requestPublicKey(readMessage.Receiver)
	err = zenroomData.isAuth()
	if err != nil {
		result["success"] = false
		result["error"] = err.Error()
		return
	}
	pipe := inbox.rds.Pipeline()

	// Read from redis and delete the messages
	rdsMessages := pipe.SMembers(inbox.ctx, readMessage.Receiver)
	pipe.Del(inbox.ctx, readMessage.Receiver)

	_, err = pipe.Exec(inbox.ctx)
	if err != nil {
		result["success"] = false
		result["error"] = err.Error()
		return
	}
	resultMessages := rdsMessages.Val()
	var messages []map[string]string
	for i := 0; i < len(resultMessages); i++ {
		var message map[string]string
		json.Unmarshal([]byte(resultMessages[i]), &message)
		messages = append(messages, message)
	}

	result["success"] = true
	result["request_id"] = readMessage.RequestId
	result["messages"] = messages
	return
}

func loadEnvConfig() Config {
	port, _ := strconv.Atoi(os.Getenv("PORT"))
	return Config{
		Host:  os.Getenv("HOST"),
		Redis: os.Getenv("REDIS"),
		Port:  port,
	}
}

func main() {
	config := loadEnvConfig()

	inbox := &Inbox{rds: redis.NewClient(&redis.Options{
		Addr:     config.Redis,
		Password: "",
		DB:       0,
	}), ctx: context.Background()}

	mux := http.NewServeMux()
	mux.HandleFunc("/send", inbox.sendHandler)
	mux.HandleFunc("/read", inbox.readHandler)

	c := cors.New(cors.Options{
		AllowOriginFunc: func(origin string) bool {return true},
		AllowCredentials: true,
		AllowedHeaders: []string{"Zenflows-Sign"},
	})


	handler := c.Handler(mux)
	host := fmt.Sprintf("%s:%d", config.Host, config.Port)
	fmt.Printf("Starting service on %s\n", host)
	log.Fatal(http.ListenAndServe(host, handler))
}

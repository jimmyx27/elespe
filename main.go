package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Message struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Verse   string `json:"verse,omitempty"`
	Number  int    `json:"number,omitempty"`
	Total   int    `json:"total,omitempty"`
}

func LoadVerses(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var verses []string
	if err := json.Unmarshal(data, &verses); err != nil {
		return nil, err
	}
	return verses, nil
}

func handleWebSocket(w http.ResponseWriter, r *http.Request, verses []string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocker upgrade error: %v", err)
		return
	}
	defer conn.Close()
	log.Printf("Client connected: %s", r.RemoteAddr)

	conn.WriteJSON(Message{Type: "verse", Content: "Praise the sun! \\\\[T]//"})

	for i, verse := range verses {
		conn.WriteJSON(Message{
			Type:    "verse",
			Content: fmt.Sprintf("Verse: %d%d", i+1, len(verses)),
			Verse:   verse,
			Number:  i + 1,
			Total:   len(verses),
		})

		for {
			var msg Message
			if err := conn.ReadJSON(&msg); err != nil {
				log.Printf("Read error: %v", err)
				return
			}
			userInput := strings.TrimSpace(msg.Content)
			if strings.ToLower(userInput) == "quit" {
				conn.WriteJSON(Message{Type: "response", Content: "GoodBye"})
				return
			}
			if userInput == verse {
				conn.WriteJSON(Message{Type: "correct", Content: "correct"})
				break
			} else {
				conn.WriteJSON(Message{Type: "wrong", Content: "wrong"})
			}
		}
	}
	conn.WriteJSON(Message{Type: "complete", Content: "complete"})
}

func main() {
	verses, err := LoadVerses("verses.json")
	if err != nil {
		log.Fatalf("Error loading verses: %v", err)
	}
	if len(verses) == 0 {
		log.Fatal("No verses found")
	}
	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/wss", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r, verses)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server start on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

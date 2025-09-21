package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
)

type Stats struct {
	StartTime      time.Time `json:"-"`
	CharsTyped     int       `json:"charsTyped"`
	Mistakes       int       `json:"mistakes"`
	CorrectEntries int       `json:"correct"`
	WPM            int       `json:"wpm"`
}

type WSMessage struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Stats   *Stats `json:"stats,omitempty"`
}

func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:") {
		return true
	}
	return origin == "https://elespe.onrender.com"
}

var upgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

type Data struct {
	Metadata Metadata `json:"metadata"`
	Verses   []Verse  `json:"verses"`
}

type Metadata struct {
	Name               string `json:"name"`
	ShortName          string `json:"shortname"`
	Module             string `json:"module"`
	Year               string `json:"year"`
	Publisher          string `json:"publisher"`
	Owner              string `json:"owner"`
	Description        string `json:"description"`
	Lang               string `json:"lang"`
	LangShort          string `json:"lang_short"`
	Copyright          int    `json:"copyright"`
	CopyrightStatement string `json:"copyright_statement"`
	URL                string `json:"url"`
	CitationLimit      int    `json:"citation_limit"`
	Restrict           int    `json:"restrict"`
	Italics            int    `json:"italics"`
	Strongs            int    `json:"strongs"`
	RedLetter          int    `json:"red_letter"`
	Paragraph          int    `json:"paragraph"`
	Official           int    `json:"official"`
	Research           int    `json:"research"`
	ModuleVersion      string `json:"module_version"`
}

type Verse struct {
	BookName string `json:"book_name"`
	Book     int    `json:"book"`
	Chapter  int    `json:"chapter"`
	Verse    int    `json:"verse"`
	Text     string `json:"text"`
}

type Message struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Verse   string `json:"verse,omitempty"`
	Number  int    `json:"number,omitempty"`
	Total   int    `json:"total,omitempty"`
}

var bible Data

func loadData() error {
	bytes, err := os.ReadFile("kjv.json")
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, &bible)
}

func cleanString(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}

		if r == '\u00B6' || r == '\u2029' || r == '\u2028' {
			return -1
		}
		return r
	}, s)
}

//func LoadVerses(file string) ([]string, error) {
//	data, err := os.ReadFile(file)
//	if err != nil {
//		return nil, err
//	}
//	var verses []string
//	if err := json.Unmarshal(data, &verses); err != nil {
//		return nil, err
//	}
//	return verses, nil
//}

func ExtractVerseTexts(bible Data) []string {
	verses := make([]string, 0, len(bible.Verses))
	for _, v := range bible.Verses {
		verses = append(verses, cleanString(v.Text))
	}
	return verses
}

func handleWebSocket(w http.ResponseWriter, r *http.Request, verses []string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocker upgrade error: %v", err)
		return
	}
	defer conn.Close()
	log.Printf("Client connected: %s", r.RemoteAddr)

	stats := &Stats{
		StartTime: time.Now(),
	}
	if err := conn.WriteJSON(Message{Type: "verse", Content: "Praise the sun! \\\\[T]//"}); err != nil {
		log.Printf("Write error: %v", err)
		return
	}

	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			log.Println("read error:", err)
			return
		}
		stats.CharsTyped += len(msg)

		elapsed := time.Since(stats.StartTime).Minutes()
		if elapsed > 0 {
			stats.WPM = int(float64(stats.CharsTyped) / 5 / elapsed)
		}

		response := WSMessage{
			Type:    "response",
			Content: string(msg),
			Stats:   stats,
		}

		if err := conn.WriteJSON(response); err != nil {
			log.Println("write error:", err)
			return
		}
	}

	for i, verse := range verses {
		conn.WriteJSON(Message{
			Type:    "verse",
			Content: fmt.Sprintf("Verse: %d/%d", i+1, len(verses)),
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
			userInput := cleanString(strings.TrimSpace(msg.Content))
			cverse := cleanString(strings.TrimSpace(verse))
			if strings.ToLower(userInput) == "quit" {
				conn.WriteJSON(Message{Type: "response", Content: "GoodBye"})
				return
			}
			if userInput == cverse {
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
	//verses, err := LoadVerses("verses.json")
	//if err != nil {
	//	log.Fatalf("Error loading verses: %v", err)
	//}
	if err := loadData(); err != nil {
		log.Fatal(err)
	}
	verses := ExtractVerseTexts(bible)
	//if len(verses) == 0 {
	//	log.Fatal("No verses found")
	//}
	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
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

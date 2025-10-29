// TODO(jim) The wpm is busted and the progress is only saving chars typed and mistakes. It should save the current verse. Progress bar is also not moving. Also want bookname.
// TODO(jim) dynamic error notification
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
)

var sessions = make(map[string]*PersistentStats)
var sessMu sync.RWMutex

type PersistentStats struct {
	CurrentVerse   int `json:"currentVerse"`
	TotalVerses    int `json:"totalVerses"`
	CorrectEntries int `json:"correct"`
	Mistakes       int `json:"mistakes"`
}

type RuntimeStats struct {
	StartTime    time.Time `json:"startTime"`
	CharsTyped   int       `json:"charsTyped"`
	CorrectChars int       `json:"correctChars"`
	WPM          int       `json:"wpm"`
	Started      bool      `json:"started"`
	LastTypedAt  time.Time `json:"lastTypedAt"`
}

type Stats struct {
	PersistentStats
	RuntimeStats
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
	Verse   Verse  `json:"verse,omitempty"`
	Number  int    `json:"number,omitempty"`
	Total   int    `json:"total,omitempty"`
	Stats   *Stats `json:"stats,omitempty"`
}

var bible Data

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

func saveSessions() {
	sessMu.RLock()
	defer sessMu.RUnlock()
	bytes, _ := json.MarshalIndent(sessions, "", " ")
	os.WriteFile("sessions.json", bytes, 0644)
}

func loadSessions() {
	data, err := os.ReadFile("sessions.json")
	if err != nil {
		return
	}
	var saved map[string]*PersistentStats
	if err := json.Unmarshal(data, &saved); err != nil {
		log.Printf("sessions corrupted, resetting: %v", err)
		return
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	sessions = saved
}

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

func ExtractVerseTexts(bible Data) []Verse {
	verses := make([]Verse, 0, len(bible.Verses))
	for _, v := range bible.Verses {
		verses = append(verses, Verse{
			BookName: v.BookName,
			Book:     v.Book,
			Chapter:  v.Chapter,
			Verse:    v.Verse,
			Text:     cleanString(v.Text),
		})
	}
	return verses
}

func sendStats(conn *websocket.Conn, stats *Stats) {
	conn.WriteJSON(Message{
		Type:  "stats",
		Stats: stats,
	})
}

func handleWebSocket(w http.ResponseWriter, r *http.Request, verses []Verse) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocker upgrade error: %v", err)
		return
	}
	defer conn.Close()
	log.Printf("Client connected: %s", r.RemoteAddr)
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		uid = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	sessMu.Lock()
	stats, ok := sessions[uid]
	if !ok {
		stats = &Stats{
			StartTime:    time.Now(),
			CurrentVerse: 0,
			TotalVerses:  len(verses),
		}
		sessions[uid] = stats
	}
	sessMu.Unlock()
	conn.WriteJSON(Message{Type: "verse", Content: "Praise the sun! \\\\[T]//", Stats: stats})
	for i := stats.CurrentVerse; i < len(verses); i++ {
		verse := verses[i]
		conn.WriteJSON(Message{
			Type:    "verse",
			Content: verse.Text, //fmt.Sprintf("%s %d:%d (%d/%d)", verse.Book, verse.Chapter, verse.Verse, i+1, len(verses)),
			Verse: Verse{
				BookName: verse.BookName,
				Book:     verse.Book,
				Chapter:  verse.Chapter,
				Verse:    verse.Verse,
			},
			Number: i + 1,
			Total:  len(verses),
			Stats:  stats,
		})

		for {
			var msg Message
			if err := conn.ReadJSON(&msg); err != nil {
				log.Printf("Read error: %v", err)
				return
			}
			userInput := cleanString(strings.TrimSpace(msg.Content))
			correctText := cleanString(strings.TrimSpace(verse.Text))
			if !stats.Started && len(userInput) > 0 {
				stats.Started = true
				stats.StartTime = time.Now()
			}
			stats.CharsTyped += len(userInput)
			if strings.ToLower(userInput) == "quit" {
				conn.WriteJSON(Message{Type: "response", Content: "GoodBye"})
				return
			}
			if userInput == correctText {
				stats.CorrectChars += len(correctText)
				stats.CorrectEntries++
				stats.CurrentVerse = i + 1
				elapsed := time.Since(stats.StartTime).Seconds()
				if elapsed > 1 {
					stats.WPM = int(float64(stats.CorrectChars) / 5 / elapsed)
				}
				conn.WriteJSON(Message{Type: "correct", Content: "correct"})
				sendStats(conn, stats)
				saveSessions()
				break
			} else {
				stats.Mistakes++
				conn.WriteJSON(Message{Type: "wrong", Content: "wrong"})
				sendStats(conn, stats)
				saveSessions()
			}
			if time.Since(stats.LastTypedAt) > time.Second*2 {
				stats.StartTime = time.Now()
			}
			stats.LastTypedAt = time.Now()
		}
	}
	conn.WriteJSON(Message{Type: "complete", Content: "complete", Stats: stats})
	saveSessions()
}

func main() {
	loadSessions()
	defer saveSessions()
	if err := loadData(); err != nil {
		log.Fatal(err)
	}
	verses := ExtractVerseTexts(bible)
	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r, verses)
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("from: %s", r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
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

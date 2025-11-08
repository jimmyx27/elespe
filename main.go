package main

import (
	"context"
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
	"github.com/jackc/pgx/v5/pgxpool"
)

var sessions = make(map[string]*PersistentStats)
var sessMu sync.RWMutex

type BookProgress struct {
	CurrentVerse   int `json:"currentVerse"`
	TotalVerses    int `json:"totalVerses"`
	CorrectEntries int `json:"correct"`
	Mistakes       int `json:"mistakes"`
}

type PersistentStats struct {
	Books map[string]*BookProgress `json:"books"`
}

type RuntimeStats struct {
	StartTime    time.Time `json:"startTime"`
	CharsTyped   int       `json:"charsTyped"`
	CorrectChars int       `json:"correctChars"`
	WPM          int       `json:"wpm"`
	Started      bool      `json:"started"`
}

type Stats struct {
	PersistentStats
	RuntimeStats
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

var bible struct {
	Verses []Verse `json:"verses"`
}
var versesByBook map[string][]Verse

// ---- utils ----

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

func groupVersesByBook(verses []Verse) {
	versesByBook = make(map[string][]Verse)
	for _, v := range verses {
		versesByBook[v.BookName] = append(versesByBook[v.BookName], v)
	}
}

func getBookProgress(pstats *PersistentStats, book string, total int) *BookProgress {
	if pstats.Books == nil {
		pstats.Books = make(map[string]*BookProgress)
	}
	bp, ok := pstats.Books[book]
	if !ok {
		bp = &BookProgress{CurrentVerse: 0, TotalVerses: total}
		pstats.Books[book] = bp
	}
	return bp
}

// ---- persistence ----

func saveSessions() {
	sessMu.RLock()
	defer sessMu.RUnlock()
	b, _ := json.MarshalIndent(sessions, "", " ")
	os.WriteFile("sessions.json", b, 0644)
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
	sessions = saved
	sessMu.Unlock()
}

// ---- websocket ----

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}
	defer conn.Close()

	uid := r.URL.Query().Get("uid")
	if uid == "" {
		uid = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	// load or create session
	sessMu.Lock()
	pstats, ok := sessions[uid]
	if !ok {
		pstats = &PersistentStats{Books: make(map[string]*BookProgress)}
		sessions[uid] = pstats
	}
	stats := &Stats{PersistentStats: *pstats, RuntimeStats: RuntimeStats{StartTime: time.Now()}}
	sessMu.Unlock()

	// send book list
	books := make([]string, 0, len(versesByBook))
	for b := range versesByBook {
		books = append(books, b)
	}
	conn.WriteJSON(map[string]any{"type": "books", "books": books})

	// track selected book
	var selectedBook string
	var bookProgress *BookProgress
	var bookVerses []Verse
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			log.Println("Read error:", err)
			return
		}

		switch msg.Type {
		case "select_book":
			selectedBook = msg.Content
			bookVerses = versesByBook[selectedBook]
			bookProgress = getBookProgress(pstats, selectedBook, len(bookVerses))

			// send first verse in this book
			i := bookProgress.CurrentVerse
			if i < len(bookVerses) {
				v := bookVerses[i]
				conn.WriteJSON(Message{
					Type:    "verse",
					Content: v.Text,
					Verse:   v,
					Number:  i + 1,
					Total:   len(bookVerses),
					Stats:   stats,
				})
			}

		case "content":
			if selectedBook == "" || bookProgress == nil {
				continue
			}
			i := bookProgress.CurrentVerse
			if i >= len(bookVerses) {
				continue
			}
			v := bookVerses[i]
			user := cleanString(strings.TrimSpace(msg.Content))
			correct := cleanString(strings.TrimSpace(v.Text))

			if !stats.Started && len(user) > 0 {
				stats.Started = true
				stats.StartTime = time.Now()
			}

			if strings.ToLower(user) == "quit" {
				conn.WriteJSON(Message{Type: "response", Content: "Goodbye"})
				return
			}

			if user == correct {
				stats.CorrectChars += len(correct)
				stats.CharsTyped += len(user)
				bookProgress.CurrentVerse++
				bookProgress.CorrectEntries++
				elapsed := time.Since(stats.StartTime).Minutes()
				if elapsed > 0 {
					stats.WPM = int(float64(stats.CorrectChars) / 5 / elapsed)
				}
				conn.WriteJSON(Message{Type: "correct", Content: "correct"})
				conn.WriteJSON(Message{Type: "stats", Stats: stats})
				saveSessions()

				// send next verse
				if bookProgress.CurrentVerse < len(bookVerses) {
					next := bookVerses[bookProgress.CurrentVerse]
					conn.WriteJSON(Message{
						Type:    "verse",
						Content: next.Text,
						Verse:   next,
						Number:  bookProgress.CurrentVerse + 1,
						Total:   len(bookVerses),
						Stats:   stats,
					})
				} else {
					conn.WriteJSON(Message{Type: "complete", Content: "All done!", Stats: stats})
				}

			} else {
				bookProgress.Mistakes++
				conn.WriteJSON(Message{Type: "wrong", Content: "wrong"})
				conn.WriteJSON(Message{Type: "stats", Stats: stats})
				saveSessions()
			}
		}
	}
}

// ---- main ----

func main() {
	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("Failed to connect to the database: %v", err)
	}
	config.MaxConns = 2
	config.MinConns = 0
	config.MaxConnLifetime = time.Minute * 5
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	var version string
	if err := pool.QueryRow(context.Background(), "SELECT version()").Scan(&version); err != nil {
		log.Fatalf("Query failed %v", err)
	}
	log.Println("Connected to:", version)
	loadSessions()
	defer saveSessions()

	// load bible
	data, err := os.ReadFile("kjv.json")
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(data, &bible); err != nil {
		log.Fatal(err)
	}

	verses := make([]Verse, 0, len(bible.Verses))
	for _, v := range bible.Verses {
		v.Text = cleanString(v.Text)
		verses = append(verses, v)
	}
	groupVersesByBook(verses)

	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

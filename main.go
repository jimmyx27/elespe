package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

type BookProgress struct {
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
}

type Stats struct {
	BookProgress
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

// ---- Database functions ----

func initDB() error {
	ctx := context.Background()

	// Create tables
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		uid TEXT PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_active TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS book_progress (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		book_name TEXT NOT NULL,
		current_verse INT DEFAULT 0,
		total_verses INT NOT NULL,
		correct_entries INT DEFAULT 0,
		mistakes INT DEFAULT 0,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(uid, book_name)
	);

	CREATE INDEX IF NOT EXISTS idx_book_progress_uid ON book_progress(uid);
	CREATE INDEX IF NOT EXISTS idx_book_progress_book ON book_progress(uid, book_name);

	CREATE TABLE IF NOT EXISTS typing_sessions (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		book_name TEXT NOT NULL,
		started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		ended_at TIMESTAMP,
		chars_typed INT DEFAULT 0,
		correct_chars INT DEFAULT 0,
		wpm INT DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_typing_sessions_uid ON typing_sessions(uid);
	`

	_, err := pool.Exec(ctx, schema)
	return err
}

func ensureUser(ctx context.Context, uid string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO users (uid, last_active) 
		VALUES ($1, CURRENT_TIMESTAMP)
		ON CONFLICT (uid) DO UPDATE SET last_active = CURRENT_TIMESTAMP
	`, uid)
	return err
}

func getBookProgress(ctx context.Context, uid, bookName string, totalVerses int) (*BookProgress, error) {
	var bp BookProgress

	err := pool.QueryRow(ctx, `
		SELECT current_verse, total_verses, correct_entries, mistakes
		FROM book_progress
		WHERE uid = $1 AND book_name = $2
	`, uid, bookName).Scan(&bp.CurrentVerse, &bp.TotalVerses, &bp.CorrectEntries, &bp.Mistakes)

	if err != nil {
		// No progress found, create new entry
		_, err = pool.Exec(ctx, `
			INSERT INTO book_progress (uid, book_name, total_verses)
			VALUES ($1, $2, $3)
		`, uid, bookName, totalVerses)

		if err != nil {
			return nil, err
		}

		return &BookProgress{
			CurrentVerse:   0,
			TotalVerses:    totalVerses,
			CorrectEntries: 0,
			Mistakes:       0,
		}, nil
	}

	return &bp, nil
}

func updateBookProgress(ctx context.Context, uid, bookName string, bp *BookProgress) error {
	_, err := pool.Exec(ctx, `
		UPDATE book_progress
		SET current_verse = $3,
			correct_entries = $4,
			mistakes = $5,
			updated_at = CURRENT_TIMESTAMP
		WHERE uid = $1 AND book_name = $2
	`, uid, bookName, bp.CurrentVerse, bp.CorrectEntries, bp.Mistakes)

	return err
}

func createTypingSession(ctx context.Context, uid, bookName string) (int, error) {
	var sessionID int
	err := pool.QueryRow(ctx, `
		INSERT INTO typing_sessions (uid, book_name)
		VALUES ($1, $2)
		RETURNING id
	`, uid, bookName).Scan(&sessionID)

	return sessionID, err
}

func updateTypingSession(ctx context.Context, sessionID int, stats *RuntimeStats) error {
	_, err := pool.Exec(ctx, `
		UPDATE typing_sessions
		SET chars_typed = $2,
			correct_chars = $3,
			wpm = $4,
			ended_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`, sessionID, stats.CharsTyped, stats.CorrectChars, stats.WPM)

	return err
}

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
		uid = fmt.Sprintf("user_%d", time.Now().UnixNano())
	}

	ctx := context.Background()

	// Ensure user exists
	if err := ensureUser(ctx, uid); err != nil {
		log.Printf("Error ensuring user: %v", err)
		return
	}

	// Initialize runtime stats
	runtimeStats := RuntimeStats{StartTime: time.Now()}
	var sessionID int

	// Send book list
	books := make([]string, 0, len(versesByBook))
	for b := range versesByBook {
		books = append(books, b)
	}
	conn.WriteJSON(map[string]any{"type": "books", "books": books})

	// Track selected book
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

			// Load progress from database
			bookProgress, err = getBookProgress(ctx, uid, selectedBook, len(bookVerses))
			if err != nil {
				log.Printf("Error loading progress: %v", err)
				continue
			}

			// Create new typing session
			sessionID, err = createTypingSession(ctx, uid, selectedBook)
			if err != nil {
				log.Printf("Error creating session: %v", err)
			}

			// Reset runtime stats for new session
			runtimeStats = RuntimeStats{StartTime: time.Now()}

			// Send first verse
			i := bookProgress.CurrentVerse
			if i < len(bookVerses) {
				v := bookVerses[i]
				stats := &Stats{
					BookProgress: *bookProgress,
					RuntimeStats: runtimeStats,
				}
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

			if !runtimeStats.Started && len(user) > 0 {
				runtimeStats.Started = true
				runtimeStats.StartTime = time.Now()
			}

			if strings.ToLower(user) == "quit" {
				conn.WriteJSON(Message{Type: "response", Content: "Goodbye"})
				return
			}

			if user == correct {
				runtimeStats.CorrectChars += len(correct)
				runtimeStats.CharsTyped += len(user)
				bookProgress.CurrentVerse++
				bookProgress.CorrectEntries++

				elapsed := time.Since(runtimeStats.StartTime).Minutes()
				if elapsed > 0 {
					runtimeStats.WPM = int(float64(runtimeStats.CorrectChars) / 5 / elapsed)
				}

				// Update database
				if err := updateBookProgress(ctx, uid, selectedBook, bookProgress); err != nil {
					log.Printf("Error updating progress: %v", err)
				}

				if sessionID > 0 {
					if err := updateTypingSession(ctx, sessionID, &runtimeStats); err != nil {
						log.Printf("Error updating session: %v", err)
					}
				}

				stats := &Stats{
					BookProgress: *bookProgress,
					RuntimeStats: runtimeStats,
				}

				conn.WriteJSON(Message{Type: "correct", Content: "correct"})
				conn.WriteJSON(Message{Type: "stats", Stats: stats})

				// Send next verse
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
				runtimeStats.CharsTyped += len(user)

				// Update database
				if err := updateBookProgress(ctx, uid, selectedBook, bookProgress); err != nil {
					log.Printf("Error updating progress: %v", err)
				}

				if sessionID > 0 {
					if err := updateTypingSession(ctx, sessionID, &runtimeStats); err != nil {
						log.Printf("Error updating session: %v", err)
					}
				}

				stats := &Stats{
					BookProgress: *bookProgress,
					RuntimeStats: runtimeStats,
				}

				conn.WriteJSON(Message{Type: "wrong", Content: "wrong"})
				conn.WriteJSON(Message{Type: "stats", Stats: stats})
			}
		}
	}
}

// ---- main ----

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("Failed to parse database config: %v", err)
	}

	// Supabase-optimized connection pool settings
	config.ConnConfig.StatementCacheCapacity = 0
	config.MaxConns = 5 // Supabase free tier has connection limits
	config.MinConns = 0
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = time.Minute * 30
	config.HealthCheckPeriod = time.Minute

	pool, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	var version string
	if err := pool.QueryRow(context.Background(), "SELECT version()").Scan(&version); err != nil {
		log.Fatalf("Query failed: %v", err)
	}
	log.Println("Connected to:", version)

	// Initialize database schema
	if err := initDB(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	log.Println("Database initialized")

	// Load bible
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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	Type       string         `json:"type"`
	Content    string         `json:"content"`
	Verse      Verse          `json:"verse,omitempty"`
	Number     int            `json:"number,omitempty"`
	Total      int            `json:"total,omitempty"`
	Stats      *Stats         `json:"stats,omitempty"`
	Books      []string       `json:"books,omitempty"`
	Progress   map[string]int `json:"progress,omitempty"`
	Favorites  []Verse        `json:"favorites,omitempty"`
	IsFavorite bool           `json:"isFavorite,omitempty"`
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

	CREATE TABLE IF NOT EXISTS favorite_verses (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		book_name TEXT NOT NULL,
		book INT NOT NULL,
		chapter INT NOT NULL,
		verse INT NOT NULL,
		text TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(uid, book_name, chapter, verse)
	);

	CREATE INDEX IF NOT EXISTS idx_favorite_verses_uid ON favorite_verses(uid);
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

func getAllBookProgress(ctx context.Context, uid string) (map[string]int, error) {
	rows, err := pool.Query(ctx, `
		SELECT book_name, current_verse, total_verses
		FROM book_progress
		WHERE uid = $1
	`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	progress := make(map[string]int)
	for rows.Next() {
		var bookName string
		var current, total int
		if err := rows.Scan(&bookName, &current, &total); err != nil {
			continue
		}
		if total > 0 {
			progress[bookName] = (current * 100) / total
		}
	}

	return progress, nil
}

func toggleFavorite(ctx context.Context, uid string, v Verse) (bool, error) {
	// Check if already favorited
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM favorite_verses 
			WHERE uid = $1 AND book_name = $2 AND chapter = $3 AND verse = $4
		)
	`, uid, v.BookName, v.Chapter, v.Verse).Scan(&exists)

	if err != nil {
		return false, err
	}

	if exists {
		// Remove from favorites
		_, err = pool.Exec(ctx, `
			DELETE FROM favorite_verses 
			WHERE uid = $1 AND book_name = $2 AND chapter = $3 AND verse = $4
		`, uid, v.BookName, v.Chapter, v.Verse)
		return false, err
	} else {
		// Add to favorites
		_, err = pool.Exec(ctx, `
			INSERT INTO favorite_verses (uid, book_name, book, chapter, verse, text)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, uid, v.BookName, v.Book, v.Chapter, v.Verse, v.Text)
		return true, err
	}
}

func getFavorites(ctx context.Context, uid string) ([]Verse, error) {
	rows, err := pool.Query(ctx, `
		SELECT book_name, book, chapter, verse, text
		FROM favorite_verses
		WHERE uid = $1
		ORDER BY created_at DESC
	`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var favorites []Verse
	for rows.Next() {
		var v Verse
		if err := rows.Scan(&v.BookName, &v.Book, &v.Chapter, &v.Verse, &v.Text); err != nil {
			continue
		}
		favorites = append(favorites, v)
	}

	return favorites, nil
}

func isFavorite(ctx context.Context, uid string, v Verse) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM favorite_verses 
			WHERE uid = $1 AND book_name = $2 AND chapter = $3 AND verse = $4
		)
	`, uid, v.BookName, v.Chapter, v.Verse).Scan(&exists)

	return exists, err
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
	var currentVerseIndex int // Track which verse we're on (can jump around)

	// Get all book progress for this user
	allProgress, err := getAllBookProgress(ctx, uid)
	if err != nil {
		log.Printf("Error loading all progress: %v", err)
		allProgress = make(map[string]int)
	}

	// Send book list with progress
	books := make([]string, 0, len(versesByBook))
	for b := range versesByBook {
		books = append(books, b)
	}

	// Send as single unified message
	type BooksMessage struct {
		Type     string         `json:"type"`
		Books    []string       `json:"books"`
		Progress map[string]int `json:"progress"`
	}

	conn.WriteJSON(BooksMessage{
		Type:     "books",
		Books:    books,
		Progress: allProgress,
	})

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
		case "get_favorites":
			favorites, err := getFavorites(ctx, uid)
			if err != nil {
				log.Printf("Error getting favorites: %v", err)
				conn.WriteJSON(Message{Type: "error", Content: "Failed to load favorites"})
				continue
			}
			conn.WriteJSON(Message{Type: "favorites", Favorites: favorites})

		case "toggle_favorite":
			if selectedBook == "" || currentVerseIndex >= len(bookVerses) {
				continue
			}
			v := bookVerses[currentVerseIndex]
			isFav, err := toggleFavorite(ctx, uid, v)
			if err != nil {
				log.Printf("Error toggling favorite: %v", err)
				conn.WriteJSON(Message{Type: "error", Content: "Failed to toggle favorite"})
				continue
			}
			conn.WriteJSON(Message{Type: "favorite_toggled", IsFavorite: isFav})

		case "jump_to_verse":
			if selectedBook == "" || bookProgress == nil {
				continue
			}

			// Parse verse number from content
			var verseNum int
			fmt.Sscanf(msg.Content, "%d", &verseNum)

			if verseNum < 1 || verseNum > len(bookVerses) {
				conn.WriteJSON(Message{Type: "error", Content: "Invalid verse number"})
				continue
			}

			currentVerseIndex = verseNum - 1
			v := bookVerses[currentVerseIndex]

			// Check if favorited
			isFav, _ := isFavorite(ctx, uid, v)

			stats := &Stats{
				BookProgress: *bookProgress,
				RuntimeStats: runtimeStats,
			}

			conn.WriteJSON(Message{
				Type:       "verse",
				Content:    v.Text,
				Verse:      v,
				Number:     currentVerseIndex + 1,
				Total:      len(bookVerses),
				Stats:      stats,
				IsFavorite: isFav,
			})

		case "select_book":
			selectedBook = msg.Content
			bookVerses, exists := versesByBook[selectedBook]
			if !exists {
				conn.WriteJSON(Message{Type: "error", Content: "Book not found"})
				continue
			}

			// Load progress from database
			bookProgress, err = getBookProgress(ctx, uid, selectedBook, len(bookVerses))
			if err != nil {
				log.Printf("Error loading progress: %v", err)
				conn.WriteJSON(Message{Type: "error", Content: "Failed to load progress"})
				continue
			}

			// Create new typing session
			sessionID, err = createTypingSession(ctx, uid, selectedBook)
			if err != nil {
				log.Printf("Error creating session: %v", err)
			}

			// Reset runtime stats for new session
			runtimeStats = RuntimeStats{StartTime: time.Now()}

			// Send first verse (or current progress verse)
			currentVerseIndex = bookProgress.CurrentVerse
			if currentVerseIndex < len(bookVerses) {
				v := bookVerses[currentVerseIndex]

				// Check if favorited
				isFav, _ := isFavorite(ctx, uid, v)

				stats := &Stats{
					BookProgress: *bookProgress,
					RuntimeStats: runtimeStats,
				}
				conn.WriteJSON(Message{
					Type:       "verse",
					Content:    v.Text,
					Verse:      v,
					Number:     currentVerseIndex + 1,
					Total:      len(bookVerses),
					Stats:      stats,
					IsFavorite: isFav,
				})
			} else {
				// Book is complete
				stats := &Stats{
					BookProgress: *bookProgress,
					RuntimeStats: runtimeStats,
				}
				conn.WriteJSON(Message{
					Type:    "complete",
					Content: fmt.Sprintf("You've completed %s! Select a verse to practice or choose another book.", selectedBook),
					Stats:   stats,
				})
			}

		case "content":
			if selectedBook == "" || bookProgress == nil {
				continue
			}

			if currentVerseIndex >= len(bookVerses) {
				continue
			}

			v := bookVerses[currentVerseIndex]
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
	// Validate required files exist
	if _, err := os.Stat("kjv.json"); os.IsNotExist(err) {
		log.Fatal("kjv.json not found - please ensure bible data file exists")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("Failed to parse database config: %v", err)
	}

	// Supabase-optimized connection pool settings
	config.MaxConns = 5 // Supabase free tier has connection limits
	config.MinConns = 1
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
		// Check database connection
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("database unavailable"))
			return
		}

		w.Write([]byte("ok"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("Server starting on port %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	pool.Close()
	log.Println("Server exited")
}

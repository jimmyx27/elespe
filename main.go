package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed index.html logo.svg kjv.json asv.json bbe.json esv.json niv.json nlt.json hcsb.json book_guides.json
var appFS embed.FS

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// publicFS serves embedded frontend assets but keeps the raw JSON datasets out of the public HTTP route.
type publicFS struct {
	fs.FS
}

func (f publicFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".json") {
		return nil, fs.ErrNotExist
	}
	return f.FS.Open(name)
}

// safeConn wraps websocket.Conn with a mutex to guarantee thread-safe writes.
type safeConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *safeConn) WriteJSON(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return s.conn.WriteJSON(v)
}

func (s *safeConn) WritePing() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
}

func (s *safeConn) Close() error {
	return s.conn.Close()
}

// ---- Data Models ----

type Verse struct {
	BookName string `json:"book_name"`
	Book     int    `json:"book"`
	Chapter  int    `json:"chapter"`
	Verse    int    `json:"verse"`
	Text     string `json:"text"`
}

type BookProgress struct {
	CurrentVerse    int        `json:"currentVerse"`
	TotalVerses     int        `json:"totalVerses"`
	CorrectEntries  int        `json:"correct"`
	Mistakes        int        `json:"mistakes"`
	IntroCompleted  bool       `json:"introCompleted"`
	ReviewCompleted bool       `json:"reviewCompleted"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
}

type RuntimeStats struct {
	StartTime      time.Time `json:"startTime"`
	VerseStartTime time.Time `json:"verseStartTime"`
	CharsTyped     int       `json:"charsTyped"`
	CorrectChars   int       `json:"correctChars"`
	WPM            int       `json:"wpm"`
	Started        bool      `json:"started"`
}

type Stats struct {
	BookProgress
	RuntimeStats
}

type Favorite struct {
	ID          int       `json:"id,omitempty"`
	UID         string    `json:"uid"`
	Translation string    `json:"translation"`
	BookName    string    `json:"book_name"`
	Book        int       `json:"book"`
	Chapter     int       `json:"chapter"`
	Verse       int       `json:"verse"`
	Text        string    `json:"text"`
	CreatedAt   time.Time `json:"created_at"`
	TimesTyped  int       `json:"times_typed"`
}

type UserStreak struct {
	UID              string `json:"uid"`
	CurrentStreak    int    `json:"current_streak"`
	LongestStreak    int    `json:"longest_streak"`
	LastActiveDate   string `json:"last_active_date"` // YYYY-MM-DD
	TodayVersesCount int    `json:"today_verses_count"`
	TotalVersesTyped int    `json:"total_verses_typed"`
	TotalCharsTyped  int    `json:"total_chars_typed"`
	TotalSeconds     int    `json:"total_seconds"`
}

type Badge struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // "book", "streak", "milestone", "special"
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	EarnedAt    time.Time `json:"earned_at"`
}

type BookGuide struct {
	Name        string `json:"name"`
	Order       int    `json:"order"`
	Testament   string `json:"testament"`
	Category    string `json:"category"`
	Author      string `json:"author"`
	Timeframe   string `json:"timeframe"`
	Theme       string `json:"theme"`
	KeyVerse    string `json:"key_verse"`
	Description string `json:"description"`
	Review      string `json:"review"`
}

type TranslationMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"shortname"`
}

type TranslationData struct {
	Meta           TranslationMeta
	Verses         []Verse
	VersesByBook   map[string][]Verse
	CanonicalBooks []string
}

type Message struct {
	Type         string            `json:"type"`
	Content      string            `json:"content,omitempty"`
	Translation  string            `json:"translation,omitempty"`
	Book         string            `json:"book,omitempty"`
	Verse        *Verse            `json:"verse,omitempty"`
	Guide        *BookGuide        `json:"guide,omitempty"`
	Favorite     *Favorite         `json:"favorite,omitempty"`
	Number       int               `json:"number,omitempty"`
	Total        int               `json:"total,omitempty"`
	Stats        *Stats            `json:"stats,omitempty"`
	IsFavorite   bool              `json:"is_favorite,omitempty"`
	Streak       *UserStreak       `json:"streak,omitempty"`
	Badges       []Badge           `json:"badges,omitempty"`
	Favorites    []Favorite        `json:"favorites,omitempty"`
	Mode         string            `json:"mode,omitempty"` // "verse", "intro", "review", "favorite"
	Books        []string          `json:"books,omitempty"`
	Progress     any               `json:"progress,omitempty"`
	Translations []TranslationMeta `json:"translations,omitempty"`
	Reward       *Badge            `json:"reward,omitempty"`
	WPM          int               `json:"wpm,omitempty"`
	Duration     float64           `json:"duration,omitempty"`
	Mandatory    bool              `json:"mandatory,omitempty"`
	ReviewLocked bool              `json:"review_locked,omitempty"`
	Category     string            `json:"category,omitempty"`
}

// Global in-memory datasets
var (
	translations       = make(map[string]*TranslationData)
	availableTransList []TranslationMeta
	bookGuides         = make(map[string]BookGuide)
	canonicalOrder     []string
	store              Store
)

// ---- Store Interface ----

type Store interface {
	EnsureUser(ctx context.Context, uid string) error
	GetAllBookProgress(ctx context.Context, uid, translation string) (map[string]BookProgress, error)
	GetBookProgress(ctx context.Context, uid, translation, bookName string, totalVerses int) (*BookProgress, error)
	UpdateBookProgress(ctx context.Context, uid, translation, bookName string, bp *BookProgress) error
	EnsureBookProgressRows(ctx context.Context, uid, translation string, books []string, totals map[string]int) error
	CreateTypingSession(ctx context.Context, uid, translation, bookName string) (int, error)
	UpdateTypingSession(ctx context.Context, sessionID int, stats *RuntimeStats) error

	// Favorites
	GetFavorites(ctx context.Context, uid string) ([]Favorite, error)
	AddFavorite(ctx context.Context, fav Favorite) error
	RemoveFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) error
	IsFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) (bool, error)
	RecordFavoriteTyped(ctx context.Context, uid, translation, bookName string, chapter, verse int) error

	// Streaks
	GetStreak(ctx context.Context, uid string) (*UserStreak, error)
	RecordVerseCompleted(ctx context.Context, uid string, chars int, seconds float64) (*UserStreak, error)

	// Badges
	GetBadges(ctx context.Context, uid string) ([]Badge, error)
	AwardBadge(ctx context.Context, uid string, badge Badge) (bool, error)
}

// ---- Postgres Store ----

type postgresStore struct {
	pool *pgxpool.Pool
}

func (s *postgresStore) initDB(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		uid TEXT PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_active TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS book_progress (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		translation TEXT NOT NULL DEFAULT 'kjv',
		book_name TEXT NOT NULL,
		current_verse INT DEFAULT 0,
		total_verses INT NOT NULL,
		correct_entries INT DEFAULT 0,
		mistakes INT DEFAULT 0,
		intro_completed BOOLEAN DEFAULT false,
		review_completed BOOLEAN DEFAULT false,
		completed_at TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	ALTER TABLE book_progress ADD COLUMN IF NOT EXISTS translation TEXT NOT NULL DEFAULT 'kjv';
	ALTER TABLE book_progress ADD COLUMN IF NOT EXISTS intro_completed BOOLEAN DEFAULT false;
	ALTER TABLE book_progress ADD COLUMN IF NOT EXISTS review_completed BOOLEAN DEFAULT false;
	ALTER TABLE book_progress ADD COLUMN IF NOT EXISTS completed_at TIMESTAMP;
	CREATE UNIQUE INDEX IF NOT EXISTS idx_book_progress_uid_trans_book ON book_progress(uid, translation, book_name);
	CREATE INDEX IF NOT EXISTS idx_book_progress_uid_trans ON book_progress(uid, translation);

	CREATE TABLE IF NOT EXISTS typing_sessions (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		translation TEXT NOT NULL DEFAULT 'kjv',
		book_name TEXT NOT NULL,
		started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		ended_at TIMESTAMP,
		chars_typed INT DEFAULT 0,
		correct_chars INT DEFAULT 0,
		wpm INT DEFAULT 0
	);

	ALTER TABLE typing_sessions ADD COLUMN IF NOT EXISTS translation TEXT NOT NULL DEFAULT 'kjv';
	CREATE INDEX IF NOT EXISTS idx_typing_sessions_uid ON typing_sessions(uid);

	CREATE TABLE IF NOT EXISTS favorites (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		translation TEXT NOT NULL DEFAULT 'kjv',
		book_name TEXT NOT NULL,
		book INT NOT NULL DEFAULT 1,
		chapter INT NOT NULL,
		verse INT NOT NULL,
		text TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		times_typed INT DEFAULT 0,
		UNIQUE(uid, translation, book_name, chapter, verse)
	);

	CREATE INDEX IF NOT EXISTS idx_favorites_uid ON favorites(uid);

	CREATE TABLE IF NOT EXISTS user_streaks (
		uid TEXT PRIMARY KEY REFERENCES users(uid) ON DELETE CASCADE,
		current_streak INT DEFAULT 0,
		longest_streak INT DEFAULT 0,
		last_active_date TEXT DEFAULT '',
		today_verses_count INT DEFAULT 0,
		total_verses_typed INT DEFAULT 0,
		total_chars_typed INT DEFAULT 0,
		total_seconds INT DEFAULT 0,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS user_badges (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		badge_id TEXT NOT NULL,
		badge_type TEXT NOT NULL,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		icon TEXT NOT NULL,
		earned_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(uid, badge_id)
	);
	`
	_, err := s.pool.Exec(ctx, schema)
	return err
}

func (s *postgresStore) EnsureUser(ctx context.Context, uid string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (uid, last_active) 
		VALUES ($1, CURRENT_TIMESTAMP)
		ON CONFLICT (uid) DO UPDATE SET last_active = CURRENT_TIMESTAMP
	`, uid)
	return err
}

func (s *postgresStore) GetAllBookProgress(ctx context.Context, uid, translation string) (map[string]BookProgress, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT
			book_name,
			current_verse,
			total_verses,
			correct_entries,
			mistakes,
			intro_completed,
			review_completed,
			completed_at
		FROM book_progress WHERE uid = $1 AND translation = $2`, uid, translation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	progress := make(map[string]BookProgress)
	for rows.Next() {
		var book string
		var bp BookProgress
		if err := rows.Scan(&book, &bp.CurrentVerse, &bp.TotalVerses, &bp.CorrectEntries, &bp.Mistakes, &bp.IntroCompleted, &bp.ReviewCompleted, &bp.CompletedAt); err != nil {
			continue
		}
		progress[book] = bp
	}
	return progress, nil
}

func (s *postgresStore) GetBookProgress(ctx context.Context, uid, translation, bookName string, totalVerses int) (*BookProgress, error) {
	var bp BookProgress
	err := s.pool.QueryRow(ctx, `
		SELECT current_verse, total_verses, correct_entries, mistakes, intro_completed, review_completed, completed_at
		FROM book_progress
		WHERE uid = $1 AND translation = $2 AND book_name = $3
	`, uid, translation, bookName).Scan(&bp.CurrentVerse, &bp.TotalVerses, &bp.CorrectEntries, &bp.Mistakes, &bp.IntroCompleted, &bp.ReviewCompleted, &bp.CompletedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_, insertErr := s.pool.Exec(ctx, `
				INSERT INTO book_progress (uid, translation, book_name, total_verses, intro_completed, review_completed)
				VALUES ($1, $2, $3, $4, false, false)
				ON CONFLICT (uid, translation, book_name) DO NOTHING
			`, uid, translation, bookName, totalVerses)
			if insertErr != nil {
				return nil, insertErr
			}
			return &BookProgress{
				CurrentVerse:    0,
				TotalVerses:     totalVerses,
				CorrectEntries:  0,
				Mistakes:        0,
				IntroCompleted:  false,
				ReviewCompleted: false,
			}, nil
		}
		return nil, err
	}

	return &bp, nil
}

func (s *postgresStore) UpdateBookProgress(ctx context.Context, uid, translation, bookName string, bp *BookProgress) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE book_progress
		SET current_verse = $4,
			correct_entries = $5,
			mistakes = $6,
			intro_completed = $7,
			review_completed = $8,
			completed_at = $9,
			updated_at = CURRENT_TIMESTAMP
		WHERE uid = $1 AND translation = $2 AND book_name = $3
	`, uid, translation, bookName, bp.CurrentVerse, bp.CorrectEntries, bp.Mistakes, bp.IntroCompleted, bp.ReviewCompleted, bp.CompletedAt)
	return err
}

func (s *postgresStore) EnsureBookProgressRows(ctx context.Context, uid, translation string, books []string, totals map[string]int) error {
	totalList := make([]int, len(books))
	for i, b := range books {
		totalList[i] = totals[b]
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO book_progress (uid, translation, book_name, total_verses)
		SELECT $1, $2, unnest($3::text[]), unnest($4::int[])
		ON CONFLICT (uid, translation, book_name) DO NOTHING
	`, uid, translation, books, totalList)
	return err
}

func (s *postgresStore) CreateTypingSession(ctx context.Context, uid, translation, bookName string) (int, error) {
	var sessionID int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO typing_sessions (uid, translation, book_name)
		VALUES ($1, $2, $3)
		RETURNING id
	`, uid, translation, bookName).Scan(&sessionID)
	return sessionID, err
}

func (s *postgresStore) UpdateTypingSession(ctx context.Context, sessionID int, stats *RuntimeStats) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE typing_sessions
		SET chars_typed = $2,
			correct_chars = $3,
			wpm = $4,
			ended_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`, sessionID, stats.CharsTyped, stats.CorrectChars, stats.WPM)
	return err
}

func (s *postgresStore) GetFavorites(ctx context.Context, uid string) ([]Favorite, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, uid, translation, book_name, book, chapter, verse, text, created_at, times_typed
		FROM favorites
		WHERE uid = $1
		ORDER BY book, chapter, verse
	`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var favs []Favorite
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.ID, &f.UID, &f.Translation, &f.BookName, &f.Book, &f.Chapter, &f.Verse, &f.Text, &f.CreatedAt, &f.TimesTyped); err != nil {
			continue
		}
		favs = append(favs, f)
	}
	return favs, nil
}

func (s *postgresStore) AddFavorite(ctx context.Context, fav Favorite) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO favorites (uid, translation, book_name, book, chapter, verse, text)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (uid, translation, book_name, chapter, verse) DO NOTHING
	`, fav.UID, fav.Translation, fav.BookName, fav.Book, fav.Chapter, fav.Verse, fav.Text)
	return err
}

func (s *postgresStore) RemoveFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM favorites
		WHERE uid = $1 AND translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5
	`, uid, translation, bookName, chapter, verse)
	return err
}

func (s *postgresStore) IsFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM favorites
		WHERE uid = $1 AND translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5
	`, uid, translation, bookName, chapter, verse).Scan(&count)
	return count > 0, err
}

func (s *postgresStore) RecordFavoriteTyped(ctx context.Context, uid, translation, bookName string, chapter, verse int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE favorites
		SET times_typed = times_typed + 1
		WHERE uid = $1 AND translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5
	`, uid, translation, bookName, chapter, verse)
	return err
}

func (s *postgresStore) GetStreak(ctx context.Context, uid string) (*UserStreak, error) {
	var streak UserStreak
	err := s.pool.QueryRow(ctx, `
		SELECT uid, current_streak, longest_streak, last_active_date, today_verses_count, total_verses_typed, total_chars_typed, total_seconds
		FROM user_streaks
		WHERE uid = $1
	`, uid).Scan(&streak.UID, &streak.CurrentStreak, &streak.LongestStreak, &streak.LastActiveDate, &streak.TodayVersesCount, &streak.TotalVersesTyped, &streak.TotalCharsTyped, &streak.TotalSeconds)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			nowDate := time.Now().UTC().Format("2006-01-02")
			newStreak := UserStreak{
				UID:              uid,
				CurrentStreak:    0,
				LongestStreak:    0,
				LastActiveDate:   nowDate,
				TodayVersesCount: 0,
			}
			_, _ = s.pool.Exec(ctx, `
				INSERT INTO user_streaks (uid, current_streak, longest_streak, last_active_date, today_verses_count)
				VALUES ($1, 0, 0, $2, 0)
				ON CONFLICT (uid) DO NOTHING
			`, uid, nowDate)
			return &newStreak, nil
		}
		return nil, err
	}
	return &streak, nil
}

func (s *postgresStore) RecordVerseCompleted(ctx context.Context, uid string, chars int, seconds float64) (*UserStreak, error) {
	streak, err := s.GetStreak(ctx, uid)
	if err != nil {
		return nil, err
	}

	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	if streak.LastActiveDate == today {
		streak.TodayVersesCount++
	} else if streak.LastActiveDate == yesterday {
		streak.CurrentStreak++
		streak.TodayVersesCount = 1
		streak.LastActiveDate = today
		if streak.CurrentStreak > streak.LongestStreak {
			streak.LongestStreak = streak.CurrentStreak
		}
	} else {
		streak.CurrentStreak = 1
		streak.TodayVersesCount = 1
		streak.LastActiveDate = today
		if streak.LongestStreak < 1 {
			streak.LongestStreak = 1
		}
	}

	streak.TotalVersesTyped++
	streak.TotalCharsTyped += chars
	streak.TotalSeconds += int(seconds)

	_, err = s.pool.Exec(ctx, `
		INSERT INTO user_streaks (uid, current_streak, longest_streak, last_active_date, today_verses_count, total_verses_typed, total_chars_typed, total_seconds, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP)
		ON CONFLICT (uid) DO UPDATE SET
			current_streak = EXCLUDED.current_streak,
			longest_streak = EXCLUDED.longest_streak,
			last_active_date = EXCLUDED.last_active_date,
			today_verses_count = EXCLUDED.today_verses_count,
			total_verses_typed = EXCLUDED.total_verses_typed,
			total_chars_typed = EXCLUDED.total_chars_typed,
			total_seconds = EXCLUDED.total_seconds,
			updated_at = CURRENT_TIMESTAMP
	`, streak.UID, streak.CurrentStreak, streak.LongestStreak, streak.LastActiveDate, streak.TodayVersesCount, streak.TotalVersesTyped, streak.TotalCharsTyped, streak.TotalSeconds)

	return streak, err
}

func (s *postgresStore) GetBadges(ctx context.Context, uid string) ([]Badge, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT badge_id, badge_type, name, description, icon, earned_at
		FROM user_badges
		WHERE uid = $1
		ORDER BY earned_at DESC
	`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var badges []Badge
	for rows.Next() {
		var b Badge
		if err := rows.Scan(&b.ID, &b.Type, &b.Name, &b.Description, &b.Icon, &b.EarnedAt); err != nil {
			continue
		}
		badges = append(badges, b)
	}
	return badges, nil
}

func (s *postgresStore) AwardBadge(ctx context.Context, uid string, badge Badge) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO user_badges (uid, badge_id, badge_type, name, description, icon, earned_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (uid, badge_id) DO NOTHING
	`, uid, badge.ID, badge.Type, badge.Name, badge.Description, badge.Icon, badge.EarnedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- Local Store (JSON-file backed) ----

type localSession struct {
	UID         string       `json:"uid"`
	Translation string       `json:"translation"`
	BookName    string       `json:"bookName"`
	Stats       RuntimeStats `json:"stats"`
}

type localData struct {
	Progress    map[string]map[string]BookProgress `json:"progress"` // key: uid -> key (translation:book or book for kjv)
	Sessions    map[int]*localSession              `json:"sessions"`
	NextSession int                                `json:"nextSession"`
	Favorites   map[string][]Favorite              `json:"favorites"`
	Streaks     map[string]*UserStreak             `json:"streaks"`
	Badges      map[string][]Badge                 `json:"badges"`
}

type localStore struct {
	mu   sync.Mutex
	path string
	data *localData
}

func newLocalStore(path string) (*localStore, error) {
	s := &localStore{
		path: path,
		data: &localData{
			Progress:  make(map[string]map[string]BookProgress),
			Sessions:  make(map[int]*localSession),
			Favorites: make(map[string][]Favorite),
			Streaks:   make(map[string]*UserStreak),
			Badges:    make(map[string][]Badge),
		},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return s, nil
	}
	if err := json.Unmarshal(raw, s.data); err != nil {
		log.Printf("Warning: unreadable local store %s: %v, resetting", path, err)
		s.data = &localData{
			Progress:  make(map[string]map[string]BookProgress),
			Sessions:  make(map[int]*localSession),
			Favorites: make(map[string][]Favorite),
			Streaks:   make(map[string]*UserStreak),
			Badges:    make(map[string][]Badge),
		}
	}
	if s.data.Progress == nil {
		s.data.Progress = make(map[string]map[string]BookProgress)
	}
	if s.data.Sessions == nil {
		s.data.Sessions = make(map[int]*localSession)
	}
	if s.data.Favorites == nil {
		s.data.Favorites = make(map[string][]Favorite)
	}
	if s.data.Streaks == nil {
		s.data.Streaks = make(map[string]*UserStreak)
	}
	if s.data.Badges == nil {
		s.data.Badges = make(map[string][]Badge)
	}
	return s, nil
}

func (s *localStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func progressKey(translation, bookName string) string {
	if translation == "kjv" || translation == "" {
		return bookName
	}
	return translation + ":" + bookName
}

func (s *localStore) EnsureUser(ctx context.Context, uid string) error {
	return nil
}

func (s *localStore) GetAllBookProgress(ctx context.Context, uid, translation string) (map[string]BookProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make(map[string]BookProgress)
	userProg, ok := s.data.Progress[uid]
	if !ok {
		return res, nil
	}

	prefix := translation + ":"
	for k, bp := range userProg {
		if translation == "kjv" {
			if !strings.Contains(k, ":") {
				res[k] = bp
			} else if strings.HasPrefix(k, "kjv:") {
				res[strings.TrimPrefix(k, "kjv:")] = bp
			}
		} else {
			if strings.HasPrefix(k, prefix) {
				res[strings.TrimPrefix(k, prefix)] = bp
			}
		}
	}
	return res, nil
}

func (s *localStore) GetBookProgress(ctx context.Context, uid, translation, bookName string, totalVerses int) (*BookProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.Progress[uid] == nil {
		s.data.Progress[uid] = make(map[string]BookProgress)
	}

	k := progressKey(translation, bookName)
	if bp, ok := s.data.Progress[uid][k]; ok {
		bpCopy := bp
		if bpCopy.CurrentVerse > 0 && !bpCopy.IntroCompleted {
			bpCopy.IntroCompleted = true
		}
		return &bpCopy, nil
	}
	// Fallback for kjv
	if translation == "kjv" {
		if bp, ok := s.data.Progress[uid]["kjv:"+bookName]; ok {
			bpCopy := bp
			if bpCopy.CurrentVerse > 0 && !bpCopy.IntroCompleted {
				bpCopy.IntroCompleted = true
			}
			return &bpCopy, nil
		}
	}

	bp := BookProgress{
		TotalVerses:     totalVerses,
		IntroCompleted:  false,
		ReviewCompleted: false,
	}
	s.data.Progress[uid][k] = bp
	return &bp, s.save()
}

func (s *localStore) UpdateBookProgress(ctx context.Context, uid, translation, bookName string, bp *BookProgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.Progress[uid] == nil {
		s.data.Progress[uid] = make(map[string]BookProgress)
	}

	k := progressKey(translation, bookName)
	s.data.Progress[uid][k] = *bp
	if translation == "kjv" {
		s.data.Progress[uid][bookName] = *bp
	}
	return s.save()
}

func (s *localStore) EnsureBookProgressRows(ctx context.Context, uid, translation string, books []string, totals map[string]int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.Progress[uid] == nil {
		s.data.Progress[uid] = make(map[string]BookProgress)
	}

	changed := false
	for _, book := range books {
		k := progressKey(translation, book)
		if _, ok := s.data.Progress[uid][k]; !ok {
			if translation == "kjv" {
				if _, ok2 := s.data.Progress[uid]["kjv:"+book]; ok2 {
					continue
				}
			}
			s.data.Progress[uid][k] = BookProgress{
				TotalVerses:     totals[book],
				IntroCompleted:  false,
				ReviewCompleted: false,
			}
			changed = true
		}
	}
	if changed {
		return s.save()
	}
	return nil
}

func (s *localStore) CreateTypingSession(ctx context.Context, uid, translation, bookName string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.NextSession++
	id := s.data.NextSession
	s.data.Sessions[id] = &localSession{
		UID:         uid,
		Translation: translation,
		BookName:    bookName,
	}
	return id, s.save()
}

func (s *localStore) UpdateTypingSession(ctx context.Context, sessionID int, stats *RuntimeStats) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.data.Sessions[sessionID]; ok {
		sess.Stats = *stats
		return s.save()
	}
	return nil
}

func (s *localStore) GetFavorites(ctx context.Context, uid string) ([]Favorite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := s.data.Favorites[uid]
	res := make([]Favorite, len(list))
	copy(res, list)
	return res, nil
}

func (s *localStore) AddFavorite(ctx context.Context, fav Favorite) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.data.Favorites[fav.UID] {
		if existing.Translation == fav.Translation && existing.BookName == fav.BookName && existing.Chapter == fav.Chapter && existing.Verse == fav.Verse {
			return nil // already favorited
		}
	}
	fav.CreatedAt = time.Now().UTC()
	fav.ID = len(s.data.Favorites[fav.UID]) + 1
	s.data.Favorites[fav.UID] = append(s.data.Favorites[fav.UID], fav)
	return s.save()
}

func (s *localStore) RemoveFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := s.data.Favorites[uid]
	newList := make([]Favorite, 0, len(list))
	for _, f := range list {
		if f.Translation == translation && f.BookName == bookName && f.Chapter == chapter && f.Verse == verse {
			continue
		}
		newList = append(newList, f)
	}
	s.data.Favorites[uid] = newList
	return s.save()
}

func (s *localStore) IsFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, f := range s.data.Favorites[uid] {
		if f.Translation == translation && f.BookName == bookName && f.Chapter == chapter && f.Verse == verse {
			return true, nil
		}
	}
	return false, nil
}

func (s *localStore) RecordFavoriteTyped(ctx context.Context, uid, translation, bookName string, chapter, verse int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Favorites[uid] {
		f := &s.data.Favorites[uid][i]
		if f.Translation == translation && f.BookName == bookName && f.Chapter == chapter && f.Verse == verse {
			f.TimesTyped++
			return s.save()
		}
	}
	return nil
}

func (s *localStore) GetStreak(ctx context.Context, uid string) (*UserStreak, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	streak, ok := s.data.Streaks[uid]
	if !ok {
		nowDate := time.Now().UTC().Format("2006-01-02")
		newStreak := &UserStreak{
			UID:            uid,
			LastActiveDate: nowDate,
		}
		s.data.Streaks[uid] = newStreak
		return newStreak, nil
	}
	res := *streak
	return &res, nil
}

func (s *localStore) RecordVerseCompleted(ctx context.Context, uid string, chars int, seconds float64) (*UserStreak, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	streak, ok := s.data.Streaks[uid]
	if !ok {
		streak = &UserStreak{UID: uid}
		s.data.Streaks[uid] = streak
	}

	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	if streak.LastActiveDate == today {
		streak.TodayVersesCount++
	} else if streak.LastActiveDate == yesterday {
		streak.CurrentStreak++
		streak.TodayVersesCount = 1
		streak.LastActiveDate = today
		if streak.CurrentStreak > streak.LongestStreak {
			streak.LongestStreak = streak.CurrentStreak
		}
	} else {
		streak.CurrentStreak = 1
		streak.TodayVersesCount = 1
		streak.LastActiveDate = today
		if streak.LongestStreak < 1 {
			streak.LongestStreak = 1
		}
	}

	streak.TotalVersesTyped++
	streak.TotalCharsTyped += chars
	streak.TotalSeconds += int(seconds)

	ret := *streak
	return &ret, s.save()
}

func (s *localStore) GetBadges(ctx context.Context, uid string) ([]Badge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := s.data.Badges[uid]
	res := make([]Badge, len(list))
	copy(res, list)
	return res, nil
}

func (s *localStore) AwardBadge(ctx context.Context, uid string, badge Badge) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, b := range s.data.Badges[uid] {
		if b.ID == badge.ID {
			return false, nil // already unlocked
		}
	}
	s.data.Badges[uid] = append(s.data.Badges[uid], badge)
	return true, s.save()
}

// ---- Text Processing & Utilities ----

// cleanVerseText removes non-typing markers and digital tags while standardizing quotes,
// apostrophes, dashes, and whitespace for a smooth typing experience.
func cleanVerseText(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		// Convert whitespace (including tabs, newlines) to regular space
		if unicode.IsSpace(r) {
			sb.WriteRune(' ')
			continue
		}
		// Strip other non-space control characters
		if unicode.IsControl(r) {
			continue
		}
		// Strip paragraph marks, quote markers, and digital bracket tags
		if r == '\u00B6' || r == '\u2029' || r == '\u2028' || r == '\u2039' || r == '\u203A' || r == '[' || r == ']' {
			continue
		}
		// Strip Hebrew letter headers in English text (e.g. Psalm 119)
		if r >= 0x0590 && r <= 0x05FF {
			continue
		}
		// Standardize smart quotes / apostrophes
		if r == '\u2018' || r == '\u2019' || r == '`' || r == '´' {
			sb.WriteRune('\'')
			continue
		}
		if r == '\u201C' || r == '\u201D' || r == '«' || r == '»' {
			sb.WriteRune('"')
			continue
		}
		if r == '\u2014' || r == '\u2013' {
			sb.WriteRune('-')
			continue
		}
		if r == '\u00E6' {
			sb.WriteString("ae")
			continue
		}
		if r == '\u00C6' {
			sb.WriteString("Ae")
			continue
		}
		if r == '\u00FC' {
			sb.WriteRune('u')
			continue
		}
		if r == '\u00EF' {
			sb.WriteRune('i')
			continue
		}
		sb.WriteRune(r)
	}
	// Consolidate whitespace and trim
	return strings.Join(strings.Fields(sb.String()), " ")
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// ---- Data Loading ----

func loadTranslationsAndGuides() error {
	// 1. Load Book Guides (66 books)
	guideData, err := appFS.ReadFile("book_guides.json")
	if err != nil {
		return fmt.Errorf("reading book_guides.json: %w", err)
	}
	if err := json.Unmarshal(guideData, &bookGuides); err != nil {
		return fmt.Errorf("unmarshaling book_guides.json: %w", err)
	}

	// 2. Canonical book ordering
	type orderEntry struct {
		name  string
		order int
	}
	var orders []orderEntry
	for name, g := range bookGuides {
		orders = append(orders, orderEntry{name: name, order: g.Order})
	}
	// Sort 1 to 66
	for i := 0; i < len(orders)-1; i++ {
		for j := i + 1; j < len(orders); j++ {
			if orders[i].order > orders[j].order {
				orders[i], orders[j] = orders[j], orders[i]
			}
		}
	}
	canonicalOrder = make([]string, len(orders))
	for i, o := range orders {
		canonicalOrder[i] = o.name
	}

	// 3. Load Bible Translations: KJV, ESV, NIV, NLT, HCSB, ASV, BBE
	manifest := []struct {
		file      string
		id        string
		name      string
		shortname string
	}{
		{"kjv.json", "kjv", "King James Version (1769)", "KJV"},
		{"esv.json", "esv", "English Standard Version", "ESV"},
		{"niv.json", "niv", "New International Version", "NIV"},
		{"nlt.json", "nlt", "New Living Translation", "NLT"},
		{"hcsb.json", "hcsb", "Holman Christian Standard Bible (CSB)", "HCSB"},
		{"asv.json", "asv", "American Standard Version (1901)", "ASV"},
		{"bbe.json", "bbe", "Bible in Basic English (1965)", "BBE"},
	}

	for _, m := range manifest {
		raw, err := appFS.ReadFile(m.file)
		if err != nil {
			return fmt.Errorf("reading %s: %w", m.file, err)
		}
		var parsed struct {
			Verses []Verse `json:"verses"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf("parsing %s: %w", m.file, err)
		}

		byBook := make(map[string][]Verse)
		cleanedVerses := make([]Verse, 0, len(parsed.Verses))
		for _, v := range parsed.Verses {
			v.Text = cleanVerseText(v.Text)
			cleanedVerses = append(cleanedVerses, v)
			byBook[v.BookName] = append(byBook[v.BookName], v)
		}

		td := &TranslationData{
			Meta: TranslationMeta{
				ID:        m.id,
				Name:      m.name,
				ShortName: m.shortname,
			},
			Verses:         cleanedVerses,
			VersesByBook:   byBook,
			CanonicalBooks: canonicalOrder,
		}
		translations[m.id] = td
		availableTransList = append(availableTransList, td.Meta)
	}

	log.Printf("Loaded %d translations and %d book guides successfully", len(translations), len(bookGuides))
	return nil
}

// ---- Badge & Achievement System ----

func checkAndAwardBadges(ctx context.Context, s Store, uid, trans, book string, bp *BookProgress, streak *UserStreak, wpm int, correctVerse bool, mode string) []Badge {
	var newlyEarned []Badge

	tryAward := func(badge Badge) {
		earned, err := s.AwardBadge(ctx, uid, badge)
		if err == nil && earned {
			newlyEarned = append(newlyEarned, badge)
		}
	}

	now := time.Now().UTC()

	// 1. Verse count milestones
	if streak != nil {
		if streak.TotalVersesTyped >= 1 {
			tryAward(Badge{
				ID:          "verse_1",
				Type:        "milestone",
				Name:        "First Verse Scribe",
				Description: "Completed typing your very first verse!",
				Icon:        "🌱",
				EarnedAt:    now,
			})
		}
		if streak.TotalVersesTyped >= 25 {
			tryAward(Badge{
				ID:          "verse_25",
				Type:        "milestone",
				Name:        "Diligent Copyist",
				Description: "Typed 25 verses of Scripture!",
				Icon:        "📖",
				EarnedAt:    now,
			})
		}
		if streak.TotalVersesTyped >= 100 {
			tryAward(Badge{
				ID:          "verse_100",
				Type:        "milestone",
				Name:        "Century Scribe",
				Description: "Typed 100 verses of Scripture!",
				Icon:        "💯",
				EarnedAt:    now,
			})
		}
		if streak.TotalVersesTyped >= 500 {
			tryAward(Badge{
				ID:          "verse_500",
				Type:        "milestone",
				Name:        "Scroll Keeper",
				Description: "Typed 500 verses of Scripture!",
				Icon:        "📜",
				EarnedAt:    now,
			})
		}

		// 2. Streaks
		if streak.CurrentStreak >= 3 {
			tryAward(Badge{
				ID:          "streak_3",
				Type:        "streak",
				Name:        "Spark of Devotion",
				Description: "Maintained a 3-day typing streak!",
				Icon:        "🔥",
				EarnedAt:    now,
			})
		}
		if streak.CurrentStreak >= 7 {
			tryAward(Badge{
				ID:          "streak_7",
				Type:        "streak",
				Name:        "Week of Faith",
				Description: "Maintained a 7-day typing streak!",
				Icon:        "⚡",
				EarnedAt:    now,
			})
		}
		if streak.CurrentStreak >= 30 {
			tryAward(Badge{
				ID:          "streak_30",
				Type:        "streak",
				Name:        "Unshakeable Devotion",
				Description: "Maintained an incredible 30-day streak!",
				Icon:        "🌟",
				EarnedAt:    now,
			})
		}
	}

	// 3. Speed & Accuracy
	if wpm >= 40 {
		tryAward(Badge{
			ID:          "speed_40",
			Type:        "speed",
			Name:        "Swift Fingers",
			Description: "Achieved 40+ Words Per Minute!",
			Icon:        "⚡",
			EarnedAt:    now,
		})
	}
	if wpm >= 60 {
		tryAward(Badge{
			ID:          "speed_60",
			Type:        "speed",
			Name:        "Lightning Scribe",
			Description: "Achieved 60+ Words Per Minute!",
			Icon:        "🚀",
			EarnedAt:    now,
		})
	}

	// 4. Typable Features
	if mode == "intro" {
		tryAward(Badge{
			ID:          "intro_scribe",
			Type:        "special",
			Name:        "Prologue Scribe",
			Description: "Typed a complete Book Introduction!",
			Icon:        "✍️",
			EarnedAt:    now,
		})
	}
	if mode == "review" {
		tryAward(Badge{
			ID:          "review_scholar",
			Type:        "special",
			Name:        "Epilogue Scholar",
			Description: "Typed a complete Book Reflection & Review!",
			Icon:        "🎓",
			EarnedAt:    now,
		})
	}
	if mode == "favorite" {
		tryAward(Badge{
			ID:          "favorite_typist",
			Type:        "special",
			Name:        "Heart Inscription",
			Description: "Practiced typing a favorited passage!",
			Icon:        "🕊️",
			EarnedAt:    now,
		})
	}

	// 5. Book Completion Rewards (awarded upon review completion!)
	if bp != nil && bp.TotalVerses > 0 && bp.CurrentVerse >= bp.TotalVerses && bp.ReviewCompleted {
		tryAward(Badge{
			ID:          "book_" + book,
			Type:        "book",
			Name:        book + " Mastered",
			Description: fmt.Sprintf("Completed all %d verses and review of %s!", bp.TotalVerses, book),
			Icon:        "🏆",
			EarnedAt:    now,
		})

		// Check section milestones
		allProg, err := s.GetAllBookProgress(ctx, uid, trans)
		if err == nil {
			isBookDone := func(b string) bool {
				p, ok := allProg[b]
				return ok && p.TotalVerses > 0 && p.CurrentVerse >= p.TotalVerses && p.ReviewCompleted
			}

			// Pentateuch
			penta := []string{"Genesis", "Exodus", "Leviticus", "Numbers", "Deuteronomy"}
			pentaDone := true
			for _, b := range penta {
				if !isBookDone(b) {
					pentaDone = false
					break
				}
			}
			if pentaDone {
				tryAward(Badge{
					ID:          "pentateuch_master",
					Type:        "milestone",
					Name:        "Pentateuch Pioneer",
					Description: "Completed all 5 books of the Law of Moses!",
					Icon:        "📜",
					EarnedAt:    now,
				})
			}

			// Gospels
			gospels := []string{"Matthew", "Mark", "Luke", "John"}
			gospelsDone := true
			for _, b := range gospels {
				if !isBookDone(b) {
					gospelsDone = false
					break
				}
			}
			if gospelsDone {
				tryAward(Badge{
					ID:          "gospel_bearer",
					Type:        "milestone",
					Name:        "Gospel Bearer",
					Description: "Completed all four holy Gospels!",
					Icon:        "🕊️",
					EarnedAt:    now,
				})
			}
		}
	}

	return newlyEarned
}

// ---- WebSocket Handler ----

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	rawConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}
	conn := &safeConn{conn: rawConn}
	defer conn.Close()

	conn.conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.conn.SetPongHandler(func(string) error {
		return conn.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WritePing(); err != nil {
					return
				}
			}
		}
	}()

	uid := r.URL.Query().Get("uid")
	if uid == "" {
		uid = fmt.Sprintf("user_%d", time.Now().UnixNano())
	}

	ctx := context.Background()
	if err := store.EnsureUser(ctx, uid); err != nil {
		log.Printf("Error ensuring user %s: %v", uid, err)
		return
	}

	// Default active translation
	currentTransID := r.URL.Query().Get("trans")
	if _, ok := translations[currentTransID]; !ok {
		currentTransID = "kjv"
	}
	transData := translations[currentTransID]

	// Send available translations
	_ = conn.WriteJSON(Message{
		Type:         "translations",
		Translation:  currentTransID,
		Translations: availableTransList,
	})

	// Send current streak
	streak, _ := store.GetStreak(ctx, uid)
	if streak != nil {
		_ = conn.WriteJSON(Message{
			Type:   "streakUpdate",
			Streak: streak,
		})
	}

	// Send badges
	badges, _ := store.GetBadges(ctx, uid)
	_ = conn.WriteJSON(Message{
		Type:   "badges",
		Badges: badges,
	})

	// Send favorites
	favs, _ := store.GetFavorites(ctx, uid)
	_ = conn.WriteJSON(Message{
		Type:      "favorites",
		Favorites: favs,
	})

	// Ensure book progress rows for current translation
	totalsMap := make(map[string]int, len(transData.VersesByBook))
	for b, vList := range transData.VersesByBook {
		totalsMap[b] = len(vList)
	}
	_ = store.EnsureBookProgressRows(ctx, uid, currentTransID, canonicalOrder, totalsMap)

	allProgress, err := store.GetAllBookProgress(ctx, uid, currentTransID)
	if err != nil {
		allProgress = make(map[string]BookProgress)
	}

	// Send canonical book list
	_ = conn.WriteJSON(Message{
		Type:        "books",
		Books:       canonicalOrder,
		Progress:    allProgress,
		Translation: currentTransID,
	})

	// Session State
	var selectedBook string
	var bookProgress *BookProgress
	var bookVerses []Verse
	var sessionID int
	var activeVerseIdx int
	currentMode := "verse" // "verse", "intro", "review", "favorite"
	var activeFavorite *Favorite

	runtimeStats := RuntimeStats{
		StartTime:      time.Now(),
		VerseStartTime: time.Now(),
	}

	sendCurrentVerse := func() {
		if selectedBook == "" || bookProgress == nil || len(bookVerses) == 0 {
			return
		}
		guide := bookGuides[selectedBook]
		stats := &Stats{
			BookProgress: *bookProgress,
			RuntimeStats: runtimeStats,
		}

		// 1. Mandatory Intro Check: User MUST type the intro before starting Chapter 1
		if !bookProgress.IntroCompleted {
			currentMode = "intro"
			runtimeStats.VerseStartTime = time.Now()
			_ = conn.WriteJSON(Message{
				Type:         "intro",
				Mode:         "intro",
				Mandatory:    true,
				Content:      guide.Description,
				Guide:        &guide,
				Number:       0,
				Total:        len(bookVerses),
				Stats:        stats,
				Translation:  currentTransID,
				Category:     guide.Category,
				ReviewLocked: true,
			})
			return
		}

		// 2. Normal Scripture Verses
		// Clamp activeVerseIdx to not exceed highest unlocked verse
		if activeVerseIdx > bookProgress.CurrentVerse {
			activeVerseIdx = bookProgress.CurrentVerse
		}
		if activeVerseIdx < 0 {
			activeVerseIdx = 0
		}

		if activeVerseIdx < len(bookVerses) {
			currentMode = "verse"
			v := bookVerses[activeVerseIdx]
			isFav, _ := store.IsFavorite(ctx, uid, currentTransID, v.BookName, v.Chapter, v.Verse)
			runtimeStats.VerseStartTime = time.Now()
			_ = conn.WriteJSON(Message{
				Type:         "verse",
				Mode:         "verse",
				Content:      v.Text,
				Verse:        &v,
				Guide:        &guide,
				Number:       activeVerseIdx + 1,
				Total:        len(bookVerses),
				Stats:        stats,
				IsFavorite:   isFav,
				Translation:  currentTransID,
				Category:     guide.Category,
				ReviewLocked: bookProgress.CurrentVerse < len(bookVerses), // Locked while any verses remain uncompleted!
			})
			return
		}

		// 3. Mandatory Review Check: When all verses are typed, the user MUST type the review to finish the book
		if !bookProgress.ReviewCompleted {
			currentMode = "review"
			runtimeStats.VerseStartTime = time.Now()
			_ = conn.WriteJSON(Message{
				Type:         "review",
				Mode:         "review",
				Mandatory:    true,
				Content:      guide.Review,
				Guide:        &guide,
				Number:       len(bookVerses),
				Total:        len(bookVerses),
				Stats:        stats,
				Translation:  currentTransID,
				Category:     guide.Category,
				ReviewLocked: false, // Review is now unlocked to type!
			})
			return
		}

		// 4. Fully Completed Book!
		currentMode = "verse"
		_ = conn.WriteJSON(Message{
			Type:         "complete",
			Mode:         "verse",
			Content:      "All done with " + selectedBook + "! Praise the Sun! \\[T]/",
			Guide:        &guide,
			Number:       len(bookVerses),
			Total:        len(bookVerses),
			Stats:        stats,
			Translation:  currentTransID,
			Category:     guide.Category,
			ReviewLocked: false,
		})
	}

	for {
		var msg Message
		if err := conn.conn.ReadJSON(&msg); err != nil {
			return
		}

		switch msg.Type {
		case "select_translation":
			newTrans := strings.ToLower(strings.TrimSpace(msg.Content))
			if td, ok := translations[newTrans]; ok {
				currentTransID = newTrans
				transData = td

				totalsMap := make(map[string]int, len(transData.VersesByBook))
				for b, vList := range transData.VersesByBook {
					totalsMap[b] = len(vList)
				}
				_ = store.EnsureBookProgressRows(ctx, uid, currentTransID, canonicalOrder, totalsMap)

				allProgress, _ = store.GetAllBookProgress(ctx, uid, currentTransID)
				_ = conn.WriteJSON(Message{
					Type:        "books",
					Books:       canonicalOrder,
					Progress:    allProgress,
					Translation: currentTransID,
				})

				if selectedBook != "" {
					bookVerses = transData.VersesByBook[selectedBook]
					bookProgress, _ = store.GetBookProgress(ctx, uid, currentTransID, selectedBook, len(bookVerses))
					activeVerseIdx = bookProgress.CurrentVerse
					sendCurrentVerse()
				}
			}

		case "select_book":
			bookName := msg.Content
			if _, ok := transData.VersesByBook[bookName]; !ok {
				continue
			}
			selectedBook = bookName
			bookVerses = transData.VersesByBook[selectedBook]

			var err error
			bookProgress, err = store.GetBookProgress(ctx, uid, currentTransID, selectedBook, len(bookVerses))
			if err != nil {
				log.Printf("Error getting progress: %v", err)
				continue
			}

			sessionID, _ = store.CreateTypingSession(ctx, uid, currentTransID, selectedBook)
			runtimeStats = RuntimeStats{
				StartTime:      time.Now(),
				VerseStartTime: time.Now(),
			}
			activeVerseIdx = bookProgress.CurrentVerse
			sendCurrentVerse()

		case "jump_verse":
			if selectedBook == "" || bookProgress == nil || len(bookVerses) == 0 {
				continue
			}
			// Intro must be completed before jumping verses
			if !bookProgress.IntroCompleted {
				_ = conn.WriteJSON(Message{
					Type:    "wrong",
					Content: "Please complete the mandatory Book Introduction before jumping into chapters!",
				})
				continue
			}

			targetVerseIdx := msg.Number - 1
			if targetVerseIdx < 0 {
				targetVerseIdx = 0
			}
			if targetVerseIdx >= len(bookVerses) {
				targetVerseIdx = len(bookVerses) - 1
			}

			// User cannot skip passages or jump to ones that haven't been unlocked yet
			if targetVerseIdx > bookProgress.CurrentVerse {
				_ = conn.WriteJSON(Message{
					Type:    "wrong",
					Content: fmt.Sprintf("Verse %d is locked! You must complete preceding verses first.", msg.Number),
				})
				continue
			}

			activeVerseIdx = targetVerseIdx
			sendCurrentVerse()

		case "start_intro":
			bName := msg.Content
			if bName == "" {
				bName = selectedBook
			}
			guide, ok := bookGuides[bName]
			if !ok {
				continue
			}
			currentMode = "intro"
			runtimeStats.VerseStartTime = time.Now()
			_ = conn.WriteJSON(Message{
				Type:         "intro",
				Mode:         "intro",
				Mandatory:    !bookProgress.IntroCompleted,
				Content:      guide.Description,
				Guide:        &guide,
				Stats:        &Stats{BookProgress: *bookProgress, RuntimeStats: runtimeStats},
				Translation:  currentTransID,
				Category:     guide.Category,
				ReviewLocked: bookProgress.CurrentVerse < len(bookVerses),
			})

		case "start_review":
			bName := msg.Content
			if bName == "" {
				bName = selectedBook
			}
			guide, ok := bookGuides[bName]
			if !ok {
				continue
			}

			// Locked until all verses of this book are completed!
			if bookProgress != nil && bookProgress.CurrentVerse < len(bookVerses) {
				_ = conn.WriteJSON(Message{
					Type:         "review_locked",
					Content:      fmt.Sprintf("Review for %s is locked! Complete all %d verses to unlock the review.", bName, len(bookVerses)),
					ReviewLocked: true,
				})
				continue
			}

			currentMode = "review"
			runtimeStats.VerseStartTime = time.Now()
			_ = conn.WriteJSON(Message{
				Type:         "review",
				Mode:         "review",
				Mandatory:    !bookProgress.ReviewCompleted,
				Content:      guide.Review,
				Guide:        &guide,
				Stats:        &Stats{BookProgress: *bookProgress, RuntimeStats: runtimeStats},
				Translation:  currentTransID,
				Category:     guide.Category,
				ReviewLocked: false,
			})

		case "start_favorite":
			favID := msg.Number
			allFavs, _ := store.GetFavorites(ctx, uid)
			for _, f := range allFavs {
				if f.ID == favID || (f.BookName == msg.Book && f.Chapter == msg.Verse.Chapter && f.Verse == msg.Verse.Verse) {
					fCopy := f
					activeFavorite = &fCopy
					currentMode = "favorite"
					guide := bookGuides[f.BookName]
					runtimeStats.VerseStartTime = time.Now()
					_ = conn.WriteJSON(Message{
						Type:         "favorite_verse",
						Mode:         "favorite",
						Content:      f.Text,
						Favorite:     &f,
						Guide:        &guide,
						Stats:        &Stats{BookProgress: *bookProgress, RuntimeStats: runtimeStats},
						Translation:  f.Translation,
						Category:     guide.Category,
						ReviewLocked: bookProgress.CurrentVerse < len(bookVerses),
					})
					break
				}
			}

		case "exit_mode":
			currentMode = "verse"
			sendCurrentVerse()

		case "toggle_favorite":
			var targetVerse Verse
			if msg.Verse != nil {
				targetVerse = *msg.Verse
			} else if selectedBook != "" && bookProgress != nil && activeVerseIdx < len(bookVerses) {
				targetVerse = bookVerses[activeVerseIdx]
			} else {
				continue
			}

			isFav, _ := store.IsFavorite(ctx, uid, currentTransID, targetVerse.BookName, targetVerse.Chapter, targetVerse.Verse)
			if isFav {
				_ = store.RemoveFavorite(ctx, uid, currentTransID, targetVerse.BookName, targetVerse.Chapter, targetVerse.Verse)
			} else {
				fav := Favorite{
					UID:         uid,
					Translation: currentTransID,
					BookName:    targetVerse.BookName,
					Book:        targetVerse.Book,
					Chapter:     targetVerse.Chapter,
					Verse:       targetVerse.Verse,
					Text:        targetVerse.Text,
				}
				_ = store.AddFavorite(ctx, fav)

				favList, _ := store.GetFavorites(ctx, uid)
				if len(favList) >= 5 {
					_ = checkAndAwardBadges(ctx, store, uid, currentTransID, targetVerse.BookName, bookProgress, streak, 0, false, "fav_collect")
				}
			}

			favList, _ := store.GetFavorites(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:       "favoriteToggled",
				IsFavorite: !isFav,
				Favorites:  favList,
				Verse:      &targetVerse,
			})

		case "get_favorites":
			favList, _ := store.GetFavorites(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:      "favorites",
				Favorites: favList,
			})

		case "get_badges":
			badgeList, _ := store.GetBadges(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:   "badges",
				Badges: badgeList,
			})

		case "get_streak":
			st, _ := store.GetStreak(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:   "streakUpdate",
				Streak: st,
			})

		case "content":
			userInput := cleanVerseText(strings.TrimSpace(msg.Content))
			if userInput == "" {
				continue
			}

			var targetText string
			switch currentMode {
			case "intro":
				targetText = cleanVerseText(bookGuides[selectedBook].Description)
			case "review":
				targetText = cleanVerseText(bookGuides[selectedBook].Review)
			case "favorite":
				if activeFavorite != nil {
					targetText = cleanVerseText(activeFavorite.Text)
				}
			default: // verse
				if selectedBook != "" && bookProgress != nil && activeVerseIdx < len(bookVerses) {
					targetText = cleanVerseText(bookVerses[activeVerseIdx].Text)
				}
			}

			if targetText == "" {
				continue
			}

			// Accurate WPM & duration calculation
			verseSeconds := msg.Duration
			if verseSeconds <= 0 {
				verseSeconds = time.Since(runtimeStats.VerseStartTime).Seconds()
			}
			if verseSeconds < 0.5 {
				verseSeconds = 0.5
			}
			verseElapsedMin := verseSeconds / 60.0

			if userInput == targetText {
				inputLen := runeLen(userInput)
				runtimeStats.CorrectChars += inputLen
				runtimeStats.CharsTyped += inputLen

				// Determine WPM: prefer client-measured active typing WPM, fallback to elapsed calculation
				var instantWPM int
				if msg.WPM > 0 && msg.WPM <= 250 {
					instantWPM = msg.WPM
				} else {
					instantWPM = int((float64(inputLen) / 5.0) / verseElapsedMin)
				}
				runtimeStats.WPM = instantWPM

				// Update streak
				updatedStreak, _ := store.RecordVerseCompleted(ctx, uid, inputLen, verseSeconds)
				streak = updatedStreak

				var rewards []Badge
				if currentMode == "intro" {
					bookProgress.IntroCompleted = true
					_ = store.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
					rewards = checkAndAwardBadges(ctx, store, uid, currentTransID, selectedBook, bookProgress, streak, instantWPM, true, "intro")
					activeVerseIdx = bookProgress.CurrentVerse
				} else if currentMode == "review" {
					bookProgress.ReviewCompleted = true
					now := time.Now().UTC()
					bookProgress.CompletedAt = &now
					_ = store.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
					rewards = checkAndAwardBadges(ctx, store, uid, currentTransID, selectedBook, bookProgress, streak, instantWPM, true, "review")
				} else if currentMode == "favorite" && activeFavorite != nil {
					_ = store.RecordFavoriteTyped(ctx, uid, activeFavorite.Translation, activeFavorite.BookName, activeFavorite.Chapter, activeFavorite.Verse)
					rewards = checkAndAwardBadges(ctx, store, uid, activeFavorite.Translation, activeFavorite.BookName, bookProgress, streak, instantWPM, true, "favorite")
				} else { // verse
					if activeVerseIdx == bookProgress.CurrentVerse {
						bookProgress.CurrentVerse++
						activeVerseIdx++
					} else {
						activeVerseIdx++
					}
					bookProgress.CorrectEntries++
					_ = store.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)

					if sessionID > 0 {
						_ = store.UpdateTypingSession(ctx, sessionID, &runtimeStats)
					}

					rewards = checkAndAwardBadges(ctx, store, uid, currentTransID, selectedBook, bookProgress, streak, instantWPM, true, "verse")
				}

				stats := &Stats{
					BookProgress: *bookProgress,
					RuntimeStats: runtimeStats,
				}

				_ = conn.WriteJSON(Message{Type: "correct", Content: "correct", Stats: stats, WPM: instantWPM})
				_ = conn.WriteJSON(Message{Type: "streakUpdate", Streak: streak})

				for _, badge := range rewards {
					_ = conn.WriteJSON(Message{
						Type:   "badgeUnlocked",
						Reward: &badge,
					})
				}

				_ = conn.WriteJSON(map[string]any{
					"type":     "progressUpdate",
					"book":     selectedBook,
					"progress": bookProgress,
				})

				// If completing an intro, notify completion and advance immediately into Chapter 1 Verse 1
				if currentMode == "intro" {
					_ = conn.WriteJSON(Message{
						Type:    "modeComplete",
						Mode:    "intro",
						Content: fmt.Sprintf("✓ %s Introduction completed! Chapter 1 unlocked.", selectedBook),
					})
					sendCurrentVerse()
					continue
				}

				// If completing a review, send celebration
				if currentMode == "review" {
					guide := bookGuides[selectedBook]
					_ = conn.WriteJSON(Message{
						Type:         "complete",
						Content:      "All done with " + selectedBook + "! Praise the Sun! \\[T]/",
						Stats:        stats,
						Guide:        &guide,
						Translation:  currentTransID,
						Category:     guide.Category,
						ReviewLocked: false,
					})
					sendCurrentVerse()
					continue
				}

				// If completing a favorite, return to verse flow
				if currentMode == "favorite" {
					_ = conn.WriteJSON(Message{
						Type:    "modeComplete",
						Mode:    "favorite",
						Content: "Great job practicing this favorite verse!",
					})
					currentMode = "verse"
					sendCurrentVerse()
					continue
				}

				// Normal verse progression
				sendCurrentVerse()
			} else {
				// Typo / wrong
				if bookProgress != nil {
					bookProgress.Mistakes++
					_ = store.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
				}
				runtimeStats.CharsTyped += runeLen(userInput)

				stats := &Stats{
					BookProgress: *bookProgress,
					RuntimeStats: runtimeStats,
				}

				_ = conn.WriteJSON(Message{Type: "wrong", Content: "wrong", Stats: stats})
			}
		}
	}
}

// ---- Main ----

func main() {
	if err := loadTranslationsAndGuides(); err != nil {
		log.Fatalf("Failed to initialize bible datasets: %v", err)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		path := os.Getenv("LOCAL_STORE")
		if path == "" {
			path = filepath.Join(".", ".elespe", "data.json")
		}
		local, err := newLocalStore(path)
		if err != nil {
			log.Fatalf("Failed to open local store: %v", err)
		}
		store = local
		log.Printf("DATABASE_URL not set; using local file store at %s", path)
	} else {
		config, err := pgxpool.ParseConfig(dbURL)
		if err != nil {
			log.Fatalf("Failed to parse database config: %v", err)
		}

		config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		config.MaxConns = 5
		config.MinConns = 0
		config.MaxConnLifetime = time.Hour
		config.MaxConnIdleTime = time.Minute * 30
		config.HealthCheckPeriod = time.Minute

		pool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			log.Fatalf("Failed to connect to database: %v", err)
		}
		defer pool.Close()

		ps := &postgresStore{pool: pool}
		store = ps

		var version string
		if err := pool.QueryRow(context.Background(), "SELECT version()").Scan(&version); err != nil {
			log.Fatalf("Query failed: %v", err)
		}
		log.Println("Connected to:", version)

		if err := ps.initDB(context.Background()); err != nil {
			log.Fatalf("Failed to initialize database: %v", err)
		}
		log.Println("Database initialized")
	}

	http.Handle("/", http.FileServer(http.FS(publicFS{appFS})))
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

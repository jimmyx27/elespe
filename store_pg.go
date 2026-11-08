package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type dirtyBookProgItem struct {
	uid         string
	translation string
	bookName    string
	bp          BookProgress
}

type dirtyVerseCompItem struct {
	uid         string
	translation string
	bookName    string
	chapter     int
	verse       int
	delta       int
}

type cachedLeaderboards struct {
	resp      *LeaderboardsResponse
	fetchedAt time.Time
}

type postgresStore struct {
	pool *pgxpool.Pool

	mu                    sync.RWMutex
	knownUsers            map[string]bool
	sanctuaryCache        map[string]SanctuarySettings
	dirtySanctuary        map[string]SanctuarySettings
	streakCache           map[string]UserStreak
	dirtyStreaks          map[string]UserStreak
	bookProgCache         map[string]map[string]BookProgress // key: uid + "|" + translation
	dirtyBookProg         map[string]dirtyBookProgItem       // key: uid + "|" + translation + "|" + bookName
	accountCache          map[string]*UserAccount
	badgesCache           map[string][]Badge
	favsCache             map[string][]Favorite
	userVerseCompCache    map[string]int               // key: uid + "|" + trans + "|" + book + ":" + ch + ":" + v
	dirtyUserVerseComps   map[string]dirtyVerseCompItem
	globalVerseCompCache  map[string]int               // key: trans + "|" + book + ":" + ch + ":" + v
	dirtyGlobalVerseComps map[string]dirtyVerseCompItem
	leaderboardsCache     map[string]cachedLeaderboards
	crowdMetricsCache     *CrowdMetrics
	crowdMetricsAt        time.Time

	stopFlushChan chan struct{}
	flushDoneChan chan struct{}
	closeOnce     sync.Once
}

func newPostgresStore(pool *pgxpool.Pool) *postgresStore {
	s := &postgresStore{
		pool:                  pool,
		knownUsers:            make(map[string]bool),
		sanctuaryCache:        make(map[string]SanctuarySettings),
		dirtySanctuary:        make(map[string]SanctuarySettings),
		streakCache:           make(map[string]UserStreak),
		dirtyStreaks:          make(map[string]UserStreak),
		bookProgCache:         make(map[string]map[string]BookProgress),
		dirtyBookProg:         make(map[string]dirtyBookProgItem),
		accountCache:          make(map[string]*UserAccount),
		badgesCache:           make(map[string][]Badge),
		favsCache:             make(map[string][]Favorite),
		userVerseCompCache:    make(map[string]int),
		dirtyUserVerseComps:   make(map[string]dirtyVerseCompItem),
		globalVerseCompCache:  make(map[string]int),
		dirtyGlobalVerseComps: make(map[string]dirtyVerseCompItem),
		leaderboardsCache:     make(map[string]cachedLeaderboards),
		stopFlushChan:         make(chan struct{}),
		flushDoneChan:         make(chan struct{}),
	}
	go s.startWriteBehindLoop()
	return s
}

func (s *postgresStore) startWriteBehindLoop() {
	defer close(s.flushDoneChan)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopFlushChan:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.Flush(ctx)
			cancel()
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			s.Flush(ctx)
			cancel()
		}
	}
}

func (s *postgresStore) Close() {
	s.closeOnce.Do(func() {
		close(s.stopFlushChan)
		<-s.flushDoneChan
	})
}

// Flush persists all buffered in-memory progress, streaks, sanctuary settings, and verse completion deltas to Supabase/PostgreSQL.
func (s *postgresStore) Flush(ctx context.Context) {
	s.mu.Lock()
	if len(s.dirtySanctuary) == 0 && len(s.dirtyStreaks) == 0 && len(s.dirtyBookProg) == 0 && len(s.dirtyUserVerseComps) == 0 && len(s.dirtyGlobalVerseComps) == 0 {
		s.mu.Unlock()
		return
	}

	sanctBatch := s.dirtySanctuary
	s.dirtySanctuary = make(map[string]SanctuarySettings)

	streakBatch := s.dirtyStreaks
	s.dirtyStreaks = make(map[string]UserStreak)

	bpBatch := s.dirtyBookProg
	s.dirtyBookProg = make(map[string]dirtyBookProgItem)

	uCompBatch := s.dirtyUserVerseComps
	s.dirtyUserVerseComps = make(map[string]dirtyVerseCompItem)

	gCompBatch := s.dirtyGlobalVerseComps
	s.dirtyGlobalVerseComps = make(map[string]dirtyVerseCompItem)
	s.mu.Unlock()

	for uid, settings := range sanctBatch {
		if err := s.flushSanctuaryRow(ctx, uid, settings); err != nil {
			log.Printf("Write-behind sanctuary flush error for %s: %v", uid, err)
		}
	}

	for _, streak := range streakBatch {
		_ = s.EnsureUser(ctx, streak.UID)
		_, err := s.pool.Exec(ctx, `
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
		if err != nil {
			log.Printf("Write-behind streak flush error for %s: %v", streak.UID, err)
		}
	}

	for _, item := range bpBatch {
		_ = s.EnsureUser(ctx, item.uid)
		totalV := item.bp.TotalVerses
		if totalV <= 0 {
			totalV = 1
		}
		_, err := s.pool.Exec(ctx, `
			INSERT INTO book_progress (uid, translation, book_name, current_verse, total_verses, correct_entries, mistakes, intro_completed, review_completed, completed_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, CURRENT_TIMESTAMP)
			ON CONFLICT (uid, translation, book_name) DO UPDATE SET
				current_verse = EXCLUDED.current_verse,
				correct_entries = EXCLUDED.correct_entries,
				mistakes = EXCLUDED.mistakes,
				intro_completed = EXCLUDED.intro_completed,
				review_completed = EXCLUDED.review_completed,
				completed_at = EXCLUDED.completed_at,
				updated_at = CURRENT_TIMESTAMP
		`, item.uid, item.translation, item.bookName, item.bp.CurrentVerse, totalV, item.bp.CorrectEntries, item.bp.Mistakes, item.bp.IntroCompleted, item.bp.ReviewCompleted, item.bp.CompletedAt)
		if err != nil {
			log.Printf("Write-behind book_progress flush error for %s/%s: %v", item.uid, item.bookName, err)
		}
	}

	for _, item := range gCompBatch {
		if item.delta <= 0 {
			continue
		}
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO global_verse_completions (translation, book_name, chapter, verse, times_completed, last_completed_at)
			VALUES ($1, $2, $3, $4, $5, CURRENT_TIMESTAMP)
			ON CONFLICT (translation, book_name, chapter, verse) DO UPDATE SET
				times_completed = global_verse_completions.times_completed + $5,
				last_completed_at = CURRENT_TIMESTAMP
		`, item.translation, item.bookName, item.chapter, item.verse, item.delta)
	}

	for _, item := range uCompBatch {
		if item.delta <= 0 || item.uid == "" || strings.HasPrefix(item.uid, "angel-") {
			continue
		}
		_ = s.EnsureUser(ctx, item.uid)
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO verse_completions (uid, translation, book_name, chapter, verse, times_completed, last_completed_at)
			VALUES ($1, $2, $3, $4, $5, $6, CURRENT_TIMESTAMP)
			ON CONFLICT (uid, translation, book_name, chapter, verse) DO UPDATE SET
				times_completed = verse_completions.times_completed + $6,
				last_completed_at = CURRENT_TIMESTAMP
		`, item.uid, item.translation, item.bookName, item.chapter, item.verse, item.delta)
	}
}

func (s *postgresStore) initDB(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		uid TEXT PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_active TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS user_accounts (
		uid TEXT PRIMARY KEY REFERENCES users(uid) ON DELETE CASCADE,
		username TEXT UNIQUE NOT NULL,
		email TEXT,
		password_hash TEXT NOT NULL,
		display_name TEXT NOT NULL,
		avatar_color TEXT NOT NULL DEFAULT '#6366f1',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_active TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_user_accounts_lower_username ON user_accounts(LOWER(username));
	CREATE INDEX IF NOT EXISTS idx_user_accounts_lower_email ON user_accounts(LOWER(email));

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
	CREATE INDEX IF NOT EXISTS idx_typing_sessions_started_at ON typing_sessions(started_at DESC);
	CREATE INDEX IF NOT EXISTS idx_typing_sessions_uid_wpm ON typing_sessions(uid, wpm DESC) WHERE wpm > 0;
	CREATE INDEX IF NOT EXISTS idx_typing_sessions_wpm ON typing_sessions(wpm) WHERE wpm > 0;

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
	CREATE INDEX IF NOT EXISTS idx_user_streaks_current ON user_streaks(current_streak DESC);
	CREATE INDEX IF NOT EXISTS idx_user_streaks_total_verses ON user_streaks(total_verses_typed DESC);

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
	CREATE INDEX IF NOT EXISTS idx_user_badges_uid ON user_badges(uid, earned_at DESC);

	CREATE TABLE IF NOT EXISTS verse_comments (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		display_name TEXT NOT NULL,
		avatar_color TEXT NOT NULL DEFAULT '#6366f1',
		streak_days INT DEFAULT 0,
		translation TEXT NOT NULL,
		book_name TEXT NOT NULL,
		chapter INT NOT NULL,
		verse INT NOT NULL,
		prompt_type TEXT NOT NULL DEFAULT 'reflection',
		content TEXT NOT NULL,
		is_private BOOLEAN DEFAULT false,
		parent_id INT DEFAULT 0,
		reactions JSONB DEFAULT '{}',
		flagged BOOLEAN DEFAULT false,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_verse_comments_ref ON verse_comments(translation, book_name, chapter, verse);

	CREATE TABLE IF NOT EXISTS chat_messages (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		display_name TEXT NOT NULL,
		avatar_color TEXT NOT NULL DEFAULT '#6366f1',
		channel TEXT NOT NULL DEFAULT 'global',
		content TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_chat_messages_channel ON chat_messages(channel, created_at);

	CREATE TABLE IF NOT EXISTS daily_race_results (
		id SERIAL PRIMARY KEY,
		date TEXT NOT NULL,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		display_name TEXT NOT NULL,
		avatar_color TEXT NOT NULL DEFAULT '#6366f1',
		wpm INT NOT NULL,
		duration DOUBLE PRECISION NOT NULL,
		accuracy INT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_daily_race_date ON daily_race_results(date, wpm DESC);

	CREATE TABLE IF NOT EXISTS verse_completions (
		id SERIAL PRIMARY KEY,
		uid TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
		translation TEXT NOT NULL,
		book_name TEXT NOT NULL,
		chapter INT NOT NULL,
		verse INT NOT NULL,
		times_completed INT DEFAULT 1,
		last_completed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(uid, translation, book_name, chapter, verse)
	);
	CREATE INDEX IF NOT EXISTS idx_verse_completions_ref ON verse_completions(translation, book_name, chapter, verse);

	CREATE TABLE IF NOT EXISTS global_verse_completions (
		id SERIAL PRIMARY KEY,
		translation TEXT NOT NULL,
		book_name TEXT NOT NULL,
		chapter INT NOT NULL,
		verse INT NOT NULL,
		times_completed INT DEFAULT 1,
		last_completed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(translation, book_name, chapter, verse)
	);
	CREATE INDEX IF NOT EXISTS idx_global_verse_completions_ref ON global_verse_completions(translation, book_name, chapter, verse);

	CREATE TABLE IF NOT EXISTS sanctuary_settings (
		uid TEXT PRIMARY KEY REFERENCES users(uid) ON DELETE CASCADE,
		text_color TEXT NOT NULL DEFAULT 'emerald',
		font_family TEXT NOT NULL DEFAULT 'sans',
		caret_aura TEXT NOT NULL DEFAULT 'default',
		audio_profile TEXT NOT NULL DEFAULT 'classic',
		celebrate_fx TEXT NOT NULL DEFAULT 'confetti',
		guardian_name TEXT DEFAULT '',
		guardian_pace INT DEFAULT 5,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS guardian_name TEXT DEFAULT '';
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS guardian_pace INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS guardian_level INT DEFAULT 1;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS guardian_xp INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS preferred_angel_levels JSONB DEFAULT '{}';
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS preferred_demon_levels JSONB DEFAULT '{}';
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS demons_defeated INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS passage_burst_damage INT DEFAULT 30;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS summon_tokens INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS tokens_spent INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS synced_letters INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS synced_words INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS synced_verses INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS synced_books INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS guardian_book_name TEXT DEFAULT '';
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS guardian_chapter INT DEFAULT 0;
	ALTER TABLE sanctuary_settings ADD COLUMN IF NOT EXISTS guardian_verse INT DEFAULT 0;

	CREATE TABLE IF NOT EXISTS boss_raids (
		id TEXT PRIMARY KEY,
		boss_name TEXT NOT NULL,
		boss_title TEXT NOT NULL,
		boss_rank TEXT NOT NULL,
		boss_wpm INT NOT NULL,
		level INT DEFAULT 1,
		max_level INT DEFAULT 4,
		is_demon_boss BOOLEAN DEFAULT false,
		max_hp INT NOT NULL,
		current_hp INT NOT NULL,
		required_wpm INT NOT NULL,
		translation TEXT NOT NULL,
		book_name TEXT NOT NULL,
		chapter INT NOT NULL,
		verse INT NOT NULL,
		text TEXT NOT NULL,
		conquered BOOLEAN DEFAULT false,
		conquerors JSONB DEFAULT '[]',
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	ALTER TABLE boss_raids ADD COLUMN IF NOT EXISTS level INT DEFAULT 1;
	ALTER TABLE boss_raids ADD COLUMN IF NOT EXISTS max_level INT DEFAULT 4;
	ALTER TABLE boss_raids ADD COLUMN IF NOT EXISTS is_demon_boss BOOLEAN DEFAULT false;
	CREATE INDEX IF NOT EXISTS idx_boss_raids_level_demon ON boss_raids(level, is_demon_boss, updated_at DESC);

	-- Enable Row Level Security (RLS) on all public tables to satisfy Supabase Security Advisor
	-- and block direct PostgREST anon/authenticated access while the Go server uses the owner/postgres connection.
	ALTER TABLE users ENABLE ROW LEVEL SECURITY;
	ALTER TABLE user_accounts ENABLE ROW LEVEL SECURITY;
	ALTER TABLE book_progress ENABLE ROW LEVEL SECURITY;
	ALTER TABLE typing_sessions ENABLE ROW LEVEL SECURITY;
	ALTER TABLE favorites ENABLE ROW LEVEL SECURITY;
	ALTER TABLE user_streaks ENABLE ROW LEVEL SECURITY;
	ALTER TABLE user_badges ENABLE ROW LEVEL SECURITY;
	ALTER TABLE verse_comments ENABLE ROW LEVEL SECURITY;
	ALTER TABLE chat_messages ENABLE ROW LEVEL SECURITY;
	ALTER TABLE daily_race_results ENABLE ROW LEVEL SECURITY;
	ALTER TABLE verse_completions ENABLE ROW LEVEL SECURITY;
	ALTER TABLE global_verse_completions ENABLE ROW LEVEL SECURITY;
	ALTER TABLE sanctuary_settings ENABLE ROW LEVEL SECURITY;
	ALTER TABLE boss_raids ENABLE ROW LEVEL SECURITY;

	DO $$
	BEGIN
		IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
			REVOKE ALL ON ALL TABLES IN SCHEMA public FROM anon;
			REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM anon;
		END IF;
		IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
			REVOKE ALL ON ALL TABLES IN SCHEMA public FROM authenticated;
			REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM authenticated;
		END IF;
	EXCEPTION WHEN OTHERS THEN
		NULL;
	END $$;
	`
	_, err := s.pool.Exec(ctx, schema)
	return err
}

func (s *postgresStore) EnsureUser(ctx context.Context, uid string) error {
	if uid == "" {
		return nil
	}
	s.mu.RLock()
	known := s.knownUsers[uid]
	s.mu.RUnlock()
	if known {
		return nil
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (uid, last_active) 
		VALUES ($1, CURRENT_TIMESTAMP)
		ON CONFLICT (uid) DO UPDATE SET last_active = CURRENT_TIMESTAMP
	`, uid)
	if err == nil {
		s.mu.Lock()
		s.knownUsers[uid] = true
		s.mu.Unlock()
	}
	return err
}

func (s *postgresStore) GetAllBookProgress(ctx context.Context, uid, translation string) (map[string]BookProgress, error) {
	cacheKey := uid + "|" + translation
	s.mu.RLock()
	if cached, ok := s.bookProgCache[cacheKey]; ok && len(cached) > 0 {
		out := make(map[string]BookProgress, len(cached))
		for k, v := range cached {
			out[k] = v
		}
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()

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

	s.mu.Lock()
	if s.bookProgCache[cacheKey] == nil {
		s.bookProgCache[cacheKey] = make(map[string]BookProgress, len(progress))
	}
	for k, v := range progress {
		if _, dirty := s.dirtyBookProg[cacheKey+"|"+k]; !dirty {
			s.bookProgCache[cacheKey][k] = v
		} else {
			progress[k] = s.bookProgCache[cacheKey][k]
		}
	}
	s.mu.Unlock()

	return progress, nil
}

func (s *postgresStore) GetBookProgress(ctx context.Context, uid, translation, bookName string, totalVerses int) (*BookProgress, error) {
	cacheKey := uid + "|" + translation
	s.mu.RLock()
	if m, ok := s.bookProgCache[cacheKey]; ok {
		if bp, exists := m[bookName]; exists {
			cp := bp
			s.mu.RUnlock()
			return &cp, nil
		}
	}
	s.mu.RUnlock()

	var bp BookProgress
	err := s.pool.QueryRow(ctx, `
		SELECT current_verse, total_verses, correct_entries, mistakes, intro_completed, review_completed, completed_at
		FROM book_progress
		WHERE uid = $1 AND translation = $2 AND book_name = $3
	`, uid, translation, bookName).Scan(&bp.CurrentVerse, &bp.TotalVerses, &bp.CorrectEntries, &bp.Mistakes, &bp.IntroCompleted, &bp.ReviewCompleted, &bp.CompletedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			bp = BookProgress{
				TotalVerses:     totalVerses,
				IntroCompleted:  false,
				ReviewCompleted: false,
			}
			s.mu.Lock()
			if s.bookProgCache[cacheKey] == nil {
				s.bookProgCache[cacheKey] = make(map[string]BookProgress)
			}
			s.bookProgCache[cacheKey][bookName] = bp
			s.dirtyBookProg[cacheKey+"|"+bookName] = dirtyBookProgItem{
				uid:         uid,
				translation: translation,
				bookName:    bookName,
				bp:          bp,
			}
			s.mu.Unlock()
			return &bp, nil
		}
		return nil, err
	}

	s.mu.Lock()
	if s.bookProgCache[cacheKey] == nil {
		s.bookProgCache[cacheKey] = make(map[string]BookProgress)
	}
	s.bookProgCache[cacheKey][bookName] = bp
	s.mu.Unlock()

	return &bp, nil
}

func (s *postgresStore) UpdateBookProgress(ctx context.Context, uid, translation, bookName string, bp *BookProgress) error {
	if bp == nil {
		return nil
	}
	cacheKey := uid + "|" + translation
	cp := *bp
	s.mu.Lock()
	if s.bookProgCache[cacheKey] == nil {
		s.bookProgCache[cacheKey] = make(map[string]BookProgress)
	}
	s.bookProgCache[cacheKey][bookName] = cp
	s.dirtyBookProg[cacheKey+"|"+bookName] = dirtyBookProgItem{
		uid:         uid,
		translation: translation,
		bookName:    bookName,
		bp:          cp,
	}
	s.mu.Unlock()
	return nil
}

func (s *postgresStore) EnsureBookProgressRows(ctx context.Context, uid, translation string, books []string, totals map[string]int) error {
	cacheKey := uid + "|" + translation
	s.mu.RLock()
	_, alreadyLoaded := s.bookProgCache[cacheKey]
	s.mu.RUnlock()
	if alreadyLoaded {
		return nil
	}

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
	if sessionID <= 0 || stats == nil {
		return nil
	}
	chars := stats.CharsTyped
	correct := stats.CorrectChars
	wpm := stats.WPM
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(bgCtx, `
			UPDATE typing_sessions
			SET chars_typed = $2,
				correct_chars = $3,
				wpm = $4,
				ended_at = CURRENT_TIMESTAMP
			WHERE id = $1
		`, sessionID, chars, correct, wpm)
	}()
	return nil
}

func (s *postgresStore) RecordTypingSession(ctx context.Context, uid, translation, bookName string, chars, correct, wpm int, duration float64) error {
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.EnsureUser(bgCtx, uid)
		_, _ = s.pool.Exec(bgCtx, `
			INSERT INTO typing_sessions (uid, translation, book_name, chars_typed, correct_chars, wpm, started_at, ended_at)
			VALUES ($1, $2, $3, $4, $5, $6, CURRENT_TIMESTAMP - ($7 || ' seconds')::interval, CURRENT_TIMESTAMP)
		`, uid, translation, bookName, chars, correct, wpm, duration)
	}()
	return nil
}

func (s *postgresStore) GetFavorites(ctx context.Context, uid string) ([]Favorite, error) {
	s.mu.RLock()
	if cached, ok := s.favsCache[uid]; ok {
		cp := make([]Favorite, len(cached))
		copy(cp, cached)
		s.mu.RUnlock()
		return cp, nil
	}
	s.mu.RUnlock()

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

	s.mu.Lock()
	cp := make([]Favorite, len(favs))
	copy(cp, favs)
	s.favsCache[uid] = cp
	s.mu.Unlock()

	return favs, nil
}

func (s *postgresStore) AddFavorite(ctx context.Context, fav Favorite) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO favorites (uid, translation, book_name, book, chapter, verse, text)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (uid, translation, book_name, chapter, verse) DO NOTHING
	`, fav.UID, fav.Translation, fav.BookName, fav.Book, fav.Chapter, fav.Verse, fav.Text)
	if err == nil {
		s.mu.Lock()
		delete(s.favsCache, fav.UID)
		s.mu.Unlock()
	}
	return err
}

func (s *postgresStore) RemoveFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM favorites
		WHERE uid = $1 AND translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5
	`, uid, translation, bookName, chapter, verse)
	if err == nil {
		s.mu.Lock()
		delete(s.favsCache, uid)
		s.mu.Unlock()
	}
	return err
}

func (s *postgresStore) IsFavorite(ctx context.Context, uid, translation, bookName string, chapter, verse int) (bool, error) {
	s.mu.RLock()
	if cached, ok := s.favsCache[uid]; ok {
		for _, f := range cached {
			if f.Translation == translation && f.BookName == bookName && f.Chapter == chapter && f.Verse == verse {
				s.mu.RUnlock()
				return true, nil
			}
		}
		s.mu.RUnlock()
		return false, nil
	}
	s.mu.RUnlock()

	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM favorites
		WHERE uid = $1 AND translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5
	`, uid, translation, bookName, chapter, verse).Scan(&count)
	return count > 0, err
}

func (s *postgresStore) RecordFavoriteTyped(ctx context.Context, uid, translation, bookName string, chapter, verse int) error {
	s.mu.Lock()
	if cached, ok := s.favsCache[uid]; ok {
		for i := range cached {
			if cached[i].Translation == translation && cached[i].BookName == bookName && cached[i].Chapter == chapter && cached[i].Verse == verse {
				cached[i].TimesTyped++
			}
		}
	}
	s.mu.Unlock()

	_, err := s.pool.Exec(ctx, `
		UPDATE favorites
		SET times_typed = times_typed + 1
		WHERE uid = $1 AND translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5
	`, uid, translation, bookName, chapter, verse)
	return err
}

func (s *postgresStore) GetStreak(ctx context.Context, uid string) (*UserStreak, error) {
	s.mu.RLock()
	if cached, ok := s.streakCache[uid]; ok {
		cp := cached
		s.mu.RUnlock()
		return &cp, nil
	}
	s.mu.RUnlock()

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
			s.mu.Lock()
			s.streakCache[uid] = newStreak
			s.dirtyStreaks[uid] = newStreak
			s.mu.Unlock()
			return &newStreak, nil
		}
		return nil, err
	}

	s.mu.Lock()
	if _, dirty := s.dirtyStreaks[uid]; !dirty {
		s.streakCache[uid] = streak
	} else {
		streak = s.streakCache[uid]
	}
	s.mu.Unlock()

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

	s.mu.Lock()
	s.streakCache[uid] = *streak
	s.dirtyStreaks[uid] = *streak
	s.mu.Unlock()

	return streak, nil
}

func (s *postgresStore) GetBadges(ctx context.Context, uid string) ([]Badge, error) {
	s.mu.RLock()
	if cached, ok := s.badgesCache[uid]; ok {
		cp := make([]Badge, len(cached))
		copy(cp, cached)
		s.mu.RUnlock()
		return cp, nil
	}
	s.mu.RUnlock()

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

	s.mu.Lock()
	cp := make([]Badge, len(badges))
	copy(cp, badges)
	s.badgesCache[uid] = cp
	s.mu.Unlock()

	return badges, nil
}

func (s *postgresStore) AwardBadge(ctx context.Context, uid string, badge Badge) (bool, error) {
	s.mu.RLock()
	if cached, ok := s.badgesCache[uid]; ok {
		for _, b := range cached {
			if b.ID == badge.ID {
				s.mu.RUnlock()
				return false, nil
			}
		}
	}
	s.mu.RUnlock()

	tag, err := s.pool.Exec(ctx, `
		INSERT INTO user_badges (uid, badge_id, badge_type, name, description, icon, earned_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (uid, badge_id) DO NOTHING
	`, uid, badge.ID, badge.Type, badge.Name, badge.Description, badge.Icon, badge.EarnedAt)
	if err != nil {
		return false, err
	}
	awarded := tag.RowsAffected() > 0
	if awarded {
		s.mu.Lock()
		if cached, ok := s.badgesCache[uid]; ok {
			s.badgesCache[uid] = append([]Badge{badge}, cached...)
		}
		s.mu.Unlock()
	}
	return awarded, nil
}

// ---- Accounts & Identity Implementation ----

func (s *postgresStore) RegisterAccount(ctx context.Context, username, email, passwordHash, displayName, avatarColor, anonUID string) (*UserAccount, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username cannot be empty")
	}
	accountUID := "acc_" + username
	if displayName == "" {
		displayName = username
	}
	if avatarColor == "" {
		avatarColor = "#8b5cf6"
	}

	_ = s.EnsureUser(ctx, accountUID)
	saltedHash := saltPasswordHash(passwordHash)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_accounts (uid, username, email, password_hash, display_name, avatar_color)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, accountUID, username, email, saltedHash, displayName, avatarColor)
	if err != nil {
		return nil, fmt.Errorf("username or account already exists: %w", err)
	}

	acc := &UserAccount{
		UID:         accountUID,
		Username:    username,
		DisplayName: displayName,
		Email:       email,
		AvatarColor: avatarColor,
		CreatedAt:   time.Now().UTC(),
		LastActive:  time.Now().UTC(),
	}

	s.mu.Lock()
	cp := *acc
	s.accountCache[accountUID] = &cp
	s.mu.Unlock()

	if anonUID != "" && anonUID != accountUID && !strings.HasPrefix(anonUID, "acc_") {
		_ = s.MergeUserProgress(ctx, accountUID, anonUID)
	}

	return acc, nil
}

func (s *postgresStore) LoginAccount(ctx context.Context, usernameOrEmail, passwordHash, anonUID string) (*UserAccount, error) {
	var acc UserAccount
	var storedHash string
	key := strings.TrimSpace(usernameOrEmail)

	err := s.pool.QueryRow(ctx, `
		SELECT uid, username, email, password_hash, display_name, avatar_color, created_at, last_active
		FROM user_accounts
		WHERE LOWER(username) = LOWER($1) OR (email <> '' AND LOWER(email) = LOWER($1))
		LIMIT 1
	`, key).Scan(&acc.UID, &acc.Username, &acc.Email, &storedHash, &acc.DisplayName, &acc.AvatarColor, &acc.CreatedAt, &acc.LastActive)

	if err != nil || !verifyStoredPassword(storedHash, passwordHash) {
		return nil, errors.New("invalid credentials")
	}

	if needsPasswordUpgrade(storedHash) {
		upgradedHash := saltPasswordHash(passwordHash)
		_, _ = s.pool.Exec(ctx, `UPDATE user_accounts SET password_hash = $2, last_active = CURRENT_TIMESTAMP WHERE uid = $1`, acc.UID, upgradedHash)
	} else {
		_, _ = s.pool.Exec(ctx, `UPDATE user_accounts SET last_active = CURRENT_TIMESTAMP WHERE uid = $1`, acc.UID)
	}

	s.mu.Lock()
	cp := acc
	s.accountCache[acc.UID] = &cp
	s.mu.Unlock()

	if anonUID != "" && anonUID != acc.UID && !strings.HasPrefix(anonUID, "acc_") {
		_ = s.MergeUserProgress(ctx, acc.UID, anonUID)
	}

	return &acc, nil
}

func (s *postgresStore) GetAccount(ctx context.Context, uid string) (*UserAccount, error) {
	s.mu.RLock()
	if cached, ok := s.accountCache[uid]; ok {
		if cached == nil {
			s.mu.RUnlock()
			return nil, pgx.ErrNoRows
		}
		cp := *cached
		s.mu.RUnlock()
		return &cp, nil
	}
	s.mu.RUnlock()

	var acc UserAccount
	err := s.pool.QueryRow(ctx, `
		SELECT uid, username, email, display_name, avatar_color, created_at, last_active
		FROM user_accounts
		WHERE uid = $1
	`, uid).Scan(&acc.UID, &acc.Username, &acc.Email, &acc.DisplayName, &acc.AvatarColor, &acc.CreatedAt, &acc.LastActive)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.mu.Lock()
			s.accountCache[uid] = nil
			s.mu.Unlock()
		}
		return nil, err
	}

	s.mu.Lock()
	cp := acc
	s.accountCache[uid] = &cp
	s.mu.Unlock()
	return &acc, nil
}

func (s *postgresStore) UpdateProfile(ctx context.Context, uid, displayName, avatarColor string) (*UserAccount, error) {
	_ = s.EnsureUser(ctx, uid)
	var acc UserAccount

	err := s.pool.QueryRow(ctx, `
		INSERT INTO user_accounts (uid, username, password_hash, display_name, avatar_color)
		VALUES ($1, $1, '', $2, $3)
		ON CONFLICT (uid) DO UPDATE SET
			display_name = CASE WHEN $2 <> '' THEN $2 ELSE user_accounts.display_name END,
			avatar_color = CASE WHEN $3 <> '' THEN $3 ELSE user_accounts.avatar_color END,
			last_active = CURRENT_TIMESTAMP
		RETURNING uid, username, COALESCE(email, ''), display_name, avatar_color, created_at, last_active
	`, uid, displayName, avatarColor).Scan(&acc.UID, &acc.Username, &acc.Email, &acc.DisplayName, &acc.AvatarColor, &acc.CreatedAt, &acc.LastActive)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	cp := acc
	s.accountCache[uid] = &cp
	s.mu.Unlock()
	return &acc, nil
}

func (s *postgresStore) MergeUserProgress(ctx context.Context, targetUID, sourceUID string) error {
	if targetUID == sourceUID || sourceUID == "" || strings.HasPrefix(sourceUID, "acc_") {
		return nil
	}

	// Flush any buffered write-behind progress before merging in PostgreSQL
	s.Flush(ctx)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 1. Merge book progress
	_, err = tx.Exec(ctx, `
		INSERT INTO book_progress (uid, translation, book_name, current_verse, total_verses, correct_entries, mistakes, intro_completed, review_completed, completed_at)
		SELECT $1, translation, book_name, current_verse, total_verses, correct_entries, mistakes, intro_completed, review_completed, completed_at
		FROM book_progress
		WHERE uid = $2
		ON CONFLICT (uid, translation, book_name) DO UPDATE SET
			current_verse = GREATEST(book_progress.current_verse, EXCLUDED.current_verse),
			correct_entries = GREATEST(book_progress.correct_entries, EXCLUDED.correct_entries),
			mistakes = book_progress.mistakes + EXCLUDED.mistakes,
			intro_completed = book_progress.intro_completed OR EXCLUDED.intro_completed,
			review_completed = book_progress.review_completed OR EXCLUDED.review_completed,
			completed_at = COALESCE(book_progress.completed_at, EXCLUDED.completed_at)
	`, targetUID, sourceUID)
	if err != nil {
		return err
	}

	// 2. Merge favorites
	_, err = tx.Exec(ctx, `
		INSERT INTO favorites (uid, translation, book_name, book, chapter, verse, text, times_typed)
		SELECT $1, translation, book_name, book, chapter, verse, text, times_typed
		FROM favorites
		WHERE uid = $2
		ON CONFLICT (uid, translation, book_name, chapter, verse) DO NOTHING
	`, targetUID, sourceUID)
	if err != nil {
		return err
	}

	// 3. Merge badges
	_, err = tx.Exec(ctx, `
		INSERT INTO user_badges (uid, badge_id, badge_type, name, description, icon, earned_at)
		SELECT $1, badge_id, badge_type, name, description, icon, earned_at
		FROM user_badges
		WHERE uid = $2
		ON CONFLICT (uid, badge_id) DO NOTHING
	`, targetUID, sourceUID)
	if err != nil {
		return err
	}

	// 4. Merge streaks
	_, err = tx.Exec(ctx, `
		INSERT INTO user_streaks (uid, current_streak, longest_streak, last_active_date, today_verses_count, total_verses_typed, total_chars_typed, total_seconds)
		SELECT $1, current_streak, longest_streak, last_active_date, today_verses_count, total_verses_typed, total_chars_typed, total_seconds
		FROM user_streaks
		WHERE uid = $2
		ON CONFLICT (uid) DO UPDATE SET
			current_streak = GREATEST(user_streaks.current_streak, EXCLUDED.current_streak),
			longest_streak = GREATEST(user_streaks.longest_streak, EXCLUDED.longest_streak),
			today_verses_count = user_streaks.today_verses_count + EXCLUDED.today_verses_count,
			total_verses_typed = user_streaks.total_verses_typed + EXCLUDED.total_verses_typed,
			total_chars_typed = user_streaks.total_chars_typed + EXCLUDED.total_chars_typed,
			total_seconds = user_streaks.total_seconds + EXCLUDED.total_seconds
	`, targetUID, sourceUID)
	if err != nil {
		return err
	}

	// 5. Reassign sessions, comments, races
	_, _ = tx.Exec(ctx, `UPDATE typing_sessions SET uid = $1 WHERE uid = $2`, targetUID, sourceUID)
	_, _ = tx.Exec(ctx, `UPDATE verse_comments SET uid = $1 WHERE uid = $2`, targetUID, sourceUID)
	_, _ = tx.Exec(ctx, `UPDATE daily_race_results SET uid = $1 WHERE uid = $2`, targetUID, sourceUID)

	// 6. Merge verse completions
	_, _ = tx.Exec(ctx, `
		INSERT INTO verse_completions (uid, translation, book_name, chapter, verse, times_completed, last_completed_at)
		SELECT $1, translation, book_name, chapter, verse, times_completed, last_completed_at
		FROM verse_completions
		WHERE uid = $2
		ON CONFLICT (uid, translation, book_name, chapter, verse) DO UPDATE SET
			times_completed = verse_completions.times_completed + EXCLUDED.times_completed,
			last_completed_at = GREATEST(verse_completions.last_completed_at, EXCLUDED.last_completed_at)
	`, targetUID, sourceUID)

	// 7. Merge sanctuary settings
	_, _ = tx.Exec(ctx, `
		INSERT INTO sanctuary_settings (
			uid, text_color, font_family, caret_aura, audio_profile, celebrate_fx,
			guardian_name, guardian_pace, guardian_level, guardian_xp,
			preferred_angel_levels, preferred_demon_levels, demons_defeated, passage_burst_damage,
			summon_tokens, tokens_spent, synced_letters, synced_words, synced_verses, synced_books
		)
		SELECT
			$1, text_color, font_family, caret_aura, audio_profile, celebrate_fx,
			guardian_name, guardian_pace, guardian_level, guardian_xp,
			preferred_angel_levels, preferred_demon_levels, demons_defeated, passage_burst_damage,
			summon_tokens, tokens_spent, synced_letters, synced_words, synced_verses, synced_books
		FROM sanctuary_settings
		WHERE uid = $2
		ON CONFLICT (uid) DO UPDATE SET
			guardian_level = GREATEST(sanctuary_settings.guardian_level, EXCLUDED.guardian_level),
			guardian_xp = GREATEST(sanctuary_settings.guardian_xp, EXCLUDED.guardian_xp),
			demons_defeated = sanctuary_settings.demons_defeated + EXCLUDED.demons_defeated,
			summon_tokens = sanctuary_settings.summon_tokens + EXCLUDED.summon_tokens,
			tokens_spent = sanctuary_settings.tokens_spent + EXCLUDED.tokens_spent,
			synced_letters = sanctuary_settings.synced_letters + EXCLUDED.synced_letters,
			synced_words = sanctuary_settings.synced_words + EXCLUDED.synced_words,
			synced_verses = sanctuary_settings.synced_verses + EXCLUDED.synced_verses,
			synced_books = sanctuary_settings.synced_books + EXCLUDED.synced_books
	`, targetUID, sourceUID)

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Evict targetUID caches so subsequent reads fetch the freshly merged state
	s.mu.Lock()
	delete(s.sanctuaryCache, targetUID)
	delete(s.streakCache, targetUID)
	delete(s.badgesCache, targetUID)
	delete(s.favsCache, targetUID)
	for k := range s.bookProgCache {
		if strings.HasPrefix(k, targetUID+"|") {
			delete(s.bookProgCache, k)
		}
	}
	s.mu.Unlock()
	return nil
}

// ---- Verse Comments & Reflection Implementation ----

func (s *postgresStore) GetVerseComments(ctx context.Context, translation, bookName string, chapter, verse int, userUID string) ([]VerseComment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, uid, display_name, avatar_color, streak_days, translation, book_name, chapter, verse, prompt_type, content, is_private, parent_id, reactions, flagged, created_at
		FROM verse_comments
		WHERE translation = $1 AND book_name = $2 AND chapter = $3 AND verse = $4 AND (is_private = false OR uid = $5)
		ORDER BY created_at ASC
	`, translation, bookName, chapter, verse, userUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []VerseComment
	for rows.Next() {
		var c VerseComment
		var rxJSON []byte
		if err := rows.Scan(&c.ID, &c.UID, &c.DisplayName, &c.AvatarColor, &c.StreakDays, &c.Translation, &c.BookName, &c.Chapter, &c.Verse, &c.PromptType, &c.Content, &c.IsPrivate, &c.ParentID, &rxJSON, &c.Flagged, &c.CreatedAt); err != nil {
			continue
		}
		if len(rxJSON) > 0 {
			_ = json.Unmarshal(rxJSON, &c.Reactions)
		}
		if c.Reactions == nil {
			c.Reactions = make(map[string]int)
		}
		list = append(list, c)
	}
	return list, nil
}

func (s *postgresStore) AddVerseComment(ctx context.Context, comment VerseComment) (*VerseComment, error) {
	if strings.TrimSpace(comment.Content) == "" {
		return nil, errors.New("comment content cannot be empty")
	}

	if comment.Reactions == nil {
		comment.Reactions = make(map[string]int)
	}
	rxJSON, _ := json.Marshal(comment.Reactions)

	err := s.pool.QueryRow(ctx, `
		INSERT INTO verse_comments (uid, display_name, avatar_color, streak_days, translation, book_name, chapter, verse, prompt_type, content, is_private, parent_id, reactions)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, created_at
	`, comment.UID, comment.DisplayName, comment.AvatarColor, comment.StreakDays, comment.Translation, comment.BookName, comment.Chapter, comment.Verse, comment.PromptType, comment.Content, comment.IsPrivate, comment.ParentID, rxJSON).Scan(&comment.ID, &comment.CreatedAt)

	return &comment, err
}

func (s *postgresStore) ReactVerseComment(ctx context.Context, commentID int, reaction string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE verse_comments
		SET reactions = jsonb_set(
			COALESCE(reactions, '{}'::jsonb),
			ARRAY[$2],
			(COALESCE(reactions->>$2, '0')::int + 1)::text::jsonb
		)
		WHERE id = $1
	`, commentID, reaction)
	return err
}

func (s *postgresStore) FlagVerseComment(ctx context.Context, commentID int) error {
	_, err := s.pool.Exec(ctx, `UPDATE verse_comments SET flagged = true WHERE id = $1`, commentID)
	return err
}

// ---- Live Fellowship Chat Implementation ----

func (s *postgresStore) GetRecentChat(ctx context.Context, channel string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, uid, display_name, avatar_color, channel, content, created_at
		FROM chat_messages
		WHERE channel = $1 OR $1 = 'global'
		ORDER BY created_at DESC
		LIMIT $2
	`, channel, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.UID, &m.DisplayName, &m.AvatarColor, &m.Channel, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		list = append(list, m)
	}

	// Reverse to ascending order
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list, nil
}

func (s *postgresStore) AddChatMessage(ctx context.Context, msg ChatMessage) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat_messages (uid, display_name, avatar_color, channel, content)
		VALUES ($1, $2, $3, $4, $5)
	`, msg.UID, msg.DisplayName, msg.AvatarColor, msg.Channel, msg.Content)
	return err
}

// ---- Daily Challenge Racing Implementation ----

func (s *postgresStore) SaveRaceResult(ctx context.Context, res RaceResult) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO daily_race_results (date, uid, display_name, avatar_color, wpm, duration, accuracy)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, res.Date, res.UID, res.DisplayName, res.AvatarColor, res.WPM, res.Duration, res.Accuracy)
	if err == nil {
		s.mu.Lock()
		delete(s.leaderboardsCache, res.Date)
		s.mu.Unlock()
	}
	return err
}

func (s *postgresStore) GetDailyRaceResults(ctx context.Context, dateStr string) ([]RaceResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, date, uid, display_name, avatar_color, wpm, duration, accuracy, created_at
		FROM daily_race_results
		WHERE date = $1
		ORDER BY wpm DESC, duration ASC
	`, dateStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []RaceResult
	rank := 1
	for rows.Next() {
		var r RaceResult
		if err := rows.Scan(&r.ID, &r.Date, &r.UID, &r.DisplayName, &r.AvatarColor, &r.WPM, &r.Duration, &r.Accuracy, &r.CreatedAt); err != nil {
			continue
		}
		r.Rank = rank
		rank++
		list = append(list, r)
	}
	return list, nil
}

// ---- Analytics & Leaderboard Implementation ----

func (s *postgresStore) GetLeaderboards(ctx context.Context, dateStr string) (*LeaderboardsResponse, error) {
	s.mu.RLock()
	if cached, ok := s.leaderboardsCache[dateStr]; ok && time.Since(cached.fetchedAt) < 15*time.Second && cached.resp != nil {
		resp := cached.resp
		s.mu.RUnlock()
		return resp, nil
	}
	s.mu.RUnlock()

	resp := &LeaderboardsResponse{
		DailyRace:   make([]LeaderboardEntry, 0),
		TopWPM:      make([]LeaderboardEntry, 0),
		TopStreaks:  make([]LeaderboardEntry, 0),
		TotalVerses: make([]LeaderboardEntry, 0),
	}

	// 1. Daily Race
	daily, err := s.GetDailyRaceResults(ctx, dateStr)
	if err == nil {
		for i, r := range daily {
			if i >= 10 {
				break
			}
			resp.DailyRace = append(resp.DailyRace, LeaderboardEntry{
				Rank:        r.Rank,
				UID:         r.UID,
				DisplayName: r.DisplayName,
				AvatarColor: r.AvatarColor,
				Score:       r.WPM,
				SubText:     fmt.Sprintf("%.1fs · %d%% acc", r.Duration, r.Accuracy),
			})
		}
	}

	// 2. Top Streaks
	sRows, err := s.pool.Query(ctx, `
		SELECT us.uid, COALESCE(ua.display_name, us.uid), COALESCE(ua.avatar_color, '#3b82f6'), us.current_streak, us.longest_streak, us.total_verses_typed
		FROM user_streaks us
		LEFT JOIN user_accounts ua ON ua.uid = us.uid
		ORDER BY us.current_streak DESC
		LIMIT 10
	`)
	if err == nil {
		rank := 1
		for sRows.Next() {
			var e LeaderboardEntry
			var longest, total int
			_ = sRows.Scan(&e.UID, &e.DisplayName, &e.AvatarColor, &e.Score, &longest, &total)
			e.Rank = rank
			rank++
			e.SubText = fmt.Sprintf("Longest: %dd · %d verses", longest, total)
			resp.TopStreaks = append(resp.TopStreaks, e)
		}
		sRows.Close()
	}

	// 3. Top WPM
	wRows, err := s.pool.Query(ctx, `
		SELECT ts.uid, COALESCE(ua.display_name, ts.uid), COALESCE(ua.avatar_color, '#10b981'), MAX(ts.wpm) as best_wpm
		FROM typing_sessions ts
		LEFT JOIN user_accounts ua ON ua.uid = ts.uid
		WHERE ts.wpm > 0
		GROUP BY ts.uid, ua.display_name, ua.avatar_color
		ORDER BY best_wpm DESC
		LIMIT 10
	`)
	if err == nil {
		rank := 1
		for wRows.Next() {
			var e LeaderboardEntry
			_ = wRows.Scan(&e.UID, &e.DisplayName, &e.AvatarColor, &e.Score)
			e.Rank = rank
			rank++
			e.SubText = "WPM record"
			resp.TopWPM = append(resp.TopWPM, e)
		}
		wRows.Close()
	}

	// 4. Total Verses
	vRows, err := s.pool.Query(ctx, `
		SELECT us.uid, COALESCE(ua.display_name, us.uid), COALESCE(ua.avatar_color, '#f59e0b'), us.total_verses_typed, us.total_chars_typed
		FROM user_streaks us
		LEFT JOIN user_accounts ua ON ua.uid = us.uid
		ORDER BY us.total_verses_typed DESC
		LIMIT 10
	`)
	if err == nil {
		rank := 1
		for vRows.Next() {
			var e LeaderboardEntry
			var chars int
			_ = vRows.Scan(&e.UID, &e.DisplayName, &e.AvatarColor, &e.Score, &chars)
			e.Rank = rank
			rank++
			e.SubText = fmt.Sprintf("%d chars total", chars)
			resp.TotalVerses = append(resp.TotalVerses, e)
		}
		vRows.Close()
	}

	s.mu.Lock()
	s.leaderboardsCache[dateStr] = cachedLeaderboards{resp: resp, fetchedAt: time.Now()}
	s.mu.Unlock()

	return resp, nil
}

func (s *postgresStore) GetUserAnalytics(ctx context.Context, uid string) (*UserAnalytics, error) {
	defaultName := "Typist " + uid[:min(len(uid), 4)]
	an := &UserAnalytics{
		UID:             uid,
		DisplayName:     defaultName,
		RecentSessions:  make([]SessionSummary, 0),
		PercentileSpeed: 50,
	}

	var slowerCount, totalCount int
	_ = s.pool.QueryRow(ctx, `
		WITH user_wpm AS (
			SELECT COALESCE(AVG(wpm), 0)::int AS avg_wpm, COALESCE(MAX(wpm), 0) AS top_wpm
			FROM typing_sessions
			WHERE uid = $1 AND wpm > 0
		)
		SELECT
			COALESCE((SELECT display_name FROM user_accounts WHERE uid = $1), $2),
			COALESCE((SELECT current_streak FROM user_streaks WHERE uid = $1), 0),
			COALESCE((SELECT longest_streak FROM user_streaks WHERE uid = $1), 0),
			COALESCE((SELECT total_verses_typed FROM user_streaks WHERE uid = $1), 0),
			COALESCE((SELECT total_chars_typed FROM user_streaks WHERE uid = $1), 0),
			COALESCE((SELECT total_seconds FROM user_streaks WHERE uid = $1), 0),
			uw.avg_wpm,
			uw.top_wpm,
			CASE WHEN uw.avg_wpm > 0 THEN (SELECT COUNT(*) FROM typing_sessions WHERE wpm > 0 AND wpm < uw.avg_wpm) ELSE 0 END,
			CASE WHEN uw.avg_wpm > 0 THEN (SELECT COUNT(*) FROM typing_sessions WHERE wpm > 0) ELSE 0 END
		FROM user_wpm uw
	`, uid, defaultName).Scan(
		&an.DisplayName,
		&an.CurrentStreak, &an.LongestStreak, &an.TotalVerses, &an.TotalChars, &an.TotalSeconds,
		&an.AverageWPM, &an.TopWPM,
		&slowerCount, &totalCount,
	)

	// Merge with in-memory streak cache if more recent
	s.mu.RLock()
	if st, ok := s.streakCache[uid]; ok && st.TotalVersesTyped >= an.TotalVerses {
		an.CurrentStreak = st.CurrentStreak
		an.LongestStreak = st.LongestStreak
		an.TotalVerses = st.TotalVersesTyped
		an.TotalChars = st.TotalCharsTyped
		an.TotalSeconds = st.TotalSeconds
	}
	s.mu.RUnlock()

	if an.AverageWPM > 0 && totalCount > 0 {
		an.PercentileSpeed = int(math.Round((float64(slowerCount) / float64(totalCount)) * 100))
		if an.PercentileSpeed < 5 {
			an.PercentileSpeed = 5
		}
		if an.PercentileSpeed > 99 {
			an.PercentileSpeed = 99
		}
	}

	// Recent sessions
	rows, err := s.pool.Query(ctx, `
		SELECT book_name, wpm, chars_typed, started_at
		FROM typing_sessions
		WHERE uid = $1 AND wpm > 0
		ORDER BY started_at DESC
		LIMIT 10
	`, uid)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sess SessionSummary
			if err := rows.Scan(&sess.BookName, &sess.WPM, &sess.Chars, &sess.Date); err == nil {
				an.RecentSessions = append(an.RecentSessions, sess)
			}
		}
	}

	return an, nil
}

func (s *postgresStore) GetCrowdMetrics(ctx context.Context) (*CrowdMetrics, error) {
	s.mu.RLock()
	if s.crowdMetricsCache != nil && time.Since(s.crowdMetricsAt) < 15*time.Second {
		cp := *s.crowdMetricsCache
		s.mu.RUnlock()
		return &cp, nil
	}
	s.mu.RUnlock()

	cm := &CrowdMetrics{
		TypersByBook: make(map[string]int),
		TrendingBook: "Genesis",
	}

	_ = s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE started_at >= CURRENT_TIMESTAMP - interval '1 hour'),
			COUNT(*) FILTER (WHERE started_at >= CURRENT_DATE)
		FROM typing_sessions
	`).Scan(&cm.TotalVersesGlobal, &cm.VersesInLastHour, &cm.TodayVersesGlobal)

	s.mu.Lock()
	cp := *cm
	s.crowdMetricsCache = &cp
	s.crowdMetricsAt = time.Now()
	s.mu.Unlock()

	return cm, nil
}

// ---- Verse Completion Counts & Sanctuary Customizations Implementation ----

func (s *postgresStore) GetVerseCompletions(ctx context.Context, uid, translation, bookName string, chapter, verse int) (int, int, error) {
	gKey := fmt.Sprintf("%s|%s:%d:%d", translation, bookName, chapter, verse)
	uKey := fmt.Sprintf("%s|%s", uid, gKey)

	s.mu.RLock()
	uCount, uCached := s.userVerseCompCache[uKey]
	gCount, gCached := s.globalVerseCompCache[gKey]
	s.mu.RUnlock()

	if uCached && gCached {
		return uCount, gCount, nil
	}

	userCount := 0
	globalCount := 0
	_ = s.pool.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT times_completed FROM verse_completions WHERE uid = $1 AND translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5), 0),
			COALESCE((SELECT times_completed FROM global_verse_completions WHERE translation = $2 AND book_name = $3 AND chapter = $4 AND verse = $5), 0)
	`, uid, translation, bookName, chapter, verse).Scan(&userCount, &globalCount)

	s.mu.Lock()
	if dirtyU, ok := s.dirtyUserVerseComps[uKey]; ok {
		userCount += dirtyU.delta
	}
	if dirtyG, ok := s.dirtyGlobalVerseComps[gKey]; ok {
		globalCount += dirtyG.delta
	}
	s.userVerseCompCache[uKey] = userCount
	s.globalVerseCompCache[gKey] = globalCount
	s.mu.Unlock()

	return userCount, globalCount, nil
}

func (s *postgresStore) RecordVerseCompletion(ctx context.Context, uid, translation, bookName string, chapter, verse int) (int, int, error) {
	gKey := fmt.Sprintf("%s|%s:%d:%d", translation, bookName, chapter, verse)
	uKey := fmt.Sprintf("%s|%s", uid, gKey)
	isBot := uid == "" || strings.HasPrefix(uid, "angel-")

	s.mu.Lock()
	gCount := s.globalVerseCompCache[gKey] + 1
	s.globalVerseCompCache[gKey] = gCount
	gDirty := s.dirtyGlobalVerseComps[gKey]
	gDirty.translation = translation
	gDirty.bookName = bookName
	gDirty.chapter = chapter
	gDirty.verse = verse
	gDirty.delta++
	s.dirtyGlobalVerseComps[gKey] = gDirty

	uCount := 0
	if !isBot {
		uCount = s.userVerseCompCache[uKey] + 1
		s.userVerseCompCache[uKey] = uCount
		uDirty := s.dirtyUserVerseComps[uKey]
		uDirty.uid = uid
		uDirty.translation = translation
		uDirty.bookName = bookName
		uDirty.chapter = chapter
		uDirty.verse = verse
		uDirty.delta++
		s.dirtyUserVerseComps[uKey] = uDirty
	}
	s.mu.Unlock()

	return uCount, gCount, nil
}

func (s *postgresStore) GetCustomizations(ctx context.Context, uid string) (*SanctuarySettings, error) {
	s.mu.RLock()
	if cached, ok := s.sanctuaryCache[uid]; ok {
		cp := cached
		s.mu.RUnlock()
		return &cp, nil
	}
	s.mu.RUnlock()

	var set SanctuarySettings
	var angelLvlsJSON, demonLvlsJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT text_color, font_family, caret_aura, audio_profile, celebrate_fx,
		       guardian_name, guardian_pace, guardian_level, guardian_xp, demons_defeated,
		       COALESCE(passage_burst_damage, 30),
		       COALESCE(summon_tokens, 0),
		       COALESCE(tokens_spent, 0),
		       COALESCE(synced_letters, 0),
		       COALESCE(synced_words, 0),
		       COALESCE(synced_verses, 0),
		       COALESCE(synced_books, 0),
		       COALESCE(guardian_book_name, ''),
		       COALESCE(guardian_chapter, 0),
		       COALESCE(guardian_verse, 0),
		       COALESCE(preferred_angel_levels, '{}'::jsonb),
		       COALESCE(preferred_demon_levels, '{}'::jsonb)
		FROM sanctuary_settings WHERE uid = $1
	`, uid).Scan(
		&set.TextColor, &set.FontFamily, &set.CaretAura, &set.AudioProfile, &set.CelebrateFx,
		&set.GuardianName, &set.GuardianPace, &set.GuardianLevel, &set.GuardianXP, &set.DemonsDefeated,
		&set.PassageBurstDamage, &set.SummonTokens, &set.TokensSpent,
		&set.SyncedLetters, &set.SyncedWords, &set.SyncedVerses, &set.SyncedBooks,
		&set.GuardianBookName, &set.GuardianChapter, &set.GuardianVerse,
		&angelLvlsJSON, &demonLvlsJSON,
	)
	if err != nil {
		def := SanctuarySettings{
			TextColor:          "emerald",
			FontFamily:         "sans",
			CaretAura:          "default",
			AudioProfile:       "classic",
			CelebrateFx:        "confetti",
			GuardianName:       "Guardian Angel",
			GuardianPace:       5,
			GuardianLevel:      1,
			GuardianXP:         0,
			DemonsDefeated:     0,
			GuardianWards:      0,
			GuardianPower:      10,
			GuardianRadiance:   100,
			PassageBurstDamage: GetDemonSyncBalance().PassageBurstBaseDamage,
			TrackerUnlocked:    false,
		}
		s.mu.Lock()
		s.sanctuaryCache[uid] = def
		s.mu.Unlock()
		return &def, nil
	}
	if len(angelLvlsJSON) > 0 {
		_ = json.Unmarshal(angelLvlsJSON, &set.PreferredAngelLevels)
	}
	if len(demonLvlsJSON) > 0 {
		_ = json.Unmarshal(demonLvlsJSON, &set.PreferredDemonLevels)
	}
	if set.GuardianLevel < 1 {
		set.GuardianLevel = 1
	}
	if set.PassageBurstDamage <= 0 {
		set.PassageBurstDamage = GetDemonSyncBalance().PassageBurstBaseDamage
	}
	set.GuardianWards = (set.GuardianLevel - 1) / 3
	set.GuardianPower = 10 + (set.GuardianLevel-1)*2
	set.GuardianRadiance = 100 + (set.GuardianLevel-1)*5
	set.TrackerUnlocked = set.DemonsDefeated >= 6

	s.mu.Lock()
	if _, dirty := s.dirtySanctuary[uid]; !dirty {
		s.sanctuaryCache[uid] = set
	} else {
		set = s.sanctuaryCache[uid]
	}
	s.mu.Unlock()

	return &set, nil
}

func (s *postgresStore) SaveCustomizations(ctx context.Context, uid string, settings SanctuarySettings) error {
	if settings.GuardianLevel < 1 {
		settings.GuardianLevel = 1
	}
	if settings.PassageBurstDamage <= 0 {
		settings.PassageBurstDamage = GetDemonSyncBalance().PassageBurstBaseDamage
	}
	settings.GuardianWards = (settings.GuardianLevel - 1) / 3
	settings.GuardianPower = 10 + (settings.GuardianLevel-1)*2
	settings.GuardianRadiance = 100 + (settings.GuardianLevel-1)*5
	settings.TrackerUnlocked = settings.DemonsDefeated >= 6

	s.mu.Lock()
	s.sanctuaryCache[uid] = settings
	s.dirtySanctuary[uid] = settings
	s.mu.Unlock()
	return nil
}

func (s *postgresStore) flushSanctuaryRow(ctx context.Context, uid string, settings SanctuarySettings) error {
	_ = s.EnsureUser(ctx, uid)
	if settings.GuardianLevel < 1 {
		settings.GuardianLevel = 1
	}
	if settings.PassageBurstDamage <= 0 {
		settings.PassageBurstDamage = GetDemonSyncBalance().PassageBurstBaseDamage
	}
	angelLvlsJSON, _ := json.Marshal(settings.PreferredAngelLevels)
	if len(angelLvlsJSON) == 0 || string(angelLvlsJSON) == "null" {
		angelLvlsJSON = []byte("{}")
	}
	demonLvlsJSON, _ := json.Marshal(settings.PreferredDemonLevels)
	if len(demonLvlsJSON) == 0 || string(demonLvlsJSON) == "null" {
		demonLvlsJSON = []byte("{}")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sanctuary_settings (
			uid, text_color, font_family, caret_aura, audio_profile, celebrate_fx,
			guardian_name, guardian_pace, guardian_level, guardian_xp,
			demons_defeated, passage_burst_damage, summon_tokens, tokens_spent,
			synced_letters, synced_words, synced_verses, synced_books,
			guardian_book_name, guardian_chapter, guardian_verse,
			preferred_angel_levels, preferred_demon_levels, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, CURRENT_TIMESTAMP)
		ON CONFLICT (uid) DO UPDATE SET
			text_color = EXCLUDED.text_color,
			font_family = EXCLUDED.font_family,
			caret_aura = EXCLUDED.caret_aura,
			audio_profile = EXCLUDED.audio_profile,
			celebrate_fx = EXCLUDED.celebrate_fx,
			guardian_name = EXCLUDED.guardian_name,
			guardian_pace = EXCLUDED.guardian_pace,
			guardian_level = EXCLUDED.guardian_level,
			guardian_xp = EXCLUDED.guardian_xp,
			demons_defeated = EXCLUDED.demons_defeated,
			passage_burst_damage = EXCLUDED.passage_burst_damage,
			summon_tokens = EXCLUDED.summon_tokens,
			tokens_spent = EXCLUDED.tokens_spent,
			synced_letters = EXCLUDED.synced_letters,
			synced_words = EXCLUDED.synced_words,
			synced_verses = EXCLUDED.synced_verses,
			synced_books = EXCLUDED.synced_books,
			guardian_book_name = EXCLUDED.guardian_book_name,
			guardian_chapter = EXCLUDED.guardian_chapter,
			guardian_verse = EXCLUDED.guardian_verse,
			preferred_angel_levels = EXCLUDED.preferred_angel_levels,
			preferred_demon_levels = EXCLUDED.preferred_demon_levels,
			updated_at = CURRENT_TIMESTAMP
	`, uid, settings.TextColor, settings.FontFamily, settings.CaretAura, settings.AudioProfile, settings.CelebrateFx,
		settings.GuardianName, settings.GuardianPace, settings.GuardianLevel, settings.GuardianXP,
		settings.DemonsDefeated, settings.PassageBurstDamage, settings.SummonTokens, settings.TokensSpent,
		settings.SyncedLetters, settings.SyncedWords, settings.SyncedVerses, settings.SyncedBooks,
		settings.GuardianBookName, settings.GuardianChapter, settings.GuardianVerse,
		angelLvlsJSON, demonLvlsJSON)
	return err
}

func (s *postgresStore) RecordWildDemonDefeat(ctx context.Context, uid string, demonRank string, demonLevel int) (*SanctuarySettings, bool, error) {
	cfg, err := s.GetCustomizations(ctx, uid)
	if err != nil {
		return nil, false, err
	}

	cfg.DemonsDefeated++
	if adm := getGlobalAdmin(); adm != nil {
		if tpd := adm.GetOptions().TokensPerDemonDefeated; tpd > 0 {
			cfg.SummonTokens += tpd
		}
	}
	if demonLevel < 1 {
		demonLevel = 1
	}
	xpGained := 100 + (demonLevel-1)*30
	cfg.GuardianXP += xpGained

	leveledUp := false
	neededXP := cfg.GuardianLevel * 100
	for cfg.GuardianXP >= neededXP && cfg.GuardianLevel < 777 {
		cfg.GuardianXP -= neededXP
		cfg.GuardianLevel++
		leveledUp = true
		neededXP = cfg.GuardianLevel * 100
	}

	cfg.GuardianWards = (cfg.GuardianLevel - 1) / 3
	cfg.GuardianPower = 10 + (cfg.GuardianLevel-1)*2
	cfg.GuardianRadiance = 100 + (cfg.GuardianLevel-1)*5
	cfg.TrackerUnlocked = cfg.DemonsDefeated >= 6

	if err := s.SaveCustomizations(ctx, uid, *cfg); err != nil {
		return nil, false, err
	}
	return cfg, leveledUp, nil
}

func (s *postgresStore) RecordGuardianSyncProgress(ctx context.Context, uid string, syncedLetters, syncedWords, versesCompleted, booksCompleted, bonusXP, tokensEarned int) (*SanctuarySettings, int, bool, error) {
	cfg, err := s.GetCustomizations(ctx, uid)
	if err != nil {
		return nil, 0, false, err
	}
	if cfg.GuardianLevel < 1 {
		cfg.GuardianLevel = 1
	}
	if syncedLetters > 0 {
		cfg.SyncedLetters += syncedLetters
	}
	if syncedWords > 0 {
		cfg.SyncedWords += syncedWords
	}
	if versesCompleted > 0 {
		cfg.SyncedVerses += versesCompleted
	}
	if booksCompleted > 0 {
		cfg.SyncedBooks += booksCompleted
	}
	if tokensEarned > 0 {
		cfg.SummonTokens += tokensEarned
	}

	xpGained := computeGuardianSyncXPGained(syncedLetters, syncedWords, versesCompleted, booksCompleted, bonusXP)
	cfg.GuardianXP += xpGained

	leveledUp := false
	neededXP := cfg.GuardianLevel * 100
	for cfg.GuardianXP >= neededXP && cfg.GuardianLevel < 777 {
		cfg.GuardianXP -= neededXP
		cfg.GuardianLevel++
		leveledUp = true
		neededXP = cfg.GuardianLevel * 100
	}

	cfg.GuardianWards = (cfg.GuardianLevel - 1) / 3
	cfg.GuardianPower = 10 + (cfg.GuardianLevel-1)*2
	cfg.GuardianRadiance = 100 + (cfg.GuardianLevel-1)*5
	cfg.TrackerUnlocked = cfg.DemonsDefeated >= 6

	if err := s.SaveCustomizations(ctx, uid, *cfg); err != nil {
		return nil, 0, false, err
	}
	return cfg, xpGained, leveledUp, nil
}

func (s *postgresStore) GetBossRaid(ctx context.Context) (*BossRaid, error) {
	var b BossRaid
	var rawConquerors []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, boss_name, boss_title, boss_rank, boss_wpm, COALESCE(level, 2), COALESCE(max_level, 4), COALESCE(is_demon_boss, false),
		       max_hp, current_hp, required_wpm, translation, book_name, chapter, verse, text, conquered, conquerors, updated_at
		FROM boss_raids
		ORDER BY updated_at DESC
		LIMIT 1
	`).Scan(&b.ID, &b.BossName, &b.BossTitle, &b.BossRank, &b.BossWPM, &b.Level, &b.MaxLevel, &b.IsDemonBoss,
		&b.MaxHP, &b.CurrentHP, &b.RequiredWPM, &b.Translation, &b.BookName, &b.Chapter, &b.Verse, &b.Text, &b.Conquered, &rawConquerors, &b.UpdatedAt)
	if err != nil {
		def := defaultBossRaid()
		_ = s.ResetBossRaid(ctx, *def)
		return def, nil
	}
	if len(rawConquerors) > 0 {
		_ = json.Unmarshal(rawConquerors, &b.Conquerors)
	}
	if b.Conquerors == nil {
		b.Conquerors = []BossConqueror{}
	}
	if b.BossName == "Archangel Michael" {
		b.BossName = "Big Mike"
	}
	return &b, nil
}

func (s *postgresStore) GetBossRaidForLevel(ctx context.Context, level int, isDemon bool) (*BossRaid, error) {
	var b BossRaid
	var rawConquerors []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, boss_name, boss_title, boss_rank, boss_wpm, max_hp, current_hp, required_wpm,
		       translation, book_name, chapter, verse, text, conquered, conquerors, updated_at
		FROM boss_raids
		WHERE level = $1 AND is_demon_boss = $2
		ORDER BY updated_at DESC
		LIMIT 1
	`, level, isDemon).Scan(&b.ID, &b.BossName, &b.BossTitle, &b.BossRank, &b.BossWPM, &b.MaxHP, &b.CurrentHP, &b.RequiredWPM,
		&b.Translation, &b.BookName, &b.Chapter, &b.Verse, &b.Text, &b.Conquered, &rawConquerors, &b.UpdatedAt)
	if err != nil {
		def := defaultBossRaidForLevel(level, isDemon)
		_ = s.ResetBossRaid(ctx, *def)
		return def, nil
	}
	if len(rawConquerors) > 0 {
		_ = json.Unmarshal(rawConquerors, &b.Conquerors)
	}
	if b.Conquerors == nil {
		b.Conquerors = []BossConqueror{}
	}
	if b.BossName == "Archangel Michael" {
		b.BossName = "Big Mike"
	}
	b.Level = level
	b.MaxLevel = 4
	b.IsDemonBoss = isDemon
	return &b, nil
}

func (s *postgresStore) RecordBossStrike(ctx context.Context, uid, displayName, avatarColor string, wpm, damage int) (*BossRaid, error) {
	conq := BossConqueror{
		UID:         uid,
		DisplayName: displayName,
		AvatarColor: avatarColor,
		WPM:         wpm,
		Damage:      damage,
		Timestamp:   time.Now().UTC(),
	}
	conqJSON, _ := json.Marshal([]BossConqueror{conq})

	var b BossRaid
	var rawConquerors []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE boss_raids
		SET current_hp = GREATEST(0, current_hp - $1),
		    conquered = (current_hp - $1 <= 0),
		    conquerors = ($2::jsonb || COALESCE(conquerors, '[]'::jsonb)),
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = (SELECT id FROM boss_raids ORDER BY updated_at DESC LIMIT 1)
		RETURNING id, boss_name, boss_title, boss_rank, boss_wpm, COALESCE(level, 2), COALESCE(max_level, 4), COALESCE(is_demon_boss, false),
		          max_hp, current_hp, required_wpm, translation, book_name, chapter, verse, text, conquered, conquerors, updated_at
	`, damage, conqJSON).Scan(
		&b.ID, &b.BossName, &b.BossTitle, &b.BossRank, &b.BossWPM, &b.Level, &b.MaxLevel, &b.IsDemonBoss,
		&b.MaxHP, &b.CurrentHP, &b.RequiredWPM, &b.Translation, &b.BookName, &b.Chapter, &b.Verse, &b.Text, &b.Conquered, &rawConquerors, &b.UpdatedAt,
	)
	if err != nil {
		def := defaultBossRaid()
		_ = s.ResetBossRaid(ctx, *def)
		return def, nil
	}
	if len(rawConquerors) > 0 {
		_ = json.Unmarshal(rawConquerors, &b.Conquerors)
	}
	if len(b.Conquerors) > 50 {
		b.Conquerors = b.Conquerors[:50]
	}
	if b.BossName == "Archangel Michael" {
		b.BossName = "Big Mike"
	}
	return &b, nil
}

func (s *postgresStore) ResetBossRaid(ctx context.Context, raid BossRaid) error {
	if raid.Level < 1 {
		raid.Level = 2
	}
	if raid.MaxLevel < 1 {
		raid.MaxLevel = 4
	}
	raw, _ := json.Marshal(raid.Conquerors)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO boss_raids (id, boss_name, boss_title, boss_rank, boss_wpm, level, max_level, is_demon_boss,
		                        max_hp, current_hp, required_wpm, translation, book_name, chapter, verse, text, conquered, conquerors, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, CURRENT_TIMESTAMP)
		ON CONFLICT (id) DO UPDATE SET
			boss_name = EXCLUDED.boss_name,
			boss_title = EXCLUDED.boss_title,
			boss_rank = EXCLUDED.boss_rank,
			boss_wpm = EXCLUDED.boss_wpm,
			level = EXCLUDED.level,
			max_level = EXCLUDED.max_level,
			is_demon_boss = EXCLUDED.is_demon_boss,
			max_hp = EXCLUDED.max_hp,
			current_hp = EXCLUDED.current_hp,
			required_wpm = EXCLUDED.required_wpm,
			translation = EXCLUDED.translation,
			book_name = EXCLUDED.book_name,
			chapter = EXCLUDED.chapter,
			verse = EXCLUDED.verse,
			text = EXCLUDED.text,
			conquered = EXCLUDED.conquered,
			conquerors = EXCLUDED.conquerors,
			updated_at = CURRENT_TIMESTAMP
	`, raid.ID, raid.BossName, raid.BossTitle, raid.BossRank, raid.BossWPM, raid.Level, raid.MaxLevel, raid.IsDemonBoss,
		raid.MaxHP, raid.CurrentHP, raid.RequiredWPM, raid.Translation, raid.BookName, raid.Chapter, raid.Verse, raid.Text, raid.Conquered, raw)
	return err
}

func (s *postgresStore) ListGuardianSeedEntries(ctx context.Context) ([]GuardianSeedEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(ua.uid, us.uid) AS uid,
		       COALESCE(ua.display_name, 'Scribe'),
		       COALESCE(ua.avatar_color, '#a855f7'),
		       COALESCE(NULLIF(us.guardian_book_name, ''), 'Genesis'),
		       CASE WHEN COALESCE(us.guardian_chapter, 0) > 0 THEN us.guardian_chapter ELSE 1 END,
		       CASE WHEN COALESCE(us.guardian_verse, 0) > 0 THEN us.guardian_verse ELSE 1 END
		FROM user_accounts ua
		FULL OUTER JOIN sanctuary_settings us ON ua.uid = us.uid
		WHERE COALESCE(ua.uid, us.uid) NOT LIKE 'angel-%'
		  AND COALESCE(ua.uid, us.uid) NOT LIKE 'demon-%'
		ORDER BY COALESCE(ua.last_active, us.updated_at) DESC NULLS LAST
		LIMIT 16
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]GuardianSeedEntry, 0, 16)
	for rows.Next() {
		var e GuardianSeedEntry
		if err := rows.Scan(&e.UID, &e.Name, &e.Color, &e.BookName, &e.Chapter, &e.Verse); err == nil && e.UID != "" {
			entries = append(entries, e)
		}
	}
	return entries, nil
}


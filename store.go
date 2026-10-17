package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

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
	Type        string    `json:"type"` // "book", "streak", "milestone", "special", "speed"
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

// getNextCanonicalVerse returns the next canonical verse across all 31,102 verses in Scripture
func getNextCanonicalVerse(trans, book string, chapter, verse int) (Verse, bool) {
	td, ok := translations[trans]
	if !ok || len(td.Verses) == 0 {
		td, ok = translations["kjv"]
		if !ok || len(td.Verses) == 0 {
			return Verse{}, false
		}
	}
	for i, v := range td.Verses {
		if v.BookName == book && v.Chapter == chapter && v.Verse == verse {
			nextIdx := (i + 1) % len(td.Verses)
			return td.Verses[nextIdx], true
		}
	}
	// Fallback to book list if specific verse was not matched directly
	if vList, ok := td.VersesByBook[book]; ok && len(vList) > 0 {
		for i, v := range vList {
			if v.Chapter == chapter && v.Verse == verse {
				if i+1 < len(vList) {
					return vList[i+1], true
				}
				for bIdx, bName := range td.CanonicalBooks {
					if bName == book {
						nextBook := td.CanonicalBooks[(bIdx+1)%len(td.CanonicalBooks)]
						if nextList, ok := td.VersesByBook[nextBook]; ok && len(nextList) > 0 {
							return nextList[0], true
						}
					}
				}
			}
		}
	}
	return td.Verses[0], true
}

// ---- Accounts & Identity Models ----

type UserAccount struct {
	UID          string    `json:"uid"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	AvatarColor  string    `json:"avatar_color"`
	CreatedAt    time.Time `json:"created_at"`
	LastActive   time.Time `json:"last_active"`
}

// ---- Verse Comments & Reflection Models ----

type VerseComment struct {
	ID          int            `json:"id"`
	UID         string         `json:"uid"`
	DisplayName string         `json:"display_name"`
	AvatarColor string         `json:"avatar_color"`
	StreakDays  int            `json:"streak_days"`
	Translation string         `json:"translation"`
	BookName    string         `json:"book_name"`
	Chapter     int            `json:"chapter"`
	Verse       int            `json:"verse"`
	PromptType  string         `json:"prompt_type"` // "reflection", "prayer", "testimony", "general"
	Content     string         `json:"content"`
	IsPrivate   bool           `json:"is_private"` // true for personal journal, false for community
	ParentID    int            `json:"parent_id"`  // 0 for top-level, >0 for replies
	Reactions   map[string]int `json:"reactions"`  // "amen", "heart", "pray", etc.
	Flagged     bool           `json:"flagged"`
	CreatedAt   time.Time      `json:"created_at"`
}

// ---- Live Fellowship Chat Models ----

type ChatMessage struct {
	ID          int       `json:"id"`
	UID         string    `json:"uid"`
	DisplayName string    `json:"display_name"`
	AvatarColor string    `json:"avatar_color"`
	Channel     string    `json:"channel"` // "global" or book name
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

// ---- Daily Challenge & Racing Models ----

type DailyChallenge struct {
	Date        string `json:"date"` // YYYY-MM-DD
	Translation string `json:"translation"`
	BookName    string `json:"book_name"`
	Chapter     int    `json:"chapter"`
	Verse       int    `json:"verse"`
	Text        string `json:"text"`
}

type RaceResult struct {
	ID          int       `json:"id"`
	Date        string    `json:"date"`
	UID         string    `json:"uid"`
	DisplayName string    `json:"display_name"`
	AvatarColor string    `json:"avatar_color"`
	WPM         int       `json:"wpm"`
	Duration    float64   `json:"duration"`
	Accuracy    int       `json:"accuracy"`
	Rank        int       `json:"rank,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// ---- Analytics & Leaderboard Models ----

type LeaderboardEntry struct {
	Rank        int    `json:"rank"`
	UID         string `json:"uid"`
	DisplayName string `json:"display_name"`
	AvatarColor string `json:"avatar_color"`
	Score       int    `json:"score"`
	SubText     string `json:"sub_text"`
}

type LeaderboardsResponse struct {
	DailyRace   []LeaderboardEntry `json:"daily_race"`
	TopWPM      []LeaderboardEntry `json:"top_wpm"`
	TopStreaks  []LeaderboardEntry `json:"top_streaks"`
	TotalVerses []LeaderboardEntry `json:"total_verses"`
}

type SessionSummary struct {
	BookName string    `json:"book_name"`
	WPM      int       `json:"wpm"`
	Chars    int       `json:"chars"`
	Date     time.Time `json:"date"`
}

type UserAnalytics struct {
	UID             string           `json:"uid"`
	DisplayName     string           `json:"display_name"`
	TotalVerses     int              `json:"total_verses"`
	TotalChars      int              `json:"total_chars"`
	TotalSeconds    int              `json:"total_seconds"`
	AverageWPM      int              `json:"avg_wpm"`
	TopWPM          int              `json:"top_wpm"`
	CurrentStreak   int              `json:"current_streak"`
	LongestStreak   int              `json:"longest_streak"`
	PercentileSpeed int              `json:"percentile_speed"` // e.g. 85 -> faster than 85% of typists
	RecentSessions  []SessionSummary `json:"recent_sessions"`
}

type CrowdMetrics struct {
	ActiveTypers      int            `json:"active_typers"`
	VersesInLastHour  int            `json:"verses_in_last_hour"`
	TodayVersesGlobal int            `json:"today_verses_global"`
	TotalVersesGlobal int            `json:"total_verses_global"`
	TypersByBook      map[string]int `json:"typers_by_book"`
	TrendingBook      string         `json:"trending_book"`
}

// ---- Sanctuary & Unlockable Customization Models ----

type SanctuarySettings struct {
	TextColor    string `json:"text_color"`    // "emerald", "amber", "sapphire", "amethyst", "crimson", "cyan", "gold"
	FontFamily   string `json:"font_family"`   // "sans", "serif", "mono", "illuminated"
	CaretAura    string `json:"caret_aura"`    // "default", "halo", "flame", "starlight"
	AudioProfile string `json:"audio_profile"` // "classic", "typewriter", "chime", "whisper"
	CelebrateFx  string `json:"celebrate_fx"`  // "confetti", "gold", "feathers", "light"
	GuardianName         string         `json:"guardian_name,omitempty"` // Custom name for personal guardian angel
	GuardianPace         int            `json:"guardian_pace,omitempty"` // Offset % from user's WPM (-10 to +10, e.g. +5)
	GuardianLevel        int            `json:"guardian_level,omitempty"`
	GuardianXP           int            `json:"guardian_xp,omitempty"`
	DemonsDefeated       int            `json:"demons_defeated,omitempty"`   // Total wild demons defeated (tracker unlocks at 6)
	GuardianWards        int            `json:"guardian_wards,omitempty"`    // Mistake absorption shields before reset
	GuardianPower        int            `json:"guardian_power,omitempty"`    // Holy resonance strike bonus damage
	GuardianRadiance     int            `json:"guardian_radiance,omitempty"` // Shroud dissipation rate %
	TrackerUnlocked      bool           `json:"tracker_unlocked,omitempty"`  // Unlocked after defeating 6 demons in the wild
	PreferredAngelLevels map[string]int `json:"preferred_angel_levels,omitempty"`
	PreferredDemonLevels map[string]int `json:"preferred_demon_levels,omitempty"`
}

type CustomizationUnlock struct {
	ID          string `json:"id"`
	Category    string `json:"category"` // "color", "font", "aura", "audio", "fx"
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
	Requirement string `json:"requirement"`
	Unlocked    bool   `json:"unlocked"`
	Preview     string `json:"preview,omitempty"`
}

// ---- Heavenly Angel Models ----

type AngelInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Rank        string  `json:"rank"` // "guardian", "cherub", "seraph", "gabriel", "michael"
	Title       string  `json:"title"`
	Description string  `json:"description"`
	WPM         int     `json:"wpm"`
	Color       string  `json:"color"`
	Icon        string  `json:"icon"`
	BookName    string  `json:"book_name,omitempty"`
	Chapter     int     `json:"chapter,omitempty"`
	Verse       int     `json:"verse,omitempty"`
	CharIndex   int     `json:"char_index,omitempty"`
	Progress    float64 `json:"progress,omitempty"`
	IsGuardian  bool    `json:"is_guardian,omitempty"`
	PaceOffset  int     `json:"pace_offset,omitempty"`
	Level       int     `json:"level,omitempty"`
	MaxLevel    int     `json:"max_level,omitempty"`
	IsDemon     bool    `json:"is_demon,omitempty"`
}

// ---- Demonic Adversary Models ----

type DemonInfo struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Rank          string  `json:"rank"` // "imp", "fiend", "tormentor", "apollyon"
	Title         string  `json:"title"`
	Description   string  `json:"description"`
	Level         int     `json:"level"`
	MaxLevel      int     `json:"max_level"`
	WPM           int     `json:"wpm"`
	Color         string  `json:"color"`
	Icon          string  `json:"icon"`
	BlockDuration float64 `json:"block_duration"`
	BookName      string  `json:"book_name,omitempty"`
	Chapter       int     `json:"chapter,omitempty"`
	Verse         int     `json:"verse,omitempty"`
	CharIndex     int     `json:"char_index,omitempty"`
	Progress      float64 `json:"progress,omitempty"`
	IsBoss        bool    `json:"is_boss,omitempty"`
	TotalHP       int     `json:"total_hp,omitempty"`
	CurrentHP     int     `json:"current_hp,omitempty"`
}

type WildDemon struct {
	ID          string    `json:"id"`
	Rank        string    `json:"rank"` // "imp", "fiend", "tormentor", "apollyon"
	Level       int       `json:"level"`
	MaxLevel    int       `json:"max_level,omitempty"`
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Icon        string    `json:"icon"`
	Color       string    `json:"color"`
	Translation string    `json:"translation"`
	BookName    string    `json:"book_name"`
	Chapter     int       `json:"chapter"`
	Verse       int       `json:"verse"`
	Text        string    `json:"text"`
	WPM         int       `json:"wpm"` // slow methodical pace: 10 - 24 WPM
	MaxHP       int       `json:"max_hp"`
	CurrentHP   int       `json:"current_hp"`
	EnrageCount int       `json:"enrage_count"`
	CharIndex   int       `json:"char_index"`
	SpawnedAt   time.Time `json:"spawned_at,omitempty"`
	LastEvolve  time.Time `json:"last_evolve,omitempty"`
	AgeMinutes  int       `json:"age_minutes,omitempty"`
}

// ---- Global Celestial & Demonic Boss Raid Models ----

type BossRaid struct {
	ID          string          `json:"id"`
	BossName    string          `json:"boss_name"`
	BossTitle   string          `json:"boss_title"`
	BossRank    string          `json:"boss_rank"`
	BossWPM     int             `json:"boss_wpm"`
	Level       int             `json:"level,omitempty"`
	MaxLevel    int             `json:"max_level,omitempty"`
	IsDemonBoss bool            `json:"is_demon_boss,omitempty"`
	MaxHP       int             `json:"max_hp"`
	CurrentHP   int             `json:"current_hp"`
	RequiredWPM int             `json:"required_wpm"`
	Translation string          `json:"translation"`
	BookName    string          `json:"book_name"`
	Chapter     int             `json:"chapter"`
	Verse       int             `json:"verse"`
	Text        string          `json:"text"`
	Conquered   bool            `json:"conquered"`
	Conquerors  []BossConqueror `json:"conquerors"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type BossConqueror struct {
	UID         string    `json:"uid"`
	DisplayName string    `json:"display_name"`
	AvatarColor string    `json:"avatar_color"`
	WPM         int       `json:"wpm"`
	Damage      int       `json:"damage"`
	Timestamp   time.Time `json:"timestamp"`
}

// ---- WebSocket & API Messages ----

type GhostCursor struct {
	UID         string `json:"uid"`
	DisplayName string `json:"display_name"`
	AvatarColor string `json:"avatar_color"`
	CharIndex   int    `json:"char_index"`
	WPM         int    `json:"wpm"`
	IsAngel     bool   `json:"is_angel,omitempty"`
	AngelRank   string `json:"angel_rank,omitempty"`
	IsDemon     bool   `json:"is_demon,omitempty"`
	DemonRank   string `json:"demon_rank,omitempty"`
	DemonLevel  int    `json:"demon_level,omitempty"`
	CastAbility string `json:"cast_ability,omitempty"` // "shroud", "typo", ""
	ShroudRange int    `json:"shroud_range,omitempty"` // number of chars to shroud
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
	Mode         string            `json:"mode,omitempty"` // "verse", "intro", "review", "favorite", "challenge"
	Books        []string          `json:"books,omitempty"`
	Progress     any               `json:"progress,omitempty"`
	Translations []TranslationMeta `json:"translations,omitempty"`
	Reward       *Badge            `json:"reward,omitempty"`
	WPM          int               `json:"wpm,omitempty"`
	Duration     float64           `json:"duration,omitempty"`
	Mandatory    bool              `json:"mandatory,omitempty"`
	ReviewLocked bool              `json:"review_locked,omitempty"`
	Category     string            `json:"category,omitempty"`

	// Auth & Identity
	Account  *UserAccount `json:"account,omitempty"`
	Username string       `json:"username,omitempty"`
	Password string       `json:"password,omitempty"`
	Email    string       `json:"email,omitempty"`
	Color    string       `json:"color,omitempty"`
	AnonUID  string       `json:"anon_uid,omitempty"`

	// Daily Challenge & Racing
	Challenge *DailyChallenge `json:"challenge,omitempty"`
	RaceRank  int             `json:"race_rank,omitempty"`

	// Comments & Reflections
	CommentID  int            `json:"comment_id,omitempty"`
	Comment    *VerseComment  `json:"comment,omitempty"`
	Comments   []VerseComment `json:"comments,omitempty"`
	PromptType string         `json:"prompt_type,omitempty"`
	IsPrivate  bool           `json:"is_private,omitempty"`
	ParentID   int            `json:"parent_id,omitempty"`
	Reaction   string         `json:"reaction,omitempty"`

	// Live Chat
	Channel     string        `json:"channel,omitempty"`
	ChatMessage *ChatMessage  `json:"chat_message,omitempty"`
	ChatHistory []ChatMessage `json:"chat_history,omitempty"`

	// Presence & Cursors
	CharIndex   int           `json:"char_index,omitempty"`
	Cursors     []GhostCursor `json:"cursors,omitempty"`
	Ghost       *GhostCursor  `json:"ghost,omitempty"`
	TypersCount int           `json:"typers_count,omitempty"`

	// Crowd Metrics & Leaderboards
	Metrics      *CrowdMetrics         `json:"metrics,omitempty"`
	Leaderboards *LeaderboardsResponse `json:"leaderboards,omitempty"`
	Analytics    *UserAnalytics        `json:"analytics,omitempty"`

	// Offline sync
	SyncItems []SyncVerseItem `json:"sync_items,omitempty"`

	// Verse Completion Counts (Personal + Global Community)
	UserCompletions   int `json:"user_completions,omitempty"`
	GlobalCompletions int `json:"global_completions,omitempty"`

	// Sanctuary & Unlockable Customizations
	Sanctuary *SanctuarySettings   `json:"sanctuary,omitempty"`
	Unlocks   []CustomizationUnlock `json:"unlocks,omitempty"`
	NewUnlock *CustomizationUnlock `json:"new_unlock,omitempty"`

	// Heavenly Angel Bot
	Angel      *AngelInfo  `json:"angel,omitempty"`
	Angels     []AngelInfo `json:"angels,omitempty"`
	AngelSpeed int         `json:"angel_speed,omitempty"`
	AngelRank  string      `json:"angel_rank,omitempty"`
	AngelLevel int         `json:"angel_level,omitempty"`

	// Demonic Adversary
	Demon                *DemonInfo  `json:"demon,omitempty"`
	Demons               []DemonInfo `json:"demons,omitempty"`
	DemonSpeed           int         `json:"demon_speed,omitempty"`
	DemonRank            string      `json:"demon_rank,omitempty"`
	DemonLevel           int         `json:"demon_level,omitempty"`
	Devil24_7VersesCount int         `json:"devil_24_7_verses_count,omitempty"`

	// Celestial & Demonic Boss Raid
	BossRaid    *BossRaid      `json:"boss_raid,omitempty"`
	BossHit     *BossConqueror `json:"boss_hit,omitempty"`
	BossLevel   int            `json:"boss_level,omitempty"`
	IsDemonBoss bool           `json:"is_demon_boss,omitempty"`

	// Wild Roaming Demons & Resonance Combat
	WildDemon       *WildDemon   `json:"wild_demon,omitempty"`
	WildDemons      []WildDemon  `json:"wild_demons,omitempty"`
	TrackerUnlocked bool         `json:"tracker_unlocked,omitempty"`
	DemonsDefeated  int          `json:"demons_defeated,omitempty"`
	DemonDefeated   bool         `json:"demon_defeated,omitempty"`
	HolyWards       int          `json:"holy_wards,omitempty"`
	DemonID         string       `json:"demon_id,omitempty"`
	Damage          int          `json:"damage,omitempty"`
	EncounterChance int          `json:"encounter_chance,omitempty"`
	IsAmbush        bool         `json:"is_ambush,omitempty"`
	ActiveDemons    int          `json:"active_demons,omitempty"`
	MaxDemons       int          `json:"max_demons,omitempty"`
	Sanctification  int          `json:"sanctification,omitempty"`
	SwarmEvent      string       `json:"swarm_event,omitempty"`
}

type SyncVerseItem struct {
	Translation string  `json:"translation"`
	BookName    string  `json:"book_name"`
	Chapter     int     `json:"chapter"`
	Verse       int     `json:"verse"`
	WPM         int     `json:"wpm"`
	Duration    float64 `json:"duration"`
	Timestamp   int64   `json:"timestamp"`
}

// ---- Store Interface ----

type Store interface {
	EnsureUser(ctx context.Context, uid string) error
	GetAllBookProgress(ctx context.Context, uid, translation string) (map[string]BookProgress, error)
	GetBookProgress(ctx context.Context, uid, translation, bookName string, totalVerses int) (*BookProgress, error)
	UpdateBookProgress(ctx context.Context, uid, translation, bookName string, bp *BookProgress) error
	EnsureBookProgressRows(ctx context.Context, uid, translation string, books []string, totals map[string]int) error
	CreateTypingSession(ctx context.Context, uid, translation, bookName string) (int, error)
	UpdateTypingSession(ctx context.Context, sessionID int, stats *RuntimeStats) error
	RecordTypingSession(ctx context.Context, uid, translation, bookName string, chars, correct, wpm int, duration float64) error

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

	// Identity & Account Linking
	RegisterAccount(ctx context.Context, username, email, passwordHash, displayName, avatarColor, anonUID string) (*UserAccount, error)
	LoginAccount(ctx context.Context, usernameOrEmail, passwordHash, anonUID string) (*UserAccount, error)
	GetAccount(ctx context.Context, uid string) (*UserAccount, error)
	UpdateProfile(ctx context.Context, uid, displayName, avatarColor string) (*UserAccount, error)
	MergeUserProgress(ctx context.Context, targetUID, sourceUID string) error

	// Comments & Reflections
	GetVerseComments(ctx context.Context, translation, bookName string, chapter, verse int, userUID string) ([]VerseComment, error)
	AddVerseComment(ctx context.Context, comment VerseComment) (*VerseComment, error)
	ReactVerseComment(ctx context.Context, commentID int, reaction string) error
	FlagVerseComment(ctx context.Context, commentID int) error

	// Live Fellowship Chat
	GetRecentChat(ctx context.Context, channel string, limit int) ([]ChatMessage, error)
	AddChatMessage(ctx context.Context, msg ChatMessage) error

	// Daily Challenge Racing
	SaveRaceResult(ctx context.Context, res RaceResult) error
	GetDailyRaceResults(ctx context.Context, dateStr string) ([]RaceResult, error)

	// Analytics & Leaderboards
	GetLeaderboards(ctx context.Context, dateStr string) (*LeaderboardsResponse, error)
	GetUserAnalytics(ctx context.Context, uid string) (*UserAnalytics, error)
	GetCrowdMetrics(ctx context.Context) (*CrowdMetrics, error)

	// Verse Completion Counts (Personal + Global Community)
	GetVerseCompletions(ctx context.Context, uid, translation, bookName string, chapter, verse int) (int, int, error)
	RecordVerseCompletion(ctx context.Context, uid, translation, bookName string, chapter, verse int) (int, int, error)

	// Sanctuary & Unlockable Customizations
	GetCustomizations(ctx context.Context, uid string) (*SanctuarySettings, error)
	SaveCustomizations(ctx context.Context, uid string, settings SanctuarySettings) error

	// Celestial & Demonic Boss Raid
	GetBossRaid(ctx context.Context) (*BossRaid, error)
	GetBossRaidForLevel(ctx context.Context, level int, isDemon bool) (*BossRaid, error)
	RecordBossStrike(ctx context.Context, uid, displayName, avatarColor string, wpm, damage int) (*BossRaid, error)
	ResetBossRaid(ctx context.Context, raid BossRaid) error

	// Wild Demons & Combat Progression
	RecordWildDemonDefeat(ctx context.Context, uid string, demonRank string, demonLevel int) (*SanctuarySettings, bool, error)
}

// Password hashing utility using SHA-256 with static application salt
func hashPassword(password string) string {
	salt := "elespe-holy-scripture-salt-v1"
	hasher := sha256.New()
	hasher.Write([]byte(salt + password))
	return hex.EncodeToString(hasher.Sum(nil))
}

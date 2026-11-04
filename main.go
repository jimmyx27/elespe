package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed index.html admin.html logo.svg manifest.json sw.js kjv.json asv.json bbe.json esv.json niv.json nlt.json hcsb.json rvr1909.json book_guides.json book_guides_es.json
var appFS embed.FS

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// publicFS serves embedded frontend assets but keeps the raw JSON datasets out of the public HTTP route,
// while allowing manifest.json and other public assets.
type publicFS struct {
	fs.FS
}

func (f publicFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".json") && name != "manifest.json" {
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
	if s.conn == nil {
		return nil
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return s.conn.WriteJSON(v)
}

func (s *safeConn) WritePing() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
}

func (s *safeConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// Global in-memory datasets & services
var (
	loadDataMu         sync.Mutex
	translations       = make(map[string]*TranslationData)
	availableTransList []TranslationMeta
	bookGuides         = make(map[string]BookGuide)
	bookGuidesES       = make(map[string]BookGuide)
	canonicalOrder     []string
	store              Store
)

func isSpanishTranslation(trans string) bool {
	t := strings.ToLower(strings.TrimSpace(trans))
	return t == "rvr1909" || strings.HasPrefix(t, "es") || strings.HasPrefix(t, "rvr")
}

func getBookGuide(trans, book string) (BookGuide, bool) {
	if isSpanishTranslation(trans) {
		if g, ok := bookGuidesES[book]; ok {
			return g, true
		}
	}
	g, ok := bookGuides[book]
	return g, ok
}

// cleanVerseText removes non-typing markers and digital tags while standardizing quotes,
// apostrophes, dashes, and whitespace for a smooth typing experience.
func cleanVerseText(s string) string {
	s = strings.ReplaceAll(s, "a\u00FC", "au")
	s = strings.ReplaceAll(s, "A\u00FC", "Au")
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) {
			sb.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if r == '\u00B6' || r == '\u2029' || r == '\u2028' || r == '\u2039' || r == '\u203A' || r == '[' || r == ']' {
			continue
		}
		if r >= 0x0590 && r <= 0x05FF {
			continue
		}
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
		if r == '\u00EF' {
			sb.WriteRune('i')
			continue
		}
		sb.WriteRune(r)
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

// normalizeSpanishForMatch folds Spanish diacritics (á->a, é->e, í->i, ó->o, ú->u, ü->u, ñ->n)
// and strips optional inverted punctuation (¿, ¡) for lenient typing comparison.
func normalizeSpanishForMatch(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch r {
		case '¿', '¡':
			continue
		case 'á', 'à', 'â', 'ä':
			sb.WriteRune('a')
		case 'é', 'è', 'ê', 'ë':
			sb.WriteRune('e')
		case 'í', 'ì', 'î', 'ï':
			sb.WriteRune('i')
		case 'ó', 'ò', 'ô', 'ö':
			sb.WriteRune('o')
		case 'ú', 'ù', 'û', 'ü':
			sb.WriteRune('u')
		case 'ñ':
			sb.WriteRune('n')
		case 'Á', 'À', 'Â', 'Ä':
			sb.WriteRune('A')
		case 'É', 'È', 'Ê', 'Ë':
			sb.WriteRune('E')
		case 'Í', 'Ì', 'Î', 'Ï':
			sb.WriteRune('I')
		case 'Ó', 'Ò', 'Ô', 'Ö':
			sb.WriteRune('O')
		case 'Ú', 'Ù', 'Û', 'Ü':
			sb.WriteRune('U')
		case 'Ñ':
			sb.WriteRune('N')
		default:
			sb.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

func matchesPassageText(userInput, targetText string) bool {
	if userInput == targetText {
		return true
	}
	return normalizeSpanishForMatch(userInput) == normalizeSpanishForMatch(targetText)
}

// normalizeSpokenForMatch folds Spanish diacritics, lowercases all characters,
// converts hyphens/dashes into spaces, strips punctuation, and normalizes whitespace
// for Voice Mode spoken passage verification.
func normalizeSpokenForMatch(s string) string {
	folded := normalizeSpanishForMatch(s)
	var sb strings.Builder
	sb.Grow(len(folded))
	for _, r := range folded {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(unicode.ToLower(r))
		} else if unicode.IsSpace(r) || r == '-' || r == '—' || r == '–' {
			sb.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

func matchesSpokenPassage(userInput, targetText string) bool {
	if matchesPassageText(userInput, targetText) {
		return true
	}
	uNorm := normalizeSpokenForMatch(userInput)
	tNorm := normalizeSpokenForMatch(targetText)
	return uNorm != "" && uNorm == tNorm
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// ---- Data Loading ----

func loadTranslationsAndGuides() error {
	loadDataMu.Lock()
	defer loadDataMu.Unlock()

	if len(translations) == 8 && len(bookGuides) == 66 && len(bookGuidesES) == 66 && len(canonicalOrder) == 66 {
		return nil
	}

	guideData, err := appFS.ReadFile("book_guides.json")
	if err != nil {
		return fmt.Errorf("reading book_guides.json: %w", err)
	}
	if err := json.Unmarshal(guideData, &bookGuides); err != nil {
		return fmt.Errorf("unmarshaling book_guides.json: %w", err)
	}

	if guideDataES, err := appFS.ReadFile("book_guides_es.json"); err == nil {
		_ = json.Unmarshal(guideDataES, &bookGuidesES)
	}

	type orderEntry struct {
		name  string
		order int
	}
	orders := make([]orderEntry, 0, len(bookGuides))
	for name, g := range bookGuides {
		orders = append(orders, orderEntry{name: name, order: g.Order})
	}
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].order < orders[j].order
	})
	canonicalOrder = make([]string, len(orders))
	for i, o := range orders {
		canonicalOrder[i] = o.name
	}

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
		{"rvr1909.json", "rvr1909", "Reina-Valera (1909) — Español", "RVR1909"},
	}

	availableTransList = nil
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
	if wpm >= 80 {
		tryAward(Badge{
			ID:          "speed_80",
			Type:        "speed",
			Name:        "Radiant Velocity",
			Description: "Achieved 80+ Words Per Minute! Unlocked Radiant Amethyst completed text color.",
			Icon:        "🔮",
			EarnedAt:    now,
		})
	}
	if wpm >= 100 {
		tryAward(Badge{
			ID:          "speed_100",
			Type:        "speed",
			Name:        "Gabriel's Herald",
			Description: "Achieved 100+ Words Per Minute! Inscribing scripture with heavenly swiftness.",
			Icon:        "🎺",
			EarnedAt:    now,
		})
	}
	if wpm >= 160 {
		tryAward(Badge{
			ID:          "speed_160",
			Type:        "speed",
			Name:        "Sword of Michael",
			Description: "Achieved 160+ Words Per Minute! Inscribing at Big Mike lightning speed!",
			Icon:        "⚔️",
			EarnedAt:    now,
		})
	}

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
	if mode == "challenge" {
		tryAward(Badge{
			ID:          "daily_racer",
			Type:        "special",
			Name:        "Daily Champion",
			Description: "Completed a Verse of the Day Challenge Race!",
			Icon:        "🏆",
			EarnedAt:    now,
		})
	}
	if mode == "voice" {
		tryAward(Badge{
			ID:          "voice_herald",
			Type:        "special",
			Name:        "Voice of the Herald",
			Description: "Proclaimed a passage of Scripture aloud in Voice Mode!",
			Icon:        "🎙️",
			EarnedAt:    now,
		})
	}

	if bp != nil && bp.TotalVerses > 0 && bp.CurrentVerse >= bp.TotalVerses && bp.ReviewCompleted {
		tryAward(Badge{
			ID:          "book_" + book,
			Type:        "book",
			Name:        book + " Mastered",
			Description: fmt.Sprintf("Completed all %d verses and review of %s!", bp.TotalVerses, book),
			Icon:        "🏆",
			EarnedAt:    now,
		})

		allProg, err := s.GetAllBookProgress(ctx, uid, trans)
		if err == nil {
			isBookDone := func(b string) bool {
				p, ok := allProg[b]
				return ok && p.TotalVerses > 0 && p.CurrentVerse >= p.TotalVerses && p.ReviewCompleted
			}

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

	activeStore := store
	activeHub := globalHub
	activeAngelMgr := globalAngelManager
	activeDemonMgr := getGlobalDemonManager()

	ctx := context.Background()
	_ = activeStore.EnsureUser(ctx, uid)

	// Fetch or init user account info
	account, _ := activeStore.GetAccount(ctx, uid)
	displayName := "Typist " + uid[:min(len(uid), 4)]
	avatarColor := "#6366f1"
	if account != nil {
		displayName = account.DisplayName
		avatarColor = account.AvatarColor
	}

	currentTransID := r.URL.Query().Get("trans")
	if _, ok := translations[currentTransID]; !ok {
		currentTransID = "kjv"
	}
	transData := translations[currentTransID]

	streak, _ := activeStore.GetStreak(ctx, uid)
	streakDays := 0
	if streak != nil {
		streakDays = streak.CurrentStreak
	}

	client := &Client{
		conn:           conn,
		UID:            uid,
		DisplayName:    displayName,
		AvatarColor:    avatarColor,
		StreakDays:     streakDays,
		Translation:    currentTransID,
		VerseStartTime: time.Now(),
	}
	if activeHub != nil {
		activeHub.Register(client)
		defer func() {
			activeHub.Unregister(client)
			if activeAngelMgr != nil {
				activeAngelMgr.DismissCompanion(uid)
			}
			if activeDemonMgr != nil {
				activeDemonMgr.DismissDemon(uid)
			}
		}()
	}

	// Send initial state to client
	_ = conn.WriteJSON(Message{
		Type:         "translations",
		Translation:  currentTransID,
		Translations: availableTransList,
	})

	if account != nil {
		_ = conn.WriteJSON(Message{
			Type:    "auth_success",
			Account: account,
		})
	}

	if streak != nil {
		_ = conn.WriteJSON(Message{
			Type:   "streakUpdate",
			Streak: streak,
		})
	}

	badges, _ := activeStore.GetBadges(ctx, uid)
	_ = conn.WriteJSON(Message{
		Type:   "badges",
		Badges: badges,
	})

	favs, _ := activeStore.GetFavorites(ctx, uid)
	_ = conn.WriteJSON(Message{
		Type:      "favorites",
		Favorites: favs,
	})

	// Ensure book progress rows
	totalsMap := make(map[string]int, len(transData.VersesByBook))
	for b, vList := range transData.VersesByBook {
		totalsMap[b] = len(vList)
	}
	_ = activeStore.EnsureBookProgressRows(ctx, uid, currentTransID, canonicalOrder, totalsMap)

	allProgress, err := activeStore.GetAllBookProgress(ctx, uid, currentTransID)
	if err != nil {
		allProgress = make(map[string]BookProgress)
	}

	_ = conn.WriteJSON(Message{
		Type:        "books",
		Books:       canonicalOrder,
		Progress:    allProgress,
		Translation: currentTransID,
	})

	sendSanctuaryState := func(progMap map[string]BookProgress) {
		customs, _ := activeStore.GetCustomizations(ctx, uid)
		var topWPM int
		if an, _ := activeStore.GetUserAnalytics(ctx, uid); an != nil {
			topWPM = an.TopWPM
		}
		totVerses := 0
		longStreak := 0
		if st, _ := activeStore.GetStreak(ctx, uid); st != nil {
			totVerses = st.TotalVersesTyped
			longStreak = st.LongestStreak
		}
		if progMap == nil {
			progMap, _ = activeStore.GetAllBookProgress(ctx, uid, currentTransID)
		}
		booksDone := 0
		for _, bp := range progMap {
			if bp.ReviewCompleted {
				booksDone++
			}
		}
		unlocks := GetAllCustomizationUnlocks(topWPM, totVerses, longStreak, booksDone)
		_ = conn.WriteJSON(Message{
			Type:      "sanctuary_state",
			Sanctuary: customs,
			Unlocks:   unlocks,
		})
	}

	// Send Sanctuary Customizations & Unlocks
	sendSanctuaryState(allProgress)

	// Send Angel Bot Status & Templates
	if activeAngelMgr != nil {
		_ = conn.WriteJSON(Message{
			Type:   "angels_list",
			Angels: activeAngelMgr.GetAngelTemplatesForUser(uid),
			Angel:  activeAngelMgr.GetGlobalAngelStatus(),
		})
	}

	// Send 24/7 Devil Status and Demon Templates
	if activeDemonMgr != nil {
		vCount, loc, dWpm := activeDemonMgr.GetDevil24_7Status()
		_ = conn.WriteJSON(Message{
			Type:                 "devil_24_7_status",
			Devil24_7VersesCount: vCount,
			Content:              loc,
			DemonSpeed:           dWpm,
			Demons:               activeDemonMgr.GetDemonTemplates(),
		})
	}

	// Send Celestial Boss Raid Status
	if boss, err := activeStore.GetBossRaid(ctx); err == nil && boss != nil {
		_ = conn.WriteJSON(Message{
			Type:     "boss_raid_update",
			BossRaid: boss,
		})
	}

	// Send active balance & runtime config
	balCopy := GetDemonSyncBalance()
	var rtOpts *AdminRuntimeOptions
	if adm := getGlobalAdmin(); adm != nil {
		opts := adm.GetOptions()
		rtOpts = &opts
	}
	_ = conn.WriteJSON(Message{
		Type:          "balance_config_update",
		BalanceConfig: &balCopy,
		RuntimeConfig: rtOpts,
	})

	// Session State
	var selectedBook string
	var bookProgress *BookProgress
	var bookVerses []Verse
	var sessionID int
	var activeVerseIdx int
	currentMode := "verse" // "verse", "intro", "review", "favorite", "challenge", "boss_raid"
	var activeFavorite *Favorite
	var activeChallenge *DailyChallenge
	var activeBossRaid *BossRaid
	var challengeStartTime time.Time

	runtimeStats := RuntimeStats{
		StartTime:      time.Now(),
		VerseStartTime: time.Now(),
	}

	currentStats := func() *Stats {
		var bp BookProgress
		if bookProgress != nil {
			bp = *bookProgress
		}
		return &Stats{
			BookProgress: bp,
			RuntimeStats: runtimeStats,
		}
	}

	ensureBookLoaded := func(bName string) bool {
		if bName == "" {
			bName = selectedBook
		}
		if bName == "" {
			return false
		}
		vList, ok := transData.VersesByBook[bName]
		if !ok {
			return false
		}
		if selectedBook != bName || bookProgress == nil || len(bookVerses) == 0 {
			selectedBook = bName
			bookVerses = vList
			bp, err := activeStore.GetBookProgress(ctx, uid, currentTransID, selectedBook, len(bookVerses))
			if err != nil || bp == nil {
				return false
			}
			bookProgress = bp
			activeVerseIdx = bookProgress.CurrentVerse
		}
		return true
	}

	resolveCurrentTargetPassage := func() (bName string, chNum, vNum int, targetText string) {
		if currentMode == "boss_raid" && activeBossRaid != nil {
			return activeBossRaid.BookName, activeBossRaid.Chapter, activeBossRaid.Verse, activeBossRaid.Text
		}
		if currentMode == "challenge" && activeChallenge != nil {
			return activeChallenge.BookName, activeChallenge.Chapter, activeChallenge.Verse, activeChallenge.Text
		}
		if currentMode == "favorite" && activeFavorite != nil {
			return activeFavorite.BookName, activeFavorite.Chapter, activeFavorite.Verse, activeFavorite.Text
		}
		if selectedBook != "" && activeVerseIdx < len(bookVerses) {
			v := bookVerses[activeVerseIdx]
			return v.BookName, v.Chapter, v.Verse, v.Text
		}
		return "", 0, 0, ""
	}

	sendCurrentVerse := func() {
		if selectedBook == "" || bookProgress == nil || len(bookVerses) == 0 {
			return
		}
		guide, _ := getBookGuide(currentTransID, selectedBook)
		stats := currentStats()

		if !bookProgress.IntroCompleted && (getGlobalAdmin() == nil || getGlobalAdmin().GetOptions().MandatoryBookIntro) {
			currentMode = "intro"
			runtimeStats.VerseStartTime = time.Now()
			client.VerseStartTime = runtimeStats.VerseStartTime
			if activeHub != nil {
				activeHub.UpdateClientLocation(client, currentTransID, selectedBook, 0, 0)
			}
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

		if activeVerseIdx > bookProgress.CurrentVerse {
			activeVerseIdx = bookProgress.CurrentVerse
		}
		if activeVerseIdx < 0 {
			activeVerseIdx = 0
		}

		if activeVerseIdx < len(bookVerses) {
			currentMode = "verse"
			v := bookVerses[activeVerseIdx]
			isFav, _ := activeStore.IsFavorite(ctx, uid, currentTransID, v.BookName, v.Chapter, v.Verse)
			uComps, gComps, _ := activeStore.GetVerseCompletions(ctx, uid, currentTransID, v.BookName, v.Chapter, v.Verse)
			if activeAngelMgr != nil {
				activeAngelMgr.UpdateCompanionLocation(uid, currentTransID, v.BookName, v.Chapter, v.Verse, v.Text)
			}
			if activeDemonMgr != nil {
				activeDemonMgr.UpdateDemonLocation(uid, currentTransID, v.BookName, v.Chapter, v.Verse, v.Text)
			}
			runtimeStats.VerseStartTime = time.Now()
			client.VerseStartTime = runtimeStats.VerseStartTime
			if activeHub != nil {
				activeHub.UpdateClientLocation(client, currentTransID, v.BookName, v.Chapter, v.Verse)
			}
			var wildD *WildDemon
			var encounterChance float64
			var isAmbush bool
			var wards int
			if activeDemonMgr != nil {
				wildD, encounterChance, isAmbush = activeDemonMgr.CheckOrSpawnWildDemonForUser(
					currentTransID,
					v,
					activeVerseIdx,
					len(bookVerses),
				)
			}
			if wildD != nil {
				if sanctuary, _ := activeStore.GetCustomizations(ctx, uid); sanctuary != nil {
					wards = sanctuary.GuardianWards
				}
			}
			_ = conn.WriteJSON(Message{
				Type:              "verse",
				Mode:              "verse",
				Content:           v.Text,
				Verse:             &v,
				Guide:             &guide,
				Number:            activeVerseIdx + 1,
				Total:             len(bookVerses),
				Stats:             stats,
				IsFavorite:        isFav,
				Translation:       currentTransID,
				Category:          guide.Category,
				ReviewLocked:      bookProgress.CurrentVerse < len(bookVerses),
				UserCompletions:   uComps,
				GlobalCompletions: gComps,
				WildDemon:         wildD,
				HolyWards:         wards,
				EncounterChance:   int(encounterChance * 100),
				IsAmbush:          isAmbush,
			})
			if wildD != nil {
				_ = conn.WriteJSON(Message{
					Type:            "wild_demon_present",
					WildDemon:       wildD,
					HolyWards:       wards,
					EncounterChance: int(encounterChance * 100),
					IsAmbush:        isAmbush,
				})
			}
			return
		}

		if !bookProgress.ReviewCompleted {
			currentMode = "review"
			runtimeStats.VerseStartTime = time.Now()
			client.VerseStartTime = runtimeStats.VerseStartTime
			if activeHub != nil {
				activeHub.UpdateClientLocation(client, currentTransID, selectedBook, 999, 999)
			}
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
				ReviewLocked: false,
			})
			return
		}

		currentMode = "verse"
		doneTitle := guide.Name
		if doneTitle == "" {
			doneTitle = selectedBook
		}
		_ = conn.WriteJSON(Message{
			Type:         "complete",
			Mode:         "verse",
			Content:      "All done with " + doneTitle + "! Praise the Sun! \\[T]/",
			Guide:        &guide,
			Number:       len(bookVerses),
			Total:        len(bookVerses),
			Stats:        stats,
			Translation:  currentTransID,
			Category:     guide.Category,
			ReviewLocked: false,
		})
	}

	syncUserStateAfterAuth := func() {
		st, _ := activeStore.GetStreak(ctx, uid)
		streak = st
		if st != nil {
			client.StreakDays = st.CurrentStreak
		}
		_ = conn.WriteJSON(Message{Type: "streakUpdate", Streak: st})
		bdg, _ := activeStore.GetBadges(ctx, uid)
		_ = conn.WriteJSON(Message{Type: "badges", Badges: bdg})
		favList, _ := activeStore.GetFavorites(ctx, uid)
		_ = conn.WriteJSON(Message{Type: "favorites", Favorites: favList})
		allProg, _ := activeStore.GetAllBookProgress(ctx, uid, currentTransID)
		_ = conn.WriteJSON(Message{Type: "books", Books: canonicalOrder, Progress: allProg, Translation: currentTransID})
		sendSanctuaryState(allProg)
		if selectedBook != "" {
			bookProgress, _ = activeStore.GetBookProgress(ctx, uid, currentTransID, selectedBook, len(bookVerses))
			sendCurrentVerse()
		}
	}

	for {
		var msg Message
		if err := conn.conn.ReadJSON(&msg); err != nil {
			return
		}

		switch msg.Type {

		// ---- Identity & Account Messages ----
		case "auth_register":
			acc, err := activeStore.RegisterAccount(ctx, msg.Username, msg.Email, hashPassword(msg.Password), msg.Content, msg.Color, msg.AnonUID)
			if err != nil {
				_ = conn.WriteJSON(Message{Type: "auth_error", Content: err.Error()})
				continue
			}
			uid = acc.UID
			client.UID = acc.UID
			client.DisplayName = acc.DisplayName
			client.AvatarColor = acc.AvatarColor
			if activeHub != nil {
				activeHub.UpdateClientIdentity(client, acc.DisplayName, acc.AvatarColor, client.StreakDays)
			}
			_ = conn.WriteJSON(Message{Type: "auth_success", Account: acc})
			syncUserStateAfterAuth()

		case "auth_login":
			acc, err := activeStore.LoginAccount(ctx, msg.Username, hashPassword(msg.Password), msg.AnonUID)
			if err != nil {
				_ = conn.WriteJSON(Message{Type: "auth_error", Content: err.Error()})
				continue
			}
			uid = acc.UID
			client.UID = acc.UID
			client.DisplayName = acc.DisplayName
			client.AvatarColor = acc.AvatarColor
			if activeHub != nil {
				activeHub.UpdateClientIdentity(client, acc.DisplayName, acc.AvatarColor, client.StreakDays)
			}
			_ = conn.WriteJSON(Message{Type: "auth_success", Account: acc})
			syncUserStateAfterAuth()

		case "update_profile":
			acc, err := activeStore.UpdateProfile(ctx, uid, msg.Content, msg.Color)
			if err == nil {
				client.DisplayName = acc.DisplayName
				client.AvatarColor = acc.AvatarColor
				if activeHub != nil {
					activeHub.UpdateClientIdentity(client, acc.DisplayName, acc.AvatarColor, client.StreakDays)
				}
				_ = conn.WriteJSON(Message{Type: "profile_updated", Account: acc})
			}

		case "claim_progress":
			if msg.AnonUID != "" && msg.AnonUID != uid {
				_ = activeStore.MergeUserProgress(ctx, uid, msg.AnonUID)
				syncUserStateAfterAuth()
			}

		// ---- Daily Challenge & Race Messages ----
		case "start_daily_challenge":
			dateStr := msg.Content
			if dateStr == "" {
				dateStr = time.Now().UTC().Format("2006-01-02")
			}
			ch, err := getDailyChallengeVerse(dateStr, currentTransID)
			if err != nil {
				_ = conn.WriteJSON(Message{Type: "wrong", Content: "Could not load challenge: " + err.Error()})
				continue
			}
			activeChallenge = ch
			currentMode = "challenge"
			challengeStartTime = time.Now()
			client.VerseStartTime = challengeStartTime
			if activeHub != nil {
				activeHub.UpdateClientLocation(client, currentTransID, ch.BookName, ch.Chapter, ch.Verse)
			}
			guide, _ := getBookGuide(currentTransID, ch.BookName)
			uComps, gComps, _ := activeStore.GetVerseCompletions(ctx, uid, currentTransID, ch.BookName, ch.Chapter, ch.Verse)
			if activeAngelMgr != nil {
				activeAngelMgr.UpdateCompanionLocation(uid, currentTransID, ch.BookName, ch.Chapter, ch.Verse, ch.Text)
			}
			if activeDemonMgr != nil {
				activeDemonMgr.UpdateDemonLocation(uid, currentTransID, ch.BookName, ch.Chapter, ch.Verse, ch.Text)
			}
			_ = conn.WriteJSON(Message{
				Type:              "daily_challenge",
				Mode:              "challenge",
				Challenge:         ch,
				Content:           ch.Text,
				Guide:             &guide,
				Translation:       currentTransID,
				Category:          guide.Category,
				UserCompletions:   uComps,
				GlobalCompletions: gComps,
			})

		// ---- Live Presence & Ghost Cursor Messages ----
		case "presence_cursor":
			if activeHub != nil {
				activeHub.UpdateCursor(client, msg.CharIndex, msg.WPM)
			}
			if activeDemonMgr != nil && client.Book != "" {
				if updated := activeDemonMgr.AdjustWildDemonPaceForPlayer(client.Translation, client.Book, client.Chapter, client.Verse, msg.CharIndex, msg.WPM); updated != nil {
					_ = conn.WriteJSON(Message{
						Type:      "wild_demon_update",
						WildDemon: updated,
					})
				}
			}

		case "toggle_spectator":
			client.IsSpectator = !client.IsSpectator
			_ = conn.WriteJSON(Message{Type: "spectator_state", Content: fmt.Sprintf("%v", client.IsSpectator)})

		// ---- Verse Comments & Reflection Messages ----
		case "get_comments":
			bName := msg.Book
			if bName == "" {
				bName = selectedBook
			}
			chNum := 1
			vNum := 1
			if msg.Verse != nil {
				chNum = msg.Verse.Chapter
				vNum = msg.Verse.Verse
			}
			cmts, _ := activeStore.GetVerseComments(ctx, currentTransID, bName, chNum, vNum, uid)
			_ = conn.WriteJSON(Message{
				Type:     "verse_comments",
				Comments: cmts,
				Book:     bName,
			})

		case "add_comment":
			bName := msg.Book
			if bName == "" {
				bName = selectedBook
			}
			chNum := 1
			vNum := 1
			if msg.Verse != nil {
				chNum = msg.Verse.Chapter
				vNum = msg.Verse.Verse
			}
			newCmt := VerseComment{
				UID:         uid,
				DisplayName: client.DisplayName,
				AvatarColor: client.AvatarColor,
				StreakDays:  client.StreakDays,
				Translation: currentTransID,
				BookName:    bName,
				Chapter:     chNum,
				Verse:       vNum,
				PromptType:  msg.PromptType,
				Content:     cleanVerseText(msg.Content),
				IsPrivate:   msg.IsPrivate,
				ParentID:    msg.ParentID,
			}
			saved, err := activeStore.AddVerseComment(ctx, newCmt)
			if err == nil {
				_ = conn.WriteJSON(Message{Type: "comment_added_ack", Comment: saved})
				if !saved.IsPrivate && activeHub != nil {
					activeHub.BroadcastCommentAdded(*saved)
				}
			}

		case "react_comment":
			if msg.CommentID > 0 && msg.Reaction != "" {
				_ = activeStore.ReactVerseComment(ctx, msg.CommentID, msg.Reaction)
				_ = conn.WriteJSON(Message{Type: "reaction_ack", CommentID: msg.CommentID, Reaction: msg.Reaction})
			}

		case "flag_comment":
			if msg.CommentID > 0 {
				_ = activeStore.FlagVerseComment(ctx, msg.CommentID)
				_ = conn.WriteJSON(Message{Type: "flag_ack", CommentID: msg.CommentID})
			}

		// ---- Live Fellowship Chat Messages ----
		case "get_chat":
			chName := msg.Channel
			if chName == "" {
				chName = "global"
			}
			history, _ := activeStore.GetRecentChat(ctx, chName, 50)
			_ = conn.WriteJSON(Message{
				Type:        "chat_history",
				Channel:     chName,
				ChatHistory: history,
			})

		case "chat_message":
			text := strings.TrimSpace(msg.Content)
			if text != "" && len(text) <= 500 {
				chName := msg.Channel
				if chName == "" {
					chName = "global"
				}
				chatMsg := ChatMessage{
					UID:         uid,
					DisplayName: client.DisplayName,
					AvatarColor: client.AvatarColor,
					Channel:     chName,
					Content:     text,
					CreatedAt:   time.Now().UTC(),
				}
				_ = activeStore.AddChatMessage(ctx, chatMsg)
				if activeHub != nil {
					activeHub.BroadcastChatMessage(chatMsg)
				}
			}

		// ---- Analytics & Leaderboard Messages ----
		case "get_leaderboards":
			todayStr := time.Now().UTC().Format("2006-01-02")
			lbs, _ := activeStore.GetLeaderboards(ctx, todayStr)
			_ = conn.WriteJSON(Message{
				Type:         "leaderboards",
				Leaderboards: lbs,
			})

		case "get_analytics":
			an, _ := activeStore.GetUserAnalytics(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:      "user_analytics",
				Analytics: an,
			})

		case "get_crowd_metrics":
			cm, _ := activeStore.GetCrowdMetrics(ctx)
			_ = conn.WriteJSON(Message{
				Type:    "crowd_metrics",
				Metrics: cm,
			})

		// ---- Sanctuary & Customization Messages ----
		case "get_sanctuary":
			sendSanctuaryState(nil)

		case "save_sanctuary":
			if msg.Sanctuary != nil {
				_ = activeStore.SaveCustomizations(ctx, uid, *msg.Sanctuary)
				_ = conn.WriteJSON(Message{
					Type:      "sanctuary_saved",
					Sanctuary: msg.Sanctuary,
				})
			}

		// ---- Heavenly Angel Bot Messages ----
		case "get_angels":
			if activeAngelMgr != nil {
				_ = conn.WriteJSON(Message{
					Type:   "angels_list",
					Angels: activeAngelMgr.GetAngelTemplatesForUser(uid),
					Angel:  activeAngelMgr.GetGlobalAngelStatus(),
				})
			}

		case "summon_angel":
			rank := msg.AngelRank
			if rank == "" {
				rank = msg.Content
			}
			if rank == "" {
				rank = "guardian"
			}
			if activeAngelMgr != nil {
				bName, chNum, vNum, targetText := resolveCurrentTargetPassage()
				if bName != "" && targetText != "" {
					ang := activeAngelMgr.SummonCompanionWithLevel(uid, rank, msg.AngelLevel, currentTransID, bName, chNum, vNum, targetText)
					_ = conn.WriteJSON(Message{
						Type:  "angel_summoned",
						Angel: ang,
					})
				}
			}

		case "dismiss_angel":
			if activeAngelMgr != nil {
				activeAngelMgr.DismissCompanion(uid)
				_ = conn.WriteJSON(Message{
					Type: "angel_dismissed",
				})
			}

		// ---- Demonic Adversary Messages ----
		case "get_demons":
			if activeDemonMgr != nil {
				vCount, loc, dWpm := activeDemonMgr.GetDevil24_7Status()
				_ = conn.WriteJSON(Message{
					Type:                 "devil_24_7_status",
					Devil24_7VersesCount: vCount,
					Content:              loc,
					DemonSpeed:           dWpm,
					Demons:               activeDemonMgr.GetDemonTemplates(),
				})
			}

		case "summon_demon":
			rank := msg.DemonRank
			if rank == "" {
				rank = "imp"
			}
			lvl := msg.DemonLevel
			if lvl < 1 {
				lvl = 1
			}
			if activeDemonMgr != nil {
				bName, chNum, vNum, targetText := resolveCurrentTargetPassage()
				if bName != "" && targetText != "" {
					dmn := activeDemonMgr.SummonDemon(uid, rank, lvl, currentTransID, bName, chNum, vNum, targetText)
					_ = conn.WriteJSON(Message{
						Type:  "demon_summoned",
						Demon: dmn,
					})
				}
			}

		case "dismiss_demon":
			if activeDemonMgr != nil {
				activeDemonMgr.DismissDemon(uid)
				_ = conn.WriteJSON(Message{
					Type: "demon_dismissed",
				})
			}

		case "get_wild_demons":
			sanctuary, _ := activeStore.GetCustomizations(ctx, uid)
			defCount := 0
			unlocked := false
			if sanctuary != nil {
				defCount = sanctuary.DemonsDefeated
				unlocked = sanctuary.DemonsDefeated >= 6
			}
			var list []WildDemon
			activeCount := 0
			maxDemons := 666
			if activeDemonMgr != nil {
				allDemons := activeDemonMgr.GetWildDemons()
				activeCount = len(allDemons)
				maxDemons = activeDemonMgr.GetMaxWildDemons()
				if unlocked {
					list = allDemons
				}
			}
			sanctPct := 100
			if activeCount > 0 && maxDemons > 0 {
				sanctPct = int((1.0 - float64(activeCount)/float64(maxDemons)) * 100)
				if sanctPct < 0 {
					sanctPct = 0
				}
			}
			_ = conn.WriteJSON(Message{
				Type:            "wild_demons_list",
				WildDemons:      list,
				TrackerUnlocked: unlocked,
				DemonsDefeated:  defCount,
				ActiveDemons:    activeCount,
				MaxDemons:       maxDemons,
				Sanctification:  sanctPct,
			})

		case "confront_wild_demon":
			if activeDemonMgr == nil {
				continue
			}
			demon := activeDemonMgr.GetWildDemon(msg.DemonID)
			if demon == nil {
				continue
			}
			if td, ok := translations[demon.Translation]; ok {
				currentTransID = demon.Translation
				transData = td
				if ensureBookLoaded(demon.BookName) {
					foundIdx := -1
					for i, v := range bookVerses {
						if v.Chapter == demon.Chapter && v.Verse == demon.Verse {
							foundIdx = i
							break
						}
					}
					if foundIdx >= 0 {
						activeVerseIdx = foundIdx
						currentMode = "verse"
						if !bookProgress.IntroCompleted {
							bookProgress.IntroCompleted = true
							_ = activeStore.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
						}
						if bookProgress.CurrentVerse < foundIdx {
							bookProgress.CurrentVerse = foundIdx
							_ = activeStore.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
						}
						sendCurrentVerse()
					}
				}
			}

		case "wild_demon_strike":
			if activeDemonMgr == nil {
				continue
			}
			demonID := msg.DemonID
			damage := msg.Damage
			if damage <= 0 {
				damage = 1
			}
			if msg.SyncStacks > 0 {
				damage = CalculateSyncStackDamage(damage, msg.SyncStacks)
			}
			updated, defeated := activeDemonMgr.ApplyWildDemonDamage(demonID, damage)
			if updated != nil {
				if defeated {
					newSettings, leveledUp, _ := activeStore.RecordWildDemonDefeat(ctx, uid, updated.Rank, updated.Level)
					if activeHub != nil {
						activeHub.BroadcastWildDemonsList(activeDemonMgr.GetWildDemons())
					}
					content := fmt.Sprintf("Exorcised %s (Lv. %d)! Total Defeated: %d", updated.Name, updated.Level, newSettings.DemonsDefeated)
					if leveledUp {
						content = fmt.Sprintf("Exorcised %s! Your Guardian Angel reached Level %d!", updated.Name, newSettings.GuardianLevel)
					}
					_ = conn.WriteJSON(Message{
						Type:            "wild_demon_defeated",
						WildDemon:       updated,
						Sanctuary:       newSettings,
						TrackerUnlocked: newSettings.TrackerUnlocked,
						DemonsDefeated:  newSettings.DemonsDefeated,
						DemonDefeated:   true,
						Damage:          damage,
						SyncStacks:      msg.SyncStacks,
						Content:         content,
					})
				} else {
					_ = conn.WriteJSON(Message{
						Type:       "wild_demon_update",
						WildDemon:  updated,
						Damage:     damage,
						SyncStacks: msg.SyncStacks,
					})
				}
			}

		case "demon_mistake_backlash":
			if activeDemonMgr == nil {
				continue
			}
			demonID := msg.DemonID
			enraged := activeDemonMgr.EnrageWildDemonOnMistake(demonID)
			if enraged != nil {
				_ = conn.WriteJSON(Message{
					Type:      "wild_demon_enraged",
					WildDemon: enraged,
					Content:   fmt.Sprintf("%s was empowered by your mistake! (+10%% HP, +2 WPM)", enraged.Name),
				})
			}

		// ---- Celestial & Demonic Boss Raid Messages ----
		case "get_boss_raid":
			lvl := msg.BossLevel
			if lvl < 1 {
				lvl = 2
			}
			isDemon := msg.IsDemonBoss || (msg.BossRaid != nil && msg.BossRaid.IsDemonBoss) || msg.Content == "demon"
			b, err := activeStore.GetBossRaidForLevel(ctx, lvl, isDemon)
			if err != nil || b == nil {
				b, _ = activeStore.GetBossRaid(ctx)
			}
			if b != nil {
				_ = conn.WriteJSON(Message{
					Type:     "boss_raid_update",
					BossRaid: b,
				})
			}

		case "start_boss_raid":
			lvl := msg.BossLevel
			if lvl < 1 {
				lvl = 2
			}
			isDemon := msg.IsDemonBoss || (msg.BossRaid != nil && msg.BossRaid.IsDemonBoss) || msg.Content == "demon"
			b, err := activeStore.GetBossRaidForLevel(ctx, lvl, isDemon)
			if err != nil || b == nil {
				b, _ = activeStore.GetBossRaid(ctx)
			}
			if b != nil {
				currentMode = "boss_raid"
				activeBossRaid = b
				challengeStartTime = time.Now()
				runtimeStats.VerseStartTime = challengeStartTime
				client.VerseStartTime = challengeStartTime
				guide, _ := getBookGuide(currentTransID, b.BookName)
				_ = conn.WriteJSON(Message{
					Type:     "boss_raid_verse",
					Content:  b.Text,
					BossRaid: b,
					Guide:    &guide,
				})
				if b.IsDemonBoss {
					if activeDemonMgr != nil {
						dmn := activeDemonMgr.SummonDemon(uid, "apollyon", b.Level, b.Translation, b.BookName, b.Chapter, b.Verse, b.Text)
						_ = conn.WriteJSON(Message{
							Type:  "demon_summoned",
							Demon: dmn,
						})
					}
				} else {
					if activeAngelMgr != nil {
						ang := activeAngelMgr.SummonCompanionWithLevel(uid, "michael", b.Level, b.Translation, b.BookName, b.Chapter, b.Verse, b.Text)
						_ = conn.WriteJSON(Message{
							Type:  "angel_summoned",
							Angel: ang,
						})
					}
				}
			}

		// ---- Offline Progress Sync ----
		case "sync_offline_progress":
			for _, item := range msg.SyncItems {
				if item.BookName != "" && item.Verse > 0 {
					totalVerses := 100
					if td, ok := translations[item.Translation]; ok {
						if vl := td.VersesByBook[item.BookName]; len(vl) > 0 {
							totalVerses = len(vl)
						}
					}
					bp, err := activeStore.GetBookProgress(ctx, uid, item.Translation, item.BookName, totalVerses)
					if err == nil {
						if item.Verse > bp.CurrentVerse {
							bp.CurrentVerse = item.Verse
						}
						bp.CorrectEntries++
						_ = activeStore.UpdateBookProgress(ctx, uid, item.Translation, item.BookName, bp)
						_ = activeStore.RecordTypingSession(ctx, uid, item.Translation, item.BookName, item.Verse*30, item.Verse*30, item.WPM, item.Duration)
						_, _ = activeStore.RecordVerseCompleted(ctx, uid, item.Verse*30, item.Duration)
					}
				}
			}
			st, _ := activeStore.GetStreak(ctx, uid)
			allProg, _ := activeStore.GetAllBookProgress(ctx, uid, currentTransID)
			_ = conn.WriteJSON(Message{
				Type:     "sync_complete",
				Streak:   st,
				Progress: allProg,
			})

		// ---- Core Typing Navigation & Selection ----
		case "select_translation":
			newTrans := strings.ToLower(strings.TrimSpace(msg.Content))
			if td, ok := translations[newTrans]; ok {
				currentTransID = newTrans
				transData = td
				client.Translation = newTrans

				totalsMap := make(map[string]int, len(transData.VersesByBook))
				for b, vList := range transData.VersesByBook {
					totalsMap[b] = len(vList)
				}
				_ = activeStore.EnsureBookProgressRows(ctx, uid, currentTransID, canonicalOrder, totalsMap)

				allProgress, _ = activeStore.GetAllBookProgress(ctx, uid, currentTransID)
				_ = conn.WriteJSON(Message{
					Type:        "books",
					Books:       canonicalOrder,
					Progress:    allProgress,
					Translation: currentTransID,
				})

				if selectedBook != "" {
					bookVerses = transData.VersesByBook[selectedBook]
					bookProgress, _ = activeStore.GetBookProgress(ctx, uid, currentTransID, selectedBook, len(bookVerses))
					activeVerseIdx = bookProgress.CurrentVerse
					sendCurrentVerse()
				}
			}

		case "select_book":
			bookName := msg.Content
			if !ensureBookLoaded(bookName) {
				continue
			}

			sessionID, _ = activeStore.CreateTypingSession(ctx, uid, currentTransID, selectedBook)
			runtimeStats = RuntimeStats{
				StartTime:      time.Now(),
				VerseStartTime: time.Now(),
			}
			client.VerseStartTime = runtimeStats.VerseStartTime
			activeVerseIdx = bookProgress.CurrentVerse
			sendCurrentVerse()

		case "jump_verse":
			if selectedBook == "" || bookProgress == nil || len(bookVerses) == 0 {
				continue
			}
			if !bookProgress.IntroCompleted && (getGlobalAdmin() == nil || getGlobalAdmin().GetOptions().MandatoryBookIntro) {
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
			if !ensureBookLoaded(bName) {
				continue
			}
			guide, ok := getBookGuide(currentTransID, selectedBook)
			if !ok {
				continue
			}
			currentMode = "intro"
			runtimeStats.VerseStartTime = time.Now()
			client.VerseStartTime = runtimeStats.VerseStartTime
			if activeHub != nil {
				activeHub.UpdateClientLocation(client, currentTransID, selectedBook, 0, 0)
			}
			_ = conn.WriteJSON(Message{
				Type:         "intro",
				Mode:         "intro",
				Mandatory:    !bookProgress.IntroCompleted,
				Content:      guide.Description,
				Guide:        &guide,
				Stats:        currentStats(),
				Translation:  currentTransID,
				Category:     guide.Category,
				ReviewLocked: bookProgress.CurrentVerse < len(bookVerses),
			})

		case "start_review":
			bName := msg.Content
			if !ensureBookLoaded(bName) {
				continue
			}
			guide, ok := getBookGuide(currentTransID, selectedBook)
			if !ok {
				continue
			}

			if bookProgress.CurrentVerse < len(bookVerses) {
				_ = conn.WriteJSON(Message{
					Type:         "review_locked",
					Content:      fmt.Sprintf("Review for %s is locked! Complete all %d verses to unlock the review.", guide.Name, len(bookVerses)),
					ReviewLocked: true,
				})
				continue
			}

			currentMode = "review"
			runtimeStats.VerseStartTime = time.Now()
			client.VerseStartTime = runtimeStats.VerseStartTime
			if activeHub != nil {
				activeHub.UpdateClientLocation(client, currentTransID, selectedBook, 999, 999)
			}
			_ = conn.WriteJSON(Message{
				Type:         "review",
				Mode:         "review",
				Mandatory:    !bookProgress.ReviewCompleted,
				Content:      guide.Review,
				Guide:        &guide,
				Stats:        currentStats(),
				Translation:  currentTransID,
				Category:     guide.Category,
				ReviewLocked: false,
			})

		case "start_favorite":
			favID := msg.Number
			allFavs, _ := activeStore.GetFavorites(ctx, uid)
			for _, f := range allFavs {
				if f.ID == favID || (msg.Verse != nil && f.BookName == msg.Book && f.Chapter == msg.Verse.Chapter && f.Verse == msg.Verse.Verse) {
					fCopy := f
					activeFavorite = &fCopy
					currentMode = "favorite"
					guide, _ := getBookGuide(f.Translation, f.BookName)
					runtimeStats.VerseStartTime = time.Now()
					client.VerseStartTime = runtimeStats.VerseStartTime
					if activeHub != nil {
						activeHub.UpdateClientLocation(client, f.Translation, f.BookName, f.Chapter, f.Verse)
					}
					uComps, gComps, _ := activeStore.GetVerseCompletions(ctx, uid, f.Translation, f.BookName, f.Chapter, f.Verse)
					if activeAngelMgr != nil {
						activeAngelMgr.UpdateCompanionLocation(uid, f.Translation, f.BookName, f.Chapter, f.Verse, f.Text)
					}
					if activeDemonMgr != nil {
						activeDemonMgr.UpdateDemonLocation(uid, f.Translation, f.BookName, f.Chapter, f.Verse, f.Text)
					}
					revLocked := true
					if bookProgress != nil {
						revLocked = bookProgress.CurrentVerse < len(bookVerses)
					}
					_ = conn.WriteJSON(Message{
						Type:              "favorite_verse",
						Mode:              "favorite",
						Content:           f.Text,
						Favorite:          &f,
						Guide:             &guide,
						Stats:             currentStats(),
						Translation:       f.Translation,
						Category:          guide.Category,
						ReviewLocked:      revLocked,
						UserCompletions:   uComps,
						GlobalCompletions: gComps,
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

			isFav, _ := activeStore.IsFavorite(ctx, uid, currentTransID, targetVerse.BookName, targetVerse.Chapter, targetVerse.Verse)
			if isFav {
				_ = activeStore.RemoveFavorite(ctx, uid, currentTransID, targetVerse.BookName, targetVerse.Chapter, targetVerse.Verse)
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
				_ = activeStore.AddFavorite(ctx, fav)

				favList, _ := activeStore.GetFavorites(ctx, uid)
				if len(favList) >= 5 {
					_ = checkAndAwardBadges(ctx, activeStore, uid, currentTransID, targetVerse.BookName, bookProgress, streak, 0, false, "fav_collect")
				}
			}

			favList, _ := activeStore.GetFavorites(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:       "favoriteToggled",
				IsFavorite: !isFav,
				Favorites:  favList,
				Verse:      &targetVerse,
			})

		case "get_favorites":
			favList, _ := activeStore.GetFavorites(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:      "favorites",
				Favorites: favList,
			})

		case "get_badges":
			badgeList, _ := activeStore.GetBadges(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:   "badges",
				Badges: badgeList,
			})

		case "get_streak":
			st, _ := activeStore.GetStreak(ctx, uid)
			_ = conn.WriteJSON(Message{
				Type:   "streakUpdate",
				Streak: st,
			})

		// ---- Core Content Submission (with Server-Side Integrity Verification) ----
		case "content":
			userInput := cleanVerseText(strings.TrimSpace(msg.Content))
			if userInput == "" {
				continue
			}

			var targetText string
			switch currentMode {
			case "boss_raid":
				if activeBossRaid != nil {
					targetText = cleanVerseText(activeBossRaid.Text)
				}
			case "challenge":
				if activeChallenge != nil {
					targetText = cleanVerseText(activeChallenge.Text)
				}
			case "intro":
				g, _ := getBookGuide(currentTransID, selectedBook)
				targetText = cleanVerseText(g.Description)
			case "review":
				g, _ := getBookGuide(currentTransID, selectedBook)
				targetText = cleanVerseText(g.Review)
			case "favorite":
				if activeFavorite != nil {
					targetText = cleanVerseText(activeFavorite.Text)
				}
			default:
				if selectedBook != "" && bookProgress != nil && activeVerseIdx < len(bookVerses) {
					targetText = cleanVerseText(bookVerses[activeVerseIdx].Text)
				}
			}

			if targetText == "" {
				continue
			}

			// Server-side timing verification (Section 9)
			startTime := runtimeStats.VerseStartTime
			if currentMode == "challenge" || currentMode == "boss_raid" {
				startTime = challengeStartTime
			}
			verifiedDuration, instantWPM, _ := verifySubmissionTiming(startTime, msg.Duration, targetText, msg.WPM)

			if matchesPassageText(userInput, targetText) || (msg.VoiceMode && matchesSpokenPassage(userInput, targetText)) {
				inputLen := runeLen(targetText)
				runtimeStats.CorrectChars += inputLen
				runtimeStats.CharsTyped += inputLen
				runtimeStats.WPM = instantWPM

				// Capture previous progression values before recording completion
				prevVerses, prevLongStreak := 0, 0
				if streak != nil {
					prevVerses = streak.TotalVersesTyped
					prevLongStreak = streak.LongestStreak
				}
				prevTopWPM := 0
				if prevAn, _ := activeStore.GetUserAnalytics(ctx, uid); prevAn != nil {
					prevTopWPM = prevAn.TopWPM
				}

				// Record completed verse in streak
				updatedStreak, _ := activeStore.RecordVerseCompleted(ctx, uid, inputLen, verifiedDuration)
				streak = updatedStreak
				if streak != nil {
					client.StreakDays = streak.CurrentStreak
				}

				// Record detailed session for analytics
				bName := selectedBook
				if currentMode == "challenge" && activeChallenge != nil {
					bName = activeChallenge.BookName
				} else if currentMode == "favorite" && activeFavorite != nil {
					bName = activeFavorite.BookName
				} else if currentMode == "boss_raid" && activeBossRaid != nil {
					bName = activeBossRaid.BookName
				}
				_ = activeStore.RecordTypingSession(ctx, uid, currentTransID, bName, inputLen, inputLen, instantWPM, verifiedDuration)

				var rewards []Badge
				var uComps, gComps int
				if currentMode == "challenge" && activeChallenge != nil {
					uComps, gComps, _ = activeStore.RecordVerseCompletion(ctx, uid, currentTransID, activeChallenge.BookName, activeChallenge.Chapter, activeChallenge.Verse)

					// Save Daily Race result
					_ = activeStore.SaveRaceResult(ctx, RaceResult{
						Date:        activeChallenge.Date,
						UID:         uid,
						DisplayName: client.DisplayName,
						AvatarColor: client.AvatarColor,
						WPM:         instantWPM,
						Duration:    verifiedDuration,
						Accuracy:    100,
					})
					rewards = checkAndAwardBadges(ctx, activeStore, uid, currentTransID, activeChallenge.BookName, bookProgress, streak, instantWPM, true, "challenge")
					if msg.VoiceMode {
						rewards = append(rewards, checkAndAwardBadges(ctx, activeStore, uid, currentTransID, activeChallenge.BookName, bookProgress, streak, instantWPM, true, "voice")...)
					}

					todayStr := activeChallenge.Date
					raceResults, _ := activeStore.GetDailyRaceResults(ctx, todayStr)
					rank := 1
					for _, r := range raceResults {
						if r.UID == uid && r.WPM == instantWPM {
							rank = r.Rank
							break
						}
					}

					_ = conn.WriteJSON(Message{
						Type:     "race_complete",
						Content:  fmt.Sprintf("Praise God! Challenge complete: %d WPM (%.1fs) · Rank #%d", instantWPM, verifiedDuration, rank),
						WPM:      instantWPM,
						Duration: verifiedDuration,
						RaceRank: rank,
					})
					_ = conn.WriteJSON(Message{
						Type:              "correct",
						Content:           "correct",
						WPM:               instantWPM,
						Duration:          verifiedDuration,
						UserCompletions:   uComps,
						GlobalCompletions: gComps,
					})
					_ = conn.WriteJSON(Message{Type: "streakUpdate", Streak: streak})

					for _, badge := range rewards {
						_ = conn.WriteJSON(Message{Type: "badgeUnlocked", Reward: &badge})
					}
					currentMode = "verse"
					sendCurrentVerse()
					continue
				}

				if currentMode == "boss_raid" && activeBossRaid != nil {
					// Calculate Holy Strike Damage
					damage := instantWPM
					critMultiplier := 1.0
					if instantWPM >= activeBossRaid.BossWPM {
						critMultiplier = 3.0 // Outpaced Big Mike / Boss!
					} else if instantWPM >= 120 {
						critMultiplier = 2.0 // Divine Crit
					} else if instantWPM >= 80 {
						critMultiplier = 1.5 // Critical Strike
					}
					damage = int(float64(damage) * critMultiplier)
					if activeBossRaid.IsDemonBoss && msg.SyncStacks > 0 {
						damage = CalculateSyncStackDamage(damage, msg.SyncStacks)
					}

					updatedBoss, _ := activeStore.RecordBossStrike(ctx, uid, client.DisplayName, client.AvatarColor, instantWPM, damage)
					if updatedBoss != nil {
						activeBossRaid = updatedBoss
						strikeHit := &BossConqueror{
							UID:         uid,
							DisplayName: client.DisplayName,
							AvatarColor: client.AvatarColor,
							WPM:         instantWPM,
							Damage:      damage,
							Timestamp:   time.Now().UTC(),
						}
						// Broadcast damage live to all fellowship connections!
						if activeHub != nil {
							activeHub.BroadcastBossRaidUpdate(*updatedBoss, strikeHit)
						}
					}

					var bossNote string
					targetName := activeBossRaid.BossName
					if instantWPM >= activeBossRaid.BossWPM {
						if activeBossRaid.IsDemonBoss {
							bossNote = fmt.Sprintf("🔥 TRIUMPH OVER THE DESTROYER! Outpaced %s at %d WPM! Struck for %d holy damage!", targetName, instantWPM, damage)
						} else {
							bossNote = fmt.Sprintf("⚡ TRIUMPH OVER BIG MIKE! Outpaced %s at %d WPM! Struck for %d holy damage!", targetName, instantWPM, damage)
						}
					} else {
						bossNote = fmt.Sprintf("⚔️ Strike landed! %d WPM dealt %d holy damage to %s!", instantWPM, damage, targetName)
					}

					if updatedBoss != nil && updatedBoss.Conquered {
						if activeBossRaid.IsDemonBoss {
							bossNote += fmt.Sprintf(" 🏆 %s HAS BEEN BANISHED INTO THE PIT BY THE FELLOWSHIP!", strings.ToUpper(targetName))
							slayerBadge := Badge{
								ID:          "demon_boss_slayer",
								Type:        "boss",
								Name:        "Abaddon Banisher",
								Description: fmt.Sprintf("Stood with the fellowship to triumph over %s (Lv. %d)!", targetName, activeBossRaid.Level),
								Icon:        "🔥⚔️",
							}
							if awarded, _ := activeStore.AwardBadge(ctx, uid, slayerBadge); awarded {
								_ = conn.WriteJSON(Message{Type: "badgeUnlocked", Reward: &slayerBadge})
							}
						} else {
							bossNote += fmt.Sprintf(" 🏆 %s'S TRIAL HAS BEEN CONQUERED BY THE FELLOWSHIP!", strings.ToUpper(targetName))
							slayerBadge := Badge{
								ID:          "archangel_slayer",
								Type:        "boss",
								Name:        "Archangel Victor",
								Description: fmt.Sprintf("Stood with the fellowship to triumph over %s's trial (Lv. %d)!", targetName, activeBossRaid.Level),
								Icon:        "⚔️",
							}
							if awarded, _ := activeStore.AwardBadge(ctx, uid, slayerBadge); awarded {
								_ = conn.WriteJSON(Message{Type: "badgeUnlocked", Reward: &slayerBadge})
							}
						}
					}

					_ = conn.WriteJSON(Message{
						Type:     "boss_strike_result",
						Content:  bossNote,
						WPM:      instantWPM,
						Duration: verifiedDuration,
						BossRaid: updatedBoss,
					})
					_ = conn.WriteJSON(Message{
						Type:     "correct",
						Content:  "correct",
						WPM:      instantWPM,
						Duration: verifiedDuration,
					})
					_ = conn.WriteJSON(Message{Type: "streakUpdate", Streak: streak})

					if activeAngelMgr != nil {
						activeAngelMgr.UpdateCompanionWPM(uid)
					}

					currentMode = "verse"
					sendCurrentVerse()
					continue
				}

				// Check if user finished a passage before a wild demon on this verse
				var completedTrans, completedBook string
				var completedChapter, completedVerse int
				if currentMode == "verse" && activeVerseIdx < len(bookVerses) {
					vCompleted := bookVerses[activeVerseIdx]
					completedTrans = currentTransID
					completedBook = vCompleted.BookName
					completedChapter = vCompleted.Chapter
					completedVerse = vCompleted.Verse
				} else if currentMode == "favorite" && activeFavorite != nil {
					completedTrans = activeFavorite.Translation
					completedBook = activeFavorite.BookName
					completedChapter = activeFavorite.Chapter
					completedVerse = activeFavorite.Verse
				}
				var activeWildDemonBeforeAdvance *WildDemon
				if activeDemonMgr != nil && completedBook != "" {
					activeWildDemonBeforeAdvance = activeDemonMgr.GetWildDemonAt(completedTrans, completedBook, completedChapter, completedVerse)
					if activeWildDemonBeforeAdvance == nil && msg.DemonID != "" {
						if d := activeDemonMgr.GetWildDemon(msg.DemonID); d != nil && d.CurrentHP > 0 && d.BookName == completedBook && d.Chapter == completedChapter && d.Verse == completedVerse {
							activeWildDemonBeforeAdvance = d
						}
					}
				}

				if currentMode == "intro" && bookProgress != nil {
					bookProgress.IntroCompleted = true
					_ = activeStore.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
					rewards = checkAndAwardBadges(ctx, activeStore, uid, currentTransID, selectedBook, bookProgress, streak, instantWPM, true, "intro")
					activeVerseIdx = bookProgress.CurrentVerse
				} else if currentMode == "review" && bookProgress != nil {
					bookProgress.ReviewCompleted = true
					now := time.Now().UTC()
					bookProgress.CompletedAt = &now
					_ = activeStore.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
					rewards = checkAndAwardBadges(ctx, activeStore, uid, currentTransID, selectedBook, bookProgress, streak, instantWPM, true, "review")
				} else if currentMode == "favorite" && activeFavorite != nil {
					uComps, gComps, _ = activeStore.RecordVerseCompletion(ctx, uid, activeFavorite.Translation, activeFavorite.BookName, activeFavorite.Chapter, activeFavorite.Verse)
					_ = activeStore.RecordFavoriteTyped(ctx, uid, activeFavorite.Translation, activeFavorite.BookName, activeFavorite.Chapter, activeFavorite.Verse)
					rewards = checkAndAwardBadges(ctx, activeStore, uid, activeFavorite.Translation, activeFavorite.BookName, bookProgress, streak, instantWPM, true, "favorite")
				} else if bookProgress != nil {
					if activeVerseIdx < len(bookVerses) {
						vCompleted := bookVerses[activeVerseIdx]
						uComps, gComps, _ = activeStore.RecordVerseCompletion(ctx, uid, currentTransID, vCompleted.BookName, vCompleted.Chapter, vCompleted.Verse)
					}

					if activeVerseIdx == bookProgress.CurrentVerse {
						bookProgress.CurrentVerse++
						activeVerseIdx++
					} else {
						activeVerseIdx++
					}
					bookProgress.CorrectEntries++
					_ = activeStore.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)

					if sessionID > 0 {
						_ = activeStore.UpdateTypingSession(ctx, sessionID, &runtimeStats)
					}

					rewards = checkAndAwardBadges(ctx, activeStore, uid, currentTransID, selectedBook, bookProgress, streak, instantWPM, true, "verse")
				}
				if msg.VoiceMode {
					rewards = append(rewards, checkAndAwardBadges(ctx, activeStore, uid, currentTransID, selectedBook, bookProgress, streak, instantWPM, true, "voice")...)
				}

				// Apply Passage Finish Burst Damage if the user finished the passage before the wild demon!
				if activeWildDemonBeforeAdvance != nil && activeWildDemonBeforeAdvance.CurrentHP > 0 && activeDemonMgr != nil {
					customs, _ := activeStore.GetCustomizations(ctx, uid)
					gPower := 10
					customBaseBurst := GetDemonSyncBalance().PassageBurstBaseDamage
					if customs != nil {
						if customs.GuardianPower > 0 {
							gPower = customs.GuardianPower
						}
						if customs.PassageBurstDamage > 0 {
							customBaseBurst = customs.PassageBurstDamage
						}
					}
					if msg.BurstDamage > 0 {
						customBaseBurst = msg.BurstDamage
						if customs != nil && customs.PassageBurstDamage != msg.BurstDamage {
							customs.PassageBurstDamage = msg.BurstDamage
							_ = activeStore.SaveCustomizations(ctx, uid, *customs)
						}
					}
					baseStrikeDamage := (gPower + 5) / 10
					if baseStrikeDamage < 1 {
						baseStrikeDamage = 1
					}
					charsAhead := inputLen - activeWildDemonBeforeAdvance.CharIndex
					if charsAhead < 0 {
						charsAhead = 0
					}
					burstDamage := CalculatePassageFinishBurstDamage(baseStrikeDamage, msg.SyncStacks, charsAhead, customBaseBurst)
					updatedDemon, defeated := activeDemonMgr.ApplyWildDemonDamage(activeWildDemonBeforeAdvance.ID, burstDamage)
					if updatedDemon != nil {
						if defeated {
							newSettings, leveledUp, _ := activeStore.RecordWildDemonDefeat(ctx, uid, updatedDemon.Rank, updatedDemon.Level)
							if activeHub != nil {
								activeHub.BroadcastWildDemonsList(activeDemonMgr.GetWildDemons())
							}
							content := fmt.Sprintf("⚡ PASSAGE FINISH BURST (-%d HP)! Exorcised %s (Lv. %d)! Total Defeated: %d", burstDamage, updatedDemon.Name, updatedDemon.Level, newSettings.DemonsDefeated)
							if leveledUp {
								content = fmt.Sprintf("⚡ PASSAGE FINISH BURST (-%d HP)! Exorcised %s! Your Guardian Angel reached Level %d!", burstDamage, updatedDemon.Name, newSettings.GuardianLevel)
							}
							_ = conn.WriteJSON(Message{
								Type:            "wild_demon_defeated",
								WildDemon:       updatedDemon,
								Sanctuary:       newSettings,
								TrackerUnlocked: newSettings.TrackerUnlocked,
								DemonsDefeated:  newSettings.DemonsDefeated,
								DemonDefeated:   true,
								Damage:          burstDamage,
								BurstDamage:     burstDamage,
								SyncStacks:      msg.SyncStacks,
								Content:         content,
							})
						} else {
							var nextUserVerse *Verse
							if currentMode == "verse" && activeVerseIdx < len(bookVerses) {
								vNext := bookVerses[activeVerseIdx]
								nextUserVerse = &vNext
							}
							advDemon, destV, _ := activeDemonMgr.DriveWildDemonToNextVerse(activeWildDemonBeforeAdvance.ID, nextUserVerse)
							if advDemon == nil {
								advDemon = updatedDemon
							}
							_ = conn.WriteJSON(Message{
								Type:        "wild_demon_burst",
								WildDemon:   advDemon,
								Damage:      burstDamage,
								BurstDamage: burstDamage,
								SyncStacks:  msg.SyncStacks,
								Verse:       &destV,
								Content:     fmt.Sprintf("⚡ PASSAGE BURST! Dealt %d Holy Burst Damage to %s (%d/%d HP)! Pursuing to %s %d:%d!", burstDamage, advDemon.Name, advDemon.CurrentHP, advDemon.MaxHP, destV.BookName, destV.Chapter, destV.Verse),
							})
						}
					}
				}

				stats := currentStats()

				// Check newly unlocked customizations based on speed, verses, streak
				totVerses := 0
				longStreak := 0
				if streak != nil {
					totVerses = streak.TotalVersesTyped
					longStreak = streak.LongestStreak
				}
				topWPM := max(prevTopWPM, instantWPM)
				newUnlocks := FindNewlyUnlockedCustomizations(prevTopWPM, topWPM, prevVerses, totVerses, prevLongStreak, longStreak, 0)
				for _, unl := range newUnlocks {
					unlCopy := unl
					_ = conn.WriteJSON(Message{
						Type:      "customization_unlocked",
						NewUnlock: &unlCopy,
						Content:   FormatUnlockToast(unlCopy),
					})
				}

				_ = conn.WriteJSON(Message{
					Type:              "correct",
					Content:           "correct",
					Stats:             stats,
					WPM:               instantWPM,
					Duration:          verifiedDuration,
					UserCompletions:   uComps,
					GlobalCompletions: gComps,
				})
				_ = conn.WriteJSON(Message{Type: "streakUpdate", Streak: streak})

				for _, badge := range rewards {
					_ = conn.WriteJSON(Message{
						Type:   "badgeUnlocked",
						Reward: &badge,
					})
				}

				if selectedBook != "" && bookProgress != nil {
					_ = conn.WriteJSON(map[string]any{
						"type":     "progressUpdate",
						"book":     selectedBook,
						"progress": bookProgress,
					})
				}

				if currentMode == "intro" {
					g, _ := getBookGuide(currentTransID, selectedBook)
					_ = conn.WriteJSON(Message{
						Type:    "modeComplete",
						Mode:    "intro",
						Content: fmt.Sprintf("✓ %s Introduction completed! Chapter 1 unlocked.", g.Name),
					})
					sendCurrentVerse()
					continue
				}

				if currentMode == "review" {
					guide, _ := getBookGuide(currentTransID, selectedBook)
					_ = conn.WriteJSON(Message{
						Type:         "complete",
						Content:      "All done with " + guide.Name + "! Praise the Sun! \\[T]/",
						Stats:        stats,
						Guide:        &guide,
						Translation:  currentTransID,
						Category:     guide.Category,
						ReviewLocked: false,
					})
					sendCurrentVerse()
					continue
				}

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

				if activeAngelMgr != nil {
					activeAngelMgr.UpdateCompanionWPM(uid)
				}
				sendCurrentVerse()
			} else {
				if bookProgress != nil {
					bookProgress.Mistakes++
					_ = activeStore.UpdateBookProgress(ctx, uid, currentTransID, selectedBook, bookProgress)
				}
				runtimeStats.CharsTyped += runeLen(userInput)

				_ = conn.WriteJSON(Message{Type: "wrong", Content: "wrong", Stats: currentStats()})
			}
		}
	}
}

// ---- Main ----

func main() {
	if err := loadTranslationsAndGuides(); err != nil {
		log.Fatalf("Failed to initialize bible datasets: %v", err)
	}

	globalHub = newHub()
	go globalHub.run()

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

	globalAngelManager = newAngelManager(store, globalHub)
	globalAngelManager.StartGlobalAngel("michael")

	dm := newDemonManager(store, globalHub)
	setGlobalDemonManager(dm)
	dm.StartDevil24_7("imp", 1)

	initAdminSystem(".elespe")
	registerAdminRoutes(http.DefaultServeMux)

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

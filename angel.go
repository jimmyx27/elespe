package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

// Predefined Angel Profiles with Levels
var angelTemplates = map[string]AngelInfo{
	"guardian": {
		ID:          "angel-guardian",
		Rank:        "guardian",
		Name:        "Personal Guardian Angel",
		Title:       "Faithful Adaptive Guide",
		Description: "Your personal celestial companion, forever by your side. Adapts within ±10% of your speed to nurture your growth.",
		WPM:         40,
		Color:       "#818cf8",
		Icon:        "👼",
		IsGuardian:  true,
		PaceOffset:  0,
		Level:       1,
		MaxLevel:    5,
	},
	"cherub": {
		ID:          "angel-cherub",
		Rank:        "cherub",
		Name:        "Guardian Cherub",
		Title:       "Gentle Guardian",
		Description: "A gentle, patient angel typing scripture with steady, comforting grace. Perfect for beginners and contemplative meditation.",
		WPM:         20,
		Color:       "#60a5fa",
		Icon:        "🕊️",
		Level:       1,
		MaxLevel:    5,
	},
	"seraph": {
		ID:          "angel-seraph",
		Rank:        "seraph",
		Name:        "Seraph Herald",
		Title:       "Faithful Messenger",
		Description: "A bright burning messenger of praise, typing scripture with disciplined rhythm and devotion.",
		WPM:         60,
		Color:       "#fbbf24",
		Icon:        "📯",
		Level:       2,
		MaxLevel:    4,
	},
	"gabriel": {
		ID:          "angel-gabriel",
		Rank:        "gabriel",
		Name:        "Archangel Gabriel",
		Title:       "Divine Announcer",
		Description: "The supreme herald of sacred revelations and glad tidings. Inscribes scripture with swift, unerring clarity.",
		WPM:         100,
		Color:       "#22d3ee",
		Icon:        "🎺",
		Level:       2,
		MaxLevel:    4,
	},
	"michael": {
		ID:          "angel-michael",
		Rank:        "michael",
		Name:        "Big Mike",
		Title:       "Leader of the Heavenly Hosts",
		Description: "Armed with the sword of holy truth! Inscribes sacred verses BLAZINGLY FAST!",
		WPM:         160,
		Color:       "#f97316",
		Icon:        "⚔️",
		Level:       777,
		MaxLevel:    7777,
	},
}

// GetAngelWPM calculates the exact speed for an angel rank and level
func GetAngelWPM(rank string, level int) int {
	if level < 1 {
		level = 1
	}
	switch rank {
	case "cherub":
		// Cherub: Lv.1 20 WPM, Lv.2 25 WPM, Lv.3 30 WPM, Lv.4 35 WPM, Lv.5 40 WPM
		if level > 5 {
			level = 5
		}
		return 20 + (level-1)*5
	case "seraph":
		// Seraph: Lv.1 50 WPM, Lv.2 60 WPM, Lv.3 70 WPM, Lv.4 80 WPM
		if level > 4 {
			level = 4
		}
		return 50 + (level-1)*10
	case "gabriel":
		// Gabriel: Lv.1 85 WPM, Lv.2 100 WPM, Lv.3 115 WPM, Lv.4 130 WPM
		if level > 4 {
			level = 4
		}
		return 85 + (level-1)*15
	case "michael":
		// Michael: Lv.1 140 WPM, Lv.2 160 WPM, Lv.3 180 WPM, Lv.4 200 WPM
		if level > 4 {
			level = 777
		}
		return 140 + (level-1)*20
	default:
		return 777
	}
}

// Ordered list of angel templates for UI display
var canonicalAngels = []string{"guardian", "cherub", "seraph", "gabriel", "michael"}

// AngelBot represents an active typing instance of an angel
type AngelBot struct {
	Info        AngelInfo
	Translation string
	BookName    string
	Chapter     int
	Verse       int
	Text        string
	CharIndex   int
	StopChan    chan struct{}
	IsRunning   bool
	mu          sync.RWMutex
}

// AngelManager orchestrates the autonomous global angel scribe and client companions
type AngelManager struct {
	mu             sync.RWMutex
	store          Store
	hub            *Hub
	globalAngel    *AngelBot
	companions     map[string]*AngelBot              // uid -> active companion bot
	guardians      map[string]*AngelBot              // uid -> persistent per-player guardian angel
	michaelTrail   map[string]time.Time              // verseKey -> timestamp when Big Mike sanctified the verse
	michaelLease   int                               // remaining verses in Big Mike's summoned crusade lease
	offlineReports map[string]*GuardianOfflineReport // uid -> offline autonomous guardian journal
	cancelGlobal   chan struct{}
}

var globalAngelManager *AngelManager

func newAngelManager(s Store, h *Hub) *AngelManager {
	m := &AngelManager{
		store:          s,
		hub:            h,
		companions:     make(map[string]*AngelBot),
		guardians:      make(map[string]*AngelBot),
		michaelTrail:   make(map[string]time.Time),
		offlineReports: make(map[string]*GuardianOfflineReport),
		cancelGlobal:   make(chan struct{}),
	}
	return m
}

func verseTrailKey(trans, book string, chapter, verse int) string {
	return fmt.Sprintf("%s|%s|%d|%d", trans, book, chapter, verse)
}

// RecordMichaelTrail marks a verse as graced by Big Mike's Glorious Golden Trail for 30 minutes.
func (m *AngelManager) RecordMichaelTrail(trans, book string, chapter, verse int) {
	if book == "" || chapter <= 0 || verse <= 0 {
		return
	}
	now := time.Now().UTC()
	m.mu.Lock()
	m.michaelTrail[verseTrailKey(trans, book, chapter, verse)] = now
	m.michaelTrail[verseTrailKey("", book, chapter, verse)] = now
	m.mu.Unlock()
}

// IsVerseOnMichaelTrail returns true if Big Mike is currently on this verse or inscribed it within the last 30 minutes.
func (m *AngelManager) IsVerseOnMichaelTrail(trans, book string, chapter, verse int) bool {
	if book == "" || chapter <= 0 || verse <= 0 {
		return false
	}
	m.mu.RLock()
	bot := m.globalAngel
	ts, ok := m.michaelTrail[verseTrailKey(trans, book, chapter, verse)]
	if !ok {
		ts, ok = m.michaelTrail[verseTrailKey("", book, chapter, verse)]
	}
	m.mu.RUnlock()

	if ok && time.Since(ts) <= 30*time.Minute {
		return true
	}
	if bot != nil {
		bot.mu.RLock()
		onVerse := bot.Info.Rank == "michael" && bot.BookName == book && bot.Chapter == chapter && bot.Verse == verse
		bot.mu.RUnlock()
		if onVerse {
			return true
		}
	}
	return false
}

// RecordOfflineGuardianBlessing records a fellowship blessing received by a user's Guardian Angel while offline.
func (m *AngelManager) RecordOfflineGuardianBlessing(uid string, xp int) {
	if uid == "" {
		return
	}
	if m.hub != nil && m.hub.IsUserConnected(uid) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rep, ok := m.offlineReports[uid]
	if !ok || rep == nil {
		rep = &GuardianOfflineReport{
			GuardianName:  "Personal Guardian Angel",
			GuardianLevel: 1,
		}
		if g, hasG := m.guardians[uid]; hasG && g != nil {
			g.mu.RLock()
			rep.GuardianName = g.Info.Name
			rep.GuardianLevel = g.Info.Level
			if g.BookName != "" {
				loc := fmt.Sprintf("%s %d:%d", g.BookName, g.Chapter, g.Verse)
				rep.StartLocation = loc
				rep.EndLocation = loc
			}
			g.mu.RUnlock()
		}
		m.offlineReports[uid] = rep
	}
	rep.BlessingsReceived++
	if xp > 0 {
		rep.BonusXPEarned += xp
	}
}

// ConsumeOfflineReport returns and clears any accumulated autonomous Guardian Angel report when the player reconnects.
func (m *AngelManager) ConsumeOfflineReport(uid string) *GuardianOfflineReport {
	if uid == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rep, ok := m.offlineReports[uid]
	if !ok || rep == nil {
		return nil
	}
	delete(m.offlineReports, uid)
	if rep.VersesInscribed <= 0 && rep.BlessingsReceived <= 0 && rep.DemonsWeakened <= 0 {
		return nil
	}
	cp := *rep
	return &cp
}

// GetAngelTemplates returns the list of all available angels
func (m *AngelManager) GetAngelTemplates() []AngelInfo {
	return m.GetAngelTemplatesForUser("")
}

// GetAngelTemplatesForUser returns all templates with the user's personal guardian angel adapted to their WPM
func (m *AngelManager) GetAngelTemplatesForUser(uid string) []AngelInfo {
	list := make([]AngelInfo, 0, len(canonicalAngels))

	var userAvgWPM int
	var paceOffset int = 0
	var guardianName string = "Personal Guardian Angel"

	var preferredLevels map[string]int
	var guardianLevel int = 1
	var ownerName string
	var ownerColor string = "#818cf8"
	if m.store != nil && uid != "" {
		if acc, _ := m.store.GetAccount(context.Background(), uid); acc != nil {
			ownerName = acc.DisplayName
			if acc.AvatarColor != "" {
				ownerColor = acc.AvatarColor
			}
		}
		an, _ := m.store.GetUserAnalytics(context.Background(), uid)
		if an != nil && an.AverageWPM > 0 {
			userAvgWPM = an.AverageWPM
		} else if an != nil && an.TopWPM > 0 {
			userAvgWPM = int(float64(an.TopWPM) * 0.75)
		}
		set, _ := m.store.GetCustomizations(context.Background(), uid)
		if set != nil {
			paceOffset = set.GuardianPace
			if set.GuardianName != "" {
				guardianName = set.GuardianName
			}
			preferredLevels = set.PreferredAngelLevels
			if set.GuardianLevel > 0 {
				guardianLevel = set.GuardianLevel
			}
		}
	}

	if paceOffset < -10 {
		paceOffset = -10
	}
	if paceOffset > 10 {
		paceOffset = 10
	}

	michaelCost := 1
	michaelCurrency := "book_tokens"
	if adm := getGlobalAdmin(); adm != nil {
		opts := adm.GetOptions()
		if opts.MichaelSummonCurrencyType != "" {
			michaelCurrency = opts.MichaelSummonCurrencyType
		}
		if opts.MichaelSummonCost >= 0 {
			michaelCost = opts.MichaelSummonCost
		}
	}

	for _, rank := range canonicalAngels {
		if info, ok := angelTemplates[rank]; ok {
			inf := info
			if inf.Rank == "guardian" {
				if uid != "" {
					inf.ID = "angel-guardian-" + uid
					inf.OwnerUID = uid
					inf.OwnerName = ownerName
					inf.OwnerColor = ownerColor
				}
				inf.Name = guardianName
				inf.PaceOffset = paceOffset
				inf.Level = guardianLevel
				inf.MaxLevel = 777
				levelBonus := (guardianLevel - 1) * 2
				if userAvgWPM > 0 {
					calcWPM := int(math.Round(float64(userAvgWPM)*(1.0+float64(paceOffset)/100.0))) + levelBonus
					if calcWPM < 15 {
						calcWPM = 15
					}
					inf.WPM = calcWPM
				} else {
					inf.WPM = 40 + paceOffset + levelBonus
				}
				var paceLabel string
				if paceOffset > 0 {
					paceLabel = fmt.Sprintf("+%d%%", paceOffset)
				} else if paceOffset < 0 {
					paceLabel = fmt.Sprintf("%d%%", paceOffset)
				} else {
					paceLabel = "Match"
				}
				inf.Title = fmt.Sprintf("Your Pace (%s · Lv. %d)", paceLabel, guardianLevel)
			} else {
				if inf.Rank == "michael" {
					inf.SummonCost = michaelCost
					inf.CurrencyType = michaelCurrency
				}
				if preferredLevels != nil {
					if lvl, ok := preferredLevels[inf.Rank]; ok && lvl >= 1 && lvl <= inf.MaxLevel {
						inf.Level = lvl
						inf.WPM = GetAngelWPM(inf.Rank, lvl)
					}
				}
			}
			list = append(list, inf)
		}
	}
	return list
}

// GetGlobalAngelStatus returns the live status of the server's autonomous angel
func (m *AngelManager) GetGlobalAngelStatus() *AngelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.globalAngel == nil {
		return nil
	}
	m.globalAngel.mu.RLock()
	defer m.globalAngel.mu.RUnlock()

	info := m.globalAngel.Info
	info.BookName = m.globalAngel.BookName
	info.Chapter = m.globalAngel.Chapter
	info.Verse = m.globalAngel.Verse
	info.CharIndex = m.globalAngel.CharIndex
	if len(m.globalAngel.Text) > 0 {
		info.Progress = float64(m.globalAngel.CharIndex) / float64(len(m.globalAngel.Text))
	}
	return &info
}

// StartGlobalAngel runs a background bot that continuously types verses across Scripture
func (m *AngelManager) StartGlobalAngel(defaultRank string) {
	m.StopGlobalAngel()

	template, ok := angelTemplates[defaultRank]
	if !ok {
		template = angelTemplates["cherub"]
	}

	bot := &AngelBot{
		Info:        template,
		Translation: "kjv",
		BookName:    "Genesis",
		Chapter:     1,
		Verse:       1,
		StopChan:    make(chan struct{}),
		IsRunning:   true,
	}

	m.mu.Lock()
	m.globalAngel = bot
	m.mu.Unlock()

	go m.runGlobalLoop(bot)
}

// StopGlobalAngel stops the autonomous global angel scribe if running.
func (m *AngelManager) StopGlobalAngel() {
	m.mu.Lock()
	bot := m.globalAngel
	m.globalAngel = nil
	m.mu.Unlock()

	if bot != nil {
		bot.mu.Lock()
		if bot.IsRunning {
			bot.IsRunning = false
			close(bot.StopChan)
		}
		bot.mu.Unlock()
	}
}

// SetGlobalAngelRank updates the global angel's rank if running or starts it with the given rank.
func (m *AngelManager) SetGlobalAngelRank(rank string) {
	m.mu.RLock()
	running := m.globalAngel != nil
	curRank := ""
	if m.globalAngel != nil {
		m.globalAngel.mu.RLock()
		curRank = m.globalAngel.Info.Rank
		m.globalAngel.mu.RUnlock()
	}
	m.mu.RUnlock()

	if running && curRank != rank {
		m.StartGlobalAngel(rank)
	}
}

// Stop cleanly shuts down the global angel and all active user companions and guardians.
func (m *AngelManager) Stop() {
	m.StopGlobalAngel()

	m.mu.Lock()
	defer m.mu.Unlock()
	for uid, bot := range m.companions {
		bot.mu.Lock()
		if bot.IsRunning {
			bot.IsRunning = false
			close(bot.StopChan)
		}
		bot.mu.Unlock()
		delete(m.companions, uid)
	}
	for uid, bot := range m.guardians {
		bot.mu.Lock()
		if bot.IsRunning {
			bot.IsRunning = false
			close(bot.StopChan)
		}
		bot.mu.Unlock()
		delete(m.guardians, uid)
	}
}

// SummonGlobalMichaelToVerse relocates the global Big Mike scribe directly to the specified verse,
// from where he will continue grinding linearly across Scripture.
func (m *AngelManager) SummonGlobalMichaelToVerse(trans, book string, chapter, verse int, text string) *AngelInfo {
	m.mu.Lock()
	bot := m.globalAngel
	m.michaelLease = 10
	m.mu.Unlock()
	if bot == nil {
		return nil
	}
	if trans == "" {
		trans = "kjv"
	}
	bot.mu.Lock()
	bot.Translation = trans
	bot.BookName = book
	bot.Chapter = chapter
	bot.Verse = verse
	bot.Text = cleanVerseText(text)
	bot.CharIndex = 0
	info := bot.Info
	info.Translation = trans
	info.BookName = book
	info.Chapter = chapter
	info.Verse = verse
	info.CharIndex = 0
	bot.mu.Unlock()

	m.RecordMichaelTrail(trans, book, chapter, verse)

	if dm := getGlobalDemonManager(); dm != nil {
		dm.SlayWildDemonByMichael(trans, book, chapter, verse)
	}
	return &info
}

func (m *AngelManager) runGlobalLoop(bot *AngelBot) {
	for {
		select {
		case <-bot.StopChan:
			return
		default:
		}

		bot.mu.RLock()
		curTrans := bot.Translation
		curBook := bot.BookName
		curCh := bot.Chapter
		curV := bot.Verse
		bot.mu.RUnlock()
		if curTrans == "" {
			curTrans = "kjv"
		}
		if curBook == "" {
			curBook = "Genesis"
		}
		if curCh <= 0 {
			curCh = 1
		}
		if curV <= 0 {
			curV = 1
		}

		// Find verse text from datasets
		text := ""
		td, ok := translations[curTrans]
		if ok {
			vList := td.VersesByBook[curBook]
			for _, v := range vList {
				if v.Chapter == curCh && v.Verse == curV {
					text = cleanVerseText(v.Text)
					break
				}
			}
		}
		if text == "" {
			text = "In the beginning God created the heaven and the earth."
		}

		// Normal angels only move when users have the site open, but Michael forever cruises 24/7!
		if bot.Info.Rank != "michael" && m.hub != nil && m.hub.ClientCount() == 0 {
			select {
			case <-bot.StopChan:
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}

		bot.mu.Lock()
		// If relocated externally while resolving text, restart loop with the new verse
		if bot.BookName != "" && (bot.BookName != curBook || bot.Chapter != curCh || bot.Verse != curV) {
			bot.mu.Unlock()
			continue
		}
		bot.Translation = curTrans
		bot.BookName = curBook
		bot.Chapter = curCh
		bot.Verse = curV
		bot.Text = text
		bot.CharIndex = 0
		bot.Info.BookName = curBook
		bot.Info.Chapter = curCh
		bot.Info.Verse = curV
		wpm := bot.Info.WPM
		isMichael := bot.Info.Rank == "michael"
		bot.mu.Unlock()

		if isMichael {
			m.RecordMichaelTrail(curTrans, curBook, curCh, curV)
		}

		// Slay any wild demons lurking on this verse along the way!
		if dm := getGlobalDemonManager(); dm != nil {
			dm.SlayWildDemonByMichael(curTrans, curBook, curCh, curV)
		}

		relocated := false
		// When no human clients are viewing this verse, Michael cruises in coarse ticks without per-char broadcasts
		if m.hub != nil && !m.hub.HasClientsOnVerse(curTrans, curBook, curCh, curV) && isMichael {
			select {
			case <-bot.StopChan:
				return
			case <-time.After(1500 * time.Millisecond):
			}
			bot.mu.Lock()
			if bot.Translation != curTrans || bot.BookName != curBook || bot.Chapter != curCh || bot.Verse != curV {
				relocated = true
			} else {
				bot.CharIndex = len([]rune(text))
			}
			bot.mu.Unlock()
		} else {
			// Calculate typing cadence:
			// Standard: 5 chars per word -> delay per char = 60s / (WPM * 5)
			charDelay := time.Duration(float64(time.Minute) / float64(wpm*5))
			if charDelay < 25*time.Millisecond {
				charDelay = 25 * time.Millisecond
			}

			textRunes := []rune(text)
			totalChars := len(textRunes)

			for i := 0; i <= totalChars; i++ {
				select {
				case <-bot.StopChan:
					return
				default:
				}

				if !isMichael && m.hub != nil && m.hub.ClientCount() == 0 {
					select {
					case <-bot.StopChan:
						return
					case <-time.After(1 * time.Second):
						continue
					}
				}

				bot.mu.Lock()
				if bot.Translation != curTrans || bot.BookName != curBook || bot.Chapter != curCh || bot.Verse != curV {
					bot.mu.Unlock()
					relocated = true
					break
				}
				bot.CharIndex = i
				bot.mu.Unlock()

				hasViewers := m.hub == nil || m.hub.HasClientsOnVerse(curTrans, curBook, curCh, curV)
				if !hasViewers && i < totalChars {
					rem := totalChars - i
					remDur := charDelay * time.Duration(rem)
					stepDur := 1 * time.Second
					if remDur < stepDur {
						stepDur = remDur
					}
					stepChars := max(1, int(stepDur/charDelay))
					if stepChars > rem {
						stepChars = rem
					}
					select {
					case <-bot.StopChan:
						return
					case <-time.After(stepDur):
					}
					i += stepChars - 1
					continue
				}

				// Broadcast ghost cursor to anyone on this verse
				if m.hub != nil && hasViewers {
					m.hub.BroadcastAngelCursor(curTrans, curBook, curCh, curV, GhostCursor{
						UID:             bot.Info.ID,
						DisplayName:     bot.Info.Icon + " " + bot.Info.Name,
						AvatarColor:     bot.Info.Color,
						CharIndex:       i,
						WPM:             wpm,
						IsAngel:         true,
						AngelRank:       bot.Info.Rank,
						AngelLevel:      bot.Info.Level,
						TrailUpTo:       i,
						IsGloriousTrail: isMichael,
					})
				}

				// Add small natural jitter (±15%)
				jitter := time.Duration((rand.Float64()*0.3 - 0.15) * float64(charDelay))
				sleepDur := charDelay + jitter
				if sleepDur < 10*time.Millisecond {
					sleepDur = 10 * time.Millisecond
				}
				select {
				case <-bot.StopChan:
					return
				case <-time.After(sleepDur):
				}
			}
		}

		if relocated {
			continue
		}

		// Verse completed by global angel!
		if isMichael {
			m.RecordMichaelTrail(curTrans, curBook, curCh, curV)
			m.mu.Lock()
			if m.michaelLease > 0 {
				m.michaelLease--
			}
			m.mu.Unlock()
		}

		// Record global completion count
		if m.store != nil {
			_, _, _ = m.store.RecordVerseCompletion(context.Background(), bot.Info.ID, curTrans, curBook, curCh, curV)
		}

		// Slay any wild demons that may have spawned or remained on this verse
		if dm := getGlobalDemonManager(); dm != nil {
			dm.SlayWildDemonByMichael(curTrans, curBook, curCh, curV)
		}

		// Strictly linear canonical progression across Scripture until summoned!
		if nextV, ok := getNextCanonicalVerse(curTrans, curBook, curCh, curV); ok {
			bot.mu.Lock()
			if bot.Translation == curTrans && bot.BookName == curBook && bot.Chapter == curCh && bot.Verse == curV {
				bot.BookName = nextV.BookName
				bot.Chapter = nextV.Chapter
				bot.Verse = nextV.Verse
				bot.Info.BookName = nextV.BookName
				bot.Info.Chapter = nextV.Chapter
				bot.Info.Verse = nextV.Verse
				bot.CharIndex = 0
			}
			bot.mu.Unlock()
		}

		// Brief holy reflection pause between verses
		select {
		case <-bot.StopChan:
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// EnsurePlayerGuardian ensures that a player's persistent Guardian Angel exists and is synced to their verse.
// While the player is active, the Guardian works through the same verse alongside them; when the player stops or leaves,
// the Guardian continues grinding linearly across Scripture as the player's ghost without granting user progress.
func (m *AngelManager) EnsurePlayerGuardian(uid, displayName, avatarColor, trans, book string, chapter, verse int, text string) *AngelInfo {
	if uid == "" {
		return nil
	}
	if trans == "" {
		trans = "kjv"
	}
	if book == "" {
		book = "Genesis"
	}
	if chapter <= 0 {
		chapter = 1
	}
	if verse <= 0 {
		verse = 1
	}

	resolvePassageText := func() string {
		cleaned := cleanVerseText(text)
		if cleaned == "" {
			if td, ok := translations[trans]; ok {
				for _, v := range td.VersesByBook[book] {
					if v.Chapter == chapter && v.Verse == verse {
						cleaned = cleanVerseText(v.Text)
						break
					}
				}
			}
			if cleaned == "" {
				cleaned = "In the beginning God created the heaven and the earth."
			}
		}
		return cleaned
	}

	// Fast path: if the player's persistent Guardian Angel is already running, update its location without querying analytics/DB!
	m.mu.Lock()
	existing, ok := m.guardians[uid]
	if ok && existing != nil {
		existing.mu.Lock()
		if existing.IsRunning {
			if displayName != "" {
				existing.Info.OwnerName = displayName
			}
			if avatarColor != "" {
				existing.Info.OwnerColor = avatarColor
			}
			existing.Info.OwnerUID = uid
			existing.Info.Translation = trans
			existing.Info.BookName = book
			existing.Info.Chapter = chapter
			existing.Info.Verse = verse
			if existing.Translation != trans || existing.BookName != book || existing.Chapter != chapter || existing.Verse != verse {
				existing.Translation = trans
				existing.BookName = book
				existing.Chapter = chapter
				existing.Verse = verse
				existing.Text = resolvePassageText()
				existing.CharIndex = 0
			}
			info := existing.Info
			existing.mu.Unlock()
			if _, hasComp := m.companions[uid]; !hasComp {
				m.companions[uid] = existing
			}
			m.mu.Unlock()
			return &info
		}
		existing.mu.Unlock()
	}
	m.mu.Unlock()

	cleanedText := resolvePassageText()

	var template AngelInfo
	templates := m.GetAngelTemplatesForUser(uid)
	for _, t := range templates {
		if t.Rank == "guardian" {
			template = t
			break
		}
	}
	if template.Rank == "" {
		template = angelTemplates["guardian"]
	}
	template.ID = "angel-guardian-" + uid
	template.IsGuardian = true
	template.OwnerUID = uid
	if displayName != "" {
		template.OwnerName = displayName
	} else if template.OwnerName == "" {
		if len(uid) >= 6 {
			template.OwnerName = "Scribe " + uid[:6]
		} else {
			template.OwnerName = "Scribe " + uid
		}
	}
	if avatarColor != "" {
		template.OwnerColor = avatarColor
	} else if template.OwnerColor == "" {
		template.OwnerColor = template.Color
	}
	template.Translation = trans
	template.BookName = book
	template.Chapter = chapter
	template.Verse = verse

	m.mu.Lock()
	if existing, ok := m.guardians[uid]; ok && existing != nil {
		existing.mu.Lock()
		if existing.IsRunning {
			info := existing.Info
			existing.mu.Unlock()
			m.mu.Unlock()
			return &info
		}
		existing.mu.Unlock()
	}

	bot := &AngelBot{
		Info:        template,
		Translation: trans,
		BookName:    book,
		Chapter:     chapter,
		Verse:       verse,
		Text:        cleanedText,
		CharIndex:   0,
		StopChan:    make(chan struct{}),
		IsRunning:   true,
	}
	m.guardians[uid] = bot
	if comp, hasComp := m.companions[uid]; !hasComp || comp == nil || comp.Info.IsGuardian {
		m.companions[uid] = bot
	}
	m.mu.Unlock()

	go m.runCompanionLoop(uid, bot)

	info := template
	return &info
}

// SummonCompanion creates or switches an Angel companion for a user's active verse
func (m *AngelManager) SummonCompanion(uid, rank, trans, book string, chapter, verse int, text string) *AngelInfo {
	return m.SummonCompanionWithLevel(uid, rank, 0, trans, book, chapter, verse, text)
}

// SummonCompanionWithLevel creates or switches an Angel companion with a specified level
func (m *AngelManager) SummonCompanionWithLevel(uid, rank string, level int, trans, book string, chapter, verse int, text string) *AngelInfo {
	if rank == "" || rank == "guardian" {
		info := m.EnsurePlayerGuardian(uid, "", "", trans, book, chapter, verse, text)
		m.mu.Lock()
		if g, ok := m.guardians[uid]; ok {
			if prev, hasPrev := m.companions[uid]; hasPrev && prev != nil && prev != g && !prev.Info.IsGuardian {
				prev.mu.Lock()
				if prev.IsRunning {
					prev.IsRunning = false
					close(prev.StopChan)
				}
				prev.mu.Unlock()
			}
			m.companions[uid] = g
		}
		m.mu.Unlock()
		return info
	}

	// Dismiss any previous non-guardian companion without stopping the player's persistent guardian
	m.DismissCompanion(uid)

	if rank == "michael" {
		m.SummonGlobalMichaelToVerse(trans, book, chapter, verse, text)
	}

	template, ok := angelTemplates[rank]
	if !ok {
		template = angelTemplates["cherub"]
	}
	if level > 0 {
		template.Level = level
		template.WPM = GetAngelWPM(rank, level)
	}
	template.Translation = trans
	template.BookName = book
	template.Chapter = chapter
	template.Verse = verse

	bot := &AngelBot{
		Info:        template,
		Translation: trans,
		BookName:    book,
		Chapter:     chapter,
		Verse:       verse,
		Text:        cleanVerseText(text),
		CharIndex:   0,
		StopChan:    make(chan struct{}),
		IsRunning:   true,
	}

	m.mu.Lock()
	m.companions[uid] = bot
	m.mu.Unlock()

	go m.runCompanionLoop(uid, bot)

	info := template
	return &info
}

// UpdateCompanionWPM updates the guardian companion bot's speed dynamically as user improves
func (m *AngelManager) UpdateCompanionWPM(uid string) {
	m.mu.RLock()
	bot, ok := m.guardians[uid]
	if !ok || bot == nil {
		bot = m.companions[uid]
	}
	m.mu.RUnlock()
	if bot == nil || !bot.Info.IsGuardian {
		return
	}

	templates := m.GetAngelTemplatesForUser(uid)
	for _, t := range templates {
		if t.Rank == "guardian" {
			bot.mu.Lock()
			bot.Info.WPM = t.WPM
			bot.Info.Level = t.Level
			bot.Info.Title = t.Title
			bot.Info.Name = t.Name
			if t.OwnerName != "" {
				bot.Info.OwnerName = t.OwnerName
			}
			if t.OwnerColor != "" {
				bot.Info.OwnerColor = t.OwnerColor
			}
			bot.mu.Unlock()
			break
		}
	}
}

// DismissCompanion stops any temporary non-guardian companion for a user, while keeping their persistent Guardian Angel running in m.guardians.
func (m *AngelManager) DismissCompanion(uid string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if bot, ok := m.companions[uid]; ok && bot != nil {
		if !bot.Info.IsGuardian {
			bot.mu.Lock()
			if bot.IsRunning {
				bot.IsRunning = false
				close(bot.StopChan)
			}
			bot.mu.Unlock()
		}
		delete(m.companions, uid)
	}
}

// OnUserDisconnect is called when a player's WebSocket closes.
// Temporary non-guardian companions are dismissed, while the player's Guardian Angel continues grinding linearly across Scripture.
func (m *AngelManager) OnUserDisconnect(uid string) {
	m.DismissCompanion(uid)
}

// GetActiveCompanion returns current active companion info for a user or nil
func (m *AngelManager) GetActiveCompanion(uid string) *AngelInfo {
	m.mu.RLock()
	bot, ok := m.companions[uid]
	if !ok || bot == nil {
		bot = m.guardians[uid]
	}
	m.mu.RUnlock()
	if bot == nil {
		return nil
	}
	bot.mu.RLock()
	defer bot.mu.RUnlock()
	info := bot.Info
	info.Translation = bot.Translation
	info.BookName = bot.BookName
	info.Chapter = bot.Chapter
	info.Verse = bot.Verse
	info.CharIndex = bot.CharIndex
	if len(bot.Text) > 0 {
		info.Progress = float64(bot.CharIndex) / float64(len(bot.Text))
	}
	return &info
}

// GetPlayerGuardian returns the persistent Guardian Angel info for a specific player UID.
func (m *AngelManager) GetPlayerGuardian(uid string) *AngelInfo {
	m.mu.RLock()
	bot, ok := m.guardians[uid]
	m.mu.RUnlock()
	if !ok || bot == nil {
		return nil
	}
	bot.mu.RLock()
	defer bot.mu.RUnlock()
	info := bot.Info
	info.Translation = bot.Translation
	info.BookName = bot.BookName
	info.Chapter = bot.Chapter
	info.Verse = bot.Verse
	info.CharIndex = bot.CharIndex
	if len(bot.Text) > 0 {
		info.Progress = float64(bot.CharIndex) / float64(len(bot.Text))
	}
	if m.hub != nil {
		info.PlayerOnline = m.hub.IsUserConnected(uid)
	}
	return &info
}

// GetRoamingGuardians returns all active player Guardian Angels across Scripture so players can see, jump to, and interact with them.
func (m *AngelManager) GetRoamingGuardians() []AngelInfo {
	m.mu.RLock()
	bots := make(map[string]*AngelBot, len(m.guardians))
	for uid, b := range m.guardians {
		if b != nil {
			bots[uid] = b
		}
	}
	m.mu.RUnlock()

	list := make([]AngelInfo, 0, len(bots))
	for uid, bot := range bots {
		bot.mu.RLock()
		if !bot.IsRunning {
			bot.mu.RUnlock()
			continue
		}
		info := bot.Info
		info.OwnerUID = uid
		info.Translation = bot.Translation
		info.BookName = bot.BookName
		info.Chapter = bot.Chapter
		info.Verse = bot.Verse
		info.CharIndex = bot.CharIndex
		if len(bot.Text) > 0 {
			info.Progress = float64(bot.CharIndex) / float64(len(bot.Text))
		}
		bot.mu.RUnlock()
		if m.hub != nil {
			info.PlayerOnline = m.hub.IsUserConnected(uid)
		}
		list = append(list, info)
	}
	return list
}

// GetGuardiansOnVerse returns all player Guardian Angels currently typing on a specific verse.
func (m *AngelManager) GetGuardiansOnVerse(trans, book string, chapter, verse int) []AngelInfo {
	all := m.GetRoamingGuardians()
	res := make([]AngelInfo, 0)
	for _, g := range all {
		if (g.Translation == trans || g.Translation == "") && g.BookName == book && g.Chapter == chapter && g.Verse == verse {
			res = append(res, g)
		}
	}
	return res
}

// SeedExistingUserGuardians initializes persistent Guardian Angels for existing users in the store on startup.
func (m *AngelManager) SeedExistingUserGuardians() {
	if m.store == nil {
		return
	}
	entries, err := m.store.ListGuardianSeedEntries(context.Background())
	if err != nil || len(entries) == 0 {
		return
	}
	for _, e := range entries {
		m.EnsurePlayerGuardian(e.UID, e.Name, e.Color, "kjv", e.BookName, e.Chapter, e.Verse, "")
	}
}

// UpdateCompanionLocation informs the user's Guardian Angel and any active companion of a new verse location
func (m *AngelManager) UpdateCompanionLocation(uid, trans, book string, chapter, verse int, text string) {
	if uid == "" {
		return
	}
	// Always ensure the player's persistent Guardian Angel is active and synced to their current verse
	m.EnsurePlayerGuardian(uid, "", "", trans, book, chapter, verse, text)

	m.mu.RLock()
	bot, ok := m.companions[uid]
	gBot := m.guardians[uid]
	m.mu.RUnlock()

	if !ok || bot == nil || bot == gBot {
		return
	}

	bot.mu.Lock()
	bot.Translation = trans
	bot.BookName = book
	bot.Chapter = chapter
	bot.Verse = verse
	bot.Text = cleanVerseText(text)
	bot.CharIndex = 0
	bot.Info.Translation = trans
	bot.Info.BookName = book
	bot.Info.Chapter = chapter
	bot.Info.Verse = verse
	bot.mu.Unlock()
}

func (m *AngelManager) runCompanionLoop(uid string, bot *AngelBot) {
	for {
		bot.mu.RLock()
		running := bot.IsRunning
		isGuardian := bot.Info.IsGuardian || bot.Info.Rank == "guardian"
		isMichael := bot.Info.Rank == "michael"
		wpm := bot.Info.WPM
		text := bot.Text
		trans := bot.Translation
		book := bot.BookName
		ch := bot.Chapter
		v := bot.Verse
		bot.mu.RUnlock()

		if !running || text == "" {
			select {
			case <-bot.StopChan:
				return
			case <-time.After(300 * time.Millisecond):
				continue
			}
		}

		if isMichael {
			m.RecordMichaelTrail(trans, book, ch, v)
		}

		charDelay := time.Duration(float64(time.Minute) / float64(wpm*5))
		if charDelay < 25*time.Millisecond {
			charDelay = 25 * time.Millisecond
		}

		textRunes := []rune(text)
		totalChars := len(textRunes)
		completed := true

		for i := 0; i <= totalChars; i++ {
			select {
			case <-bot.StopChan:
				return
			default:
			}

			// Non-guardian temporary companions only move while the user is connected;
			// Persistent player Guardian Angels NEVER stop—they continue mashing out verses as the player's ghost!
			if !isGuardian && m.hub != nil && !m.hub.IsUserConnected(uid) {
				select {
				case <-bot.StopChan:
					return
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}

			bot.mu.Lock()
			if !bot.IsRunning {
				bot.mu.Unlock()
				return
			}
			if bot.Translation != trans || bot.BookName != book || bot.Chapter != ch || bot.Verse != v {
				bot.mu.Unlock()
				completed = false
				break
			}
			bot.CharIndex = i
			botInfo := bot.Info
			bot.mu.Unlock()

			hasViewers := m.hub != nil && m.hub.HasClientsOnVerse(trans, book, ch, v)
			if !hasViewers && i < totalChars {
				rem := totalChars - i
				remDur := charDelay * time.Duration(rem)
				stepDur := 1500 * time.Millisecond
				if remDur < stepDur {
					stepDur = remDur
				}
				stepChars := max(1, int(stepDur/charDelay))
				if stepChars > rem {
					stepChars = rem
				}
				select {
				case <-bot.StopChan:
					return
				case <-time.After(stepDur):
				}
				i += stepChars - 1
				continue
			}

			// Broadcast ghost cursor to anyone on this verse
			if m.hub != nil && hasViewers {
				playerOnline := m.hub.IsUserConnected(uid)
				displayName := botInfo.Icon + " " + botInfo.Name
				if isGuardian && botInfo.OwnerName != "" {
					displayName = fmt.Sprintf("%s %s (%s)", botInfo.Icon, botInfo.Name, botInfo.OwnerName)
				}
				avatarColor := botInfo.Color
				if isGuardian && botInfo.OwnerColor != "" {
					avatarColor = botInfo.OwnerColor
				}
				m.hub.BroadcastAngelCursor(trans, book, ch, v, GhostCursor{
					UID:             botInfo.ID,
					DisplayName:     displayName,
					AvatarColor:     avatarColor,
					CharIndex:       i,
					WPM:             wpm,
					IsAngel:         true,
					AngelRank:       botInfo.Rank,
					AngelLevel:      botInfo.Level,
					OwnerUID:        uid,
					OwnerName:       botInfo.OwnerName,
					OwnerColor:      botInfo.OwnerColor,
					PlayerOnline:    playerOnline,
					TrailUpTo:       i,
					IsGloriousTrail: botInfo.Rank == "michael",
				})
			}

			select {
			case <-bot.StopChan:
				return
			case <-time.After(charDelay):
			}
		}

		if !completed {
			continue
		}

		// When companion/guardian completes the passage:
		// Record ONLY global verse completion (bot.Info.ID starts with "angel-", so it NEVER grants user progress!)
		if m.store != nil {
			_, _, _ = m.store.RecordVerseCompletion(context.Background(), bot.Info.ID, trans, book, ch, v)
		}

		// If Archangel Michael completed this verse, record his Glorious Golden Trail and slay any lurking wild demon!
		if isMichael {
			m.RecordMichaelTrail(trans, book, ch, v)
			if dm := getGlobalDemonManager(); dm != nil {
				dm.SlayWildDemonByMichael(trans, book, ch, v)
			}
		}

		bot.mu.RLock()
		infoSnap := bot.Info
		bot.mu.RUnlock()

		// Autonomous Guardian Angel skirmish against wild demons & offline journal tracking
		if isGuardian {
			var weakenedDemon *WildDemon
			if dm := getGlobalDemonManager(); dm != nil {
				weakenedDemon = dm.SkirmishWildDemonByGuardian(trans, book, ch, v, 5+infoSnap.Level)
				if weakenedDemon != nil && m.hub != nil {
					m.hub.BroadcastWildDemonsList(dm.GetWildDemons())
				}
			}

			userOnline := m.hub != nil && m.hub.IsUserConnected(uid)
			if !userOnline && uid != "" {
				loc := fmt.Sprintf("%s %d:%d", book, ch, v)
				m.mu.Lock()
				rep, ok := m.offlineReports[uid]
				if !ok || rep == nil {
					rep = &GuardianOfflineReport{
						GuardianName:  infoSnap.Name,
						GuardianLevel: infoSnap.Level,
						StartLocation: loc,
					}
					m.offlineReports[uid] = rep
				}
				rep.GuardianName = infoSnap.Name
				rep.GuardianLevel = infoSnap.Level
				if rep.StartLocation == "" {
					rep.StartLocation = loc
				}
				rep.EndLocation = loc
				rep.VersesInscribed++
				if weakenedDemon != nil {
					rep.DemonsWeakened++
				}
				m.mu.Unlock()
			}
		}

		// Notify user (if connected) that angel finished the verse
		if m.hub != nil && m.hub.IsUserConnected(uid) {
			m.hub.SendToUID(uid, Message{
				Type:    "angel_complete",
				Content: infoSnap.Icon + " " + infoSnap.Name + " has finished typing the verse at " + infoSnap.Title + " pace (" + fmt.Sprintf("%d", wpm) + " WPM)!",
				Angel:   &infoSnap,
			})
		}

		// Advance companion/guardian linearly to next canonical verse across Scripture!
		// Note: This NEVER updates the user's BookProgress; when the user stops typing or leaves,
		// their Guardian Angel continues mashing out verses on its own as their ghost.
		if nextV, ok := getNextCanonicalVerse(trans, book, ch, v); ok {
			bot.mu.Lock()
			// Only advance if the user hasn't already moved the bot to another verse
			if bot.Translation == trans && bot.BookName == book && bot.Chapter == ch && bot.Verse == v {
				bot.BookName = nextV.BookName
				bot.Chapter = nextV.Chapter
				bot.Verse = nextV.Verse
				bot.Text = cleanVerseText(nextV.Text)
				bot.CharIndex = 0
				bot.Info.BookName = nextV.BookName
				bot.Info.Chapter = nextV.Chapter
				bot.Info.Verse = nextV.Verse
				infoSnap = bot.Info
			}
			bot.mu.Unlock()

			if m.hub != nil && m.hub.IsUserConnected(uid) {
				m.hub.SendToUID(uid, Message{
					Type:    "angel_advanced",
					Content: fmt.Sprintf("%s %s advanced to %s %d:%d! (Continues ahead without granting book progress until you type the verse)", infoSnap.Icon, infoSnap.Name, nextV.BookName, nextV.Chapter, nextV.Verse),
					Angel:   &infoSnap,
					Book:    nextV.BookName,
					Verse:   &nextV,
				})
			}
		}

		// Pause briefly before typing next verse
		select {
		case <-bot.StopChan:
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

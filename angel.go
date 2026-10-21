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
	mu          sync.RWMutex
	store       Store
	hub         *Hub
	globalAngel *AngelBot
	companions  map[string]*AngelBot // uid -> companion bot
	cancelGlobal chan struct{}
}

var globalAngelManager *AngelManager

func newAngelManager(s Store, h *Hub) *AngelManager {
	m := &AngelManager{
		store:       s,
		hub:         h,
		companions:  make(map[string]*AngelBot),
		cancelGlobal: make(chan struct{}),
	}
	return m
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
	if m.store != nil && uid != "" {
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

	for _, rank := range canonicalAngels {
		if info, ok := angelTemplates[rank]; ok {
			inf := info
			if inf.Rank == "guardian" {
				inf.Name = guardianName
				inf.PaceOffset = paceOffset
				inf.Level = guardianLevel
				inf.MaxLevel = 5
				levelBonus := (guardianLevel - 1) * 2
				if userAvgWPM > 0 {
					calcWPM := int(math.Round(float64(userAvgWPM) * (1.0 + float64(paceOffset)/100.0))) + levelBonus
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

func (m *AngelManager) runGlobalLoop(bot *AngelBot) {
	curTrans := "kjv"
	curBook := "Genesis"
	curCh := 1
	curV := 1

	for {
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
			time.Sleep(1 * time.Second)
			continue
		}

		bot.mu.Lock()
		bot.Translation = curTrans
		bot.BookName = curBook
		bot.Chapter = curCh
		bot.Verse = curV
		bot.Text = text
		bot.CharIndex = 0
		wpm := bot.Info.WPM
		bot.mu.Unlock()

		// Slay any wild demons lurking on this verse along the way!
		if globalDemonManager != nil {
			globalDemonManager.SlayWildDemonByMichael(curTrans, curBook, curCh, curV)
		}

		// When no human clients are connected, Michael cruises at a brisk patrol pace
		if m.hub != nil && m.hub.ClientCount() == 0 && bot.Info.Rank == "michael" {
			time.Sleep(1500 * time.Millisecond)
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

				if bot.Info.Rank != "michael" && m.hub != nil && m.hub.ClientCount() == 0 {
					time.Sleep(1 * time.Second)
					continue
				}

				bot.mu.Lock()
				bot.CharIndex = i
				bot.mu.Unlock()

				// Broadcast ghost cursor to anyone on this verse
				if m.hub != nil {
					m.hub.BroadcastAngelCursor(curTrans, curBook, curCh, curV, GhostCursor{
						UID:         bot.Info.ID,
						DisplayName: bot.Info.Icon + " " + bot.Info.Name,
						AvatarColor: bot.Info.Color,
						CharIndex:   i,
						WPM:         wpm,
						IsAngel:     true,
						AngelRank:   bot.Info.Rank,
					})
				}

				// Add small natural jitter (±15%)
				jitter := time.Duration((rand.Float64()*0.3 - 0.15) * float64(charDelay))
				sleepDur := charDelay + jitter
				if sleepDur < 10*time.Millisecond {
					sleepDur = 10 * time.Millisecond
				}
				time.Sleep(sleepDur)
			}
		}

		// Verse completed by global angel!
		// Record global completion count
		if m.store != nil {
			_, _, _ = m.store.RecordVerseCompletion(context.Background(), bot.Info.ID, curTrans, curBook, curCh, curV)
		}

		// Slay any wild demons that may have spawned or remained on this verse
		if globalDemonManager != nil {
			globalDemonManager.SlayWildDemonByMichael(curTrans, curBook, curCh, curV)
		}

		// Advance to next canonical verse across Scripture, with occasional target crusade towards active demons
		advanced := false
		if globalDemonManager != nil && rand.Float64() < 0.25 {
			wildDemons := globalDemonManager.GetWildDemons()
			if len(wildDemons) > 0 {
				targetDemon := wildDemons[rand.Intn(len(wildDemons))]
				curBook = targetDemon.BookName
				curCh = targetDemon.Chapter
				curV = targetDemon.Verse
				advanced = true
			}
		}
		if !advanced {
			if nextV, ok := getNextCanonicalVerse(curTrans, curBook, curCh, curV); ok {
				curBook = nextV.BookName
				curCh = nextV.Chapter
				curV = nextV.Verse
			}
		}

		// Brief holy reflection pause between verses (1.5 to 2.5 seconds)
		time.Sleep(2 * time.Second)
	}
}

// SummonCompanion creates or switches an Angel companion for a user's active verse
func (m *AngelManager) SummonCompanion(uid, rank, trans, book string, chapter, verse int, text string) *AngelInfo {
	return m.SummonCompanionWithLevel(uid, rank, 0, trans, book, chapter, verse, text)
}

// SummonCompanionWithLevel creates or switches an Angel companion with a specified level
func (m *AngelManager) SummonCompanionWithLevel(uid, rank string, level int, trans, book string, chapter, verse int, text string) *AngelInfo {
	m.DismissCompanion(uid)

	template, ok := angelTemplates[rank]
	if !ok {
		template = angelTemplates["guardian"]
	}

	if template.Rank == "guardian" {
		templates := m.GetAngelTemplatesForUser(uid)
		for _, t := range templates {
			if t.Rank == "guardian" {
				template = t
				break
			}
		}
	} else if level > 0 {
		template.Level = level
		template.WPM = GetAngelWPM(rank, level)
	}

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
	info.BookName = book
	info.Chapter = chapter
	info.Verse = verse
	return &info
}

// UpdateCompanionWPM updates the guardian companion bot's speed dynamically as user improves
func (m *AngelManager) UpdateCompanionWPM(uid string) {
	m.mu.RLock()
	bot, ok := m.companions[uid]
	m.mu.RUnlock()
	if !ok || !bot.Info.IsGuardian {
		return
	}

	templates := m.GetAngelTemplatesForUser(uid)
	for _, t := range templates {
		if t.Rank == "guardian" {
			bot.mu.Lock()
			bot.Info.WPM = t.WPM
			bot.Info.Title = t.Title
			bot.Info.Name = t.Name
			bot.mu.Unlock()
			break
		}
	}
}

// DismissCompanion stops any companion angel active for a user
func (m *AngelManager) DismissCompanion(uid string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if bot, ok := m.companions[uid]; ok {
		bot.mu.Lock()
		if bot.IsRunning {
			bot.IsRunning = false
			close(bot.StopChan)
		}
		bot.mu.Unlock()
		delete(m.companions, uid)
	}
}

// GetActiveCompanion returns current active companion info for a user or nil
func (m *AngelManager) GetActiveCompanion(uid string) *AngelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bot, ok := m.companions[uid]
	if !ok {
		return nil
	}
	bot.mu.RLock()
	defer bot.mu.RUnlock()
	info := bot.Info
	return &info
}

// UpdateCompanionLocation informs the companion of a new verse location
func (m *AngelManager) UpdateCompanionLocation(uid, trans, book string, chapter, verse int, text string) {
	m.mu.RLock()
	bot, ok := m.companions[uid]
	m.mu.RUnlock()

	if !ok {
		return
	}

	bot.mu.Lock()
	bot.Translation = trans
	bot.BookName = book
	bot.Chapter = chapter
	bot.Verse = verse
	bot.Text = cleanVerseText(text)
	bot.CharIndex = 0
	bot.mu.Unlock()
}

func (m *AngelManager) runCompanionLoop(uid string, bot *AngelBot) {
	for {
		bot.mu.RLock()
		running := bot.IsRunning
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

			// Angels should only move when the user has the site open!
			if m.hub != nil && !m.hub.IsUserConnected(uid) {
				time.Sleep(500 * time.Millisecond)
				continue
			}

			bot.mu.Lock()
			if !bot.IsRunning {
				bot.mu.Unlock()
				return
			}
			bot.CharIndex = i
			bot.mu.Unlock()

			// Broadcast ghost cursor
			if m.hub != nil {
				m.hub.BroadcastAngelCursor(trans, book, ch, v, GhostCursor{
					UID:         bot.Info.ID,
					DisplayName: bot.Info.Icon + " " + bot.Info.Name,
					AvatarColor: bot.Info.Color,
					CharIndex:   i,
					WPM:         wpm,
					IsAngel:     true,
					AngelRank:   bot.Info.Rank,
				})
			}

			time.Sleep(charDelay)
		}

		// When companion completes the passage:
		// Record verse completion
		if m.store != nil {
			_, _, _ = m.store.RecordVerseCompletion(context.Background(), bot.Info.ID, trans, book, ch, v)
		}

		// If Archangel Michael completed this verse and a wild demon is lurking here, Michael slays it!
		if bot.Info.Rank == "michael" && globalDemonManager != nil {
			globalDemonManager.SlayWildDemonByMichael(trans, book, ch, v)
		}

		// Notify user that angel finished
		if m.hub != nil {
			m.hub.SendToUID(uid, Message{
				Type:    "angel_complete",
				Content: bot.Info.Icon + " " + bot.Info.Name + " has finished typing the verse at " + bot.Info.Title + " pace (" + fmt.Sprintf("%d", wpm) + " WPM)!",
				Angel:   &bot.Info,
			})
		}

		// Advance companion to next canonical verse across Scripture!
		if nextV, ok := getNextCanonicalVerse(trans, book, ch, v); ok {
			bot.mu.Lock()
			bot.BookName = nextV.BookName
			bot.Chapter = nextV.Chapter
			bot.Verse = nextV.Verse
			bot.Text = cleanVerseText(nextV.Text)
			bot.CharIndex = 0
			bot.mu.Unlock()

			if m.hub != nil {
				m.hub.SendToUID(uid, Message{
					Type:    "angel_advanced",
					Content: fmt.Sprintf("%s %s advanced to %s %d:%d!", bot.Info.Icon, bot.Info.Name, nextV.BookName, nextV.Chapter, nextV.Verse),
					Angel:   &bot.Info,
					Book:    nextV.BookName,
					Verse:   &nextV,
				})
			}
		}

		// Pause briefly before typing next verse
		select {
		case <-bot.StopChan:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

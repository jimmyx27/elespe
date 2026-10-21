package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// DemonSyncBalanceConfig holds tunable parameters for word-in-sync combat stack scaling.
// Kept centralized here for future balancing adjustments.
type DemonSyncBalanceConfig struct {
	StacksPerWord       int     `json:"stacks_per_word"`       // Stacks gained per word typed in sync (default: 1)
	StacksPerDamageTier int     `json:"stacks_per_damage_tier"` // Number of stacks required to increase damage (default: 5)
	FlatDamagePerTier   int     `json:"flat_damage_per_tier"`   // Flat bonus damage per tier (every 5 stacks) (default: 1)
	DamageMultPerTier   float64 `json:"damage_mult_per_tier"`   // Multiplicative bonus per tier (e.g. 0.25 = +25% per 5 stacks)
	MaxStacks           int     `json:"max_stacks"`             // Optional cap on stacks (0 = uncapped)
	CharSyncTolerance   int     `json:"char_sync_tolerance"`    // Max character distance from demon to count as "in sync" (default: 2)
}

// DefaultDemonSyncBalance defines the active balance values for demon sync stacks.
var DefaultDemonSyncBalance = DemonSyncBalanceConfig{
	StacksPerWord:       1,
	StacksPerDamageTier: 5,
	FlatDamagePerTier:   1,
	DamageMultPerTier:   0.25,
	MaxStacks:           0,
	CharSyncTolerance:   2,
}

// GetSyncDamageTier returns the number of 5-stack damage tiers achieved for a given stack count.
func GetSyncDamageTier(syncStacks int) int {
	return GetSyncDamageTierWithConfig(syncStacks, DefaultDemonSyncBalance)
}

// GetSyncDamageTierWithConfig returns the damage tier count using a custom balance config.
func GetSyncDamageTierWithConfig(syncStacks int, cfg DemonSyncBalanceConfig) int {
	if syncStacks < 0 {
		return 0
	}
	if cfg.MaxStacks > 0 && syncStacks > cfg.MaxStacks {
		syncStacks = cfg.MaxStacks
	}
	step := cfg.StacksPerDamageTier
	if step <= 0 {
		step = 5
	}
	return syncStacks / step
}

// CalculateSyncStackDamage computes the total strike damage given a base damage and current sync stacks.
// Every 5 stacks (StacksPerDamageTier) increases the damage done to the demon.
func CalculateSyncStackDamage(baseDamage, syncStacks int) int {
	return CalculateSyncStackDamageWithConfig(baseDamage, syncStacks, DefaultDemonSyncBalance)
}

// CalculateSyncStackDamageWithConfig computes sync-scaled damage with a custom balance configuration.
func CalculateSyncStackDamageWithConfig(baseDamage, syncStacks int, cfg DemonSyncBalanceConfig) int {
	if baseDamage < 1 {
		baseDamage = 1
	}
	tiers := GetSyncDamageTierWithConfig(syncStacks, cfg)
	if tiers <= 0 {
		return baseDamage
	}
	multDamage := int(math.Round(float64(baseDamage) * (1.0 + float64(tiers)*cfg.DamageMultPerTier)))
	flatDamage := baseDamage + tiers*cfg.FlatDamagePerTier
	if multDamage > flatDamage {
		return multDamage
	}
	return flatDamage
}

// Predefined Demon Adversary Profiles with Levels
var demonTemplates = map[string]DemonInfo{
	"imp": {
		ID:            "demon-imp",
		Rank:          "imp",
		Name:          "Mischievous Imp",
		Title:         "Shadow Whisperer",
		Description:   "A sly imp that casts billowing smoke clouds over holy words for 2.5 seconds to distract your focus.",
		Level:         1,
		MaxLevel:      5,
		WPM:           15,
		Color:         "#ef4444",
		Icon:          "😈",
		BlockDuration: 2.5,
	},
	"fiend": {
		ID:            "demon-fiend",
		Rank:          "fiend",
		Name:          "Shadow Fiend",
		Title:         "Stumbling Stone",
		Description:   "A looming shadow demon that shrouds scripture in dark fog and casts demonic typo curses to test your spiritual resilience.",
		Level:         1,
		MaxLevel:      4,
		WPM:           35,
		Color:         "#dc2626",
		Icon:          "👹",
		BlockDuration: 3.0,
	},
	"tormentor": {
		ID:            "demon-tormentor",
		Rank:          "tormentor",
		Name:          "Brimstone Tormentor",
		Title:         "Accuser of the Brethren",
		Description:   "Fierce fallen herald armed with brimstone smog. Obscures entire phrases and attempts to swap letters into demonic typos.",
		Level:         1,
		MaxLevel:      4,
		WPM:           70,
		Color:         "#b91c1c",
		Icon:          "👺",
		BlockDuration: 3.5,
	},
	"apollyon": {
		ID:            "demon-apollyon",
		Rank:          "apollyon",
		Name:          "Apollyon the Destroyer",
		Title:         "Angel of the Bottomless Pit",
		Description:   "The infernal sovereign of Abaddon (Rev 9:11). Casts abyssal darkness across verses, operating 24/7 without rest.",
		Level:         2,
		MaxLevel:      4,
		WPM:           160,
		Color:         "#991b1b",
		Icon:          "👑🔥",
		BlockDuration: 4.0,
		IsBoss:        true,
		TotalHP:       6000,
		CurrentHP:     6000,
	},
}

// Ordered list of demon ranks for UI display
var canonicalDemons = []string{"imp", "fiend", "tormentor", "apollyon"}

// GetDemonWPM calculates the exact speed for a demon rank and level
func GetDemonWPM(rank string, level int) int {
	if level < 1 {
		level = 1
	}
	switch rank {
	case "imp":
		if level > 5 {
			level = 5
		}
		return 15 + (level-1)*5 // Lv.1: 15, Lv.2: 20, Lv.3: 25, Lv.4: 30, Lv.5: 35
	case "fiend":
		if level > 4 {
			level = 4
		}
		return 35 + (level-1)*10 // Lv.1: 35, Lv.2: 45, Lv.3: 55, Lv.4: 65
	case "tormentor":
		if level > 4 {
			level = 4
		}
		return 70 + (level-1)*15 // Lv.1: 70, Lv.2: 85, Lv.3: 100, Lv.4: 115
	case "apollyon":
		if level > 4 {
			level = 4
		}
		return 130 + (level-1)*30 // Lv.1: 130, Lv.2: 160, Lv.3: 190, Lv.4: 220
	default:
		return 20
	}
}

// DemonBot represents an active typing instance of a demon adversary
type DemonBot struct {
	Info        DemonInfo
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

// DemonManager orchestrates the 24/7 autonomous devil, wild roaming demons, and adversary mechanics
type DemonManager struct {
	mu                   sync.RWMutex
	store                Store
	hub                  *Hub
	activeDemons         map[string]*DemonBot // uid -> adversary bot
	devil24_7            *DemonBot
	devil24_7VersesCount int
	cancel24_7           chan struct{}
	wildDemons           []*WildDemon
	wildDemonBases       map[string]int
	slainDemons          map[string]*WildDemon
	spawnInterval        time.Duration
	maxWildDemons        int
	evolveInterval       time.Duration
	lastDailySwarmDate   string
	lastSundaySwarmDate  string
	stopSpawnChan        chan struct{}
}

var globalDemonManager *DemonManager

func newDemonManager(s Store, h *Hub) *DemonManager {
	dm := &DemonManager{
		store:                s,
		hub:                  h,
		activeDemons:         make(map[string]*DemonBot),
		devil24_7VersesCount: 1420, // Initial atmospheric seed of 24/7 verses challenged
		cancel24_7:           make(chan struct{}),
		wildDemons:           initWildDemons(),
		wildDemonBases:       make(map[string]int),
		slainDemons:          make(map[string]*WildDemon),
		spawnInterval:        150 * time.Second, // 2.5 minutes baseline, dynamically adjusted by inverse pressure
		maxWildDemons:        666,               // Max world cap of 666 demons!
		evolveInterval:       15 * time.Minute,  // Demons grow in level over time
		stopSpawnChan:        make(chan struct{}),
	}
	for _, wd := range dm.wildDemons {
		dm.wildDemonBases[wd.ID] = wd.WPM
	}
	for _, wd := range dm.wildDemons {
		go dm.runWildDemonLoop(wd)
	}
	go dm.runPeriodicSpawnLoop()
	return dm
}

// GetDemonTemplates returns the list of all available demons with level options
func (m *DemonManager) GetDemonTemplates() []DemonInfo {
	list := make([]DemonInfo, 0, len(canonicalDemons))
	for _, rank := range canonicalDemons {
		if t, ok := demonTemplates[rank]; ok {
			inf := t
			inf.WPM = GetDemonWPM(rank, inf.Level)
			list = append(list, inf)
		}
	}
	return list
}

// GetDevil24_7Status returns the current live statistics of the 24/7 devil
func (m *DemonManager) GetDevil24_7Status() (versesCount int, loc string, wpm int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	versesCount = m.devil24_7VersesCount
	wpm = 160
	loc = "Roaming the Scriptures 24/7"

	if m.devil24_7 != nil {
		m.devil24_7.mu.RLock()
		if m.devil24_7.BookName != "" {
			loc = fmt.Sprintf("%s %d:%d", m.devil24_7.BookName, m.devil24_7.Chapter, m.devil24_7.Verse)
		}
		wpm = m.devil24_7.Info.WPM
		m.devil24_7.mu.RUnlock()
	}
	return versesCount, loc, wpm
}

// StartDevil24_7 runs the autonomous 24/7 devil loop that NEVER sleeps
func (m *DemonManager) StartDevil24_7(defaultRank string, level int) {
	template, ok := demonTemplates[defaultRank]
	if !ok {
		template = demonTemplates["imp"]
	}
	template.Level = level
	template.WPM = GetDemonWPM(template.Rank, level)

	bot := &DemonBot{
		Info:        template,
		Translation: "kjv",
		BookName:    "Genesis",
		Chapter:     3,
		Verse:       1,
		StopChan:    make(chan struct{}),
		IsRunning:   true,
	}

	m.mu.Lock()
	m.devil24_7 = bot
	m.mu.Unlock()

	go m.runDevil24_7Loop(bot)
}

func (m *DemonManager) runDevil24_7Loop(bot *DemonBot) {
	// 24/7 scriptural incursion playlist
	playlist := []struct {
		trans string
		book  string
		ch    int
		v     int
	}{
		{"kjv", "Genesis", 3, 1},
		{"kjv", "Genesis", 3, 4},
		{"kjv", "Job", 1, 7},
		{"kjv", "Job", 2, 2},
		{"kjv", "Matthew", 4, 1},
		{"kjv", "Matthew", 4, 3},
		{"kjv", "Luke", 10, 18},
		{"kjv", "1 Peter", 5, 8},
		{"kjv", "Revelation", 9, 11},
		{"kjv", "Revelation", 12, 9},
		{"kjv", "Revelation", 20, 10},
	}

	playIdx := 0

	for {
		item := playlist[playIdx%len(playlist)]
		playIdx++

		text := ""
		td, ok := translations[item.trans]
		if ok {
			vList := td.VersesByBook[item.book]
			for _, v := range vList {
				if v.Chapter == item.ch && v.Verse == item.v {
					text = cleanVerseText(v.Text)
					break
				}
			}
		}
		if text == "" {
			text = "Now the serpent was more subtil than any beast of the field which the LORD God had made."
		}

		bot.mu.Lock()
		bot.Translation = item.trans
		bot.BookName = item.book
		bot.Chapter = item.ch
		bot.Verse = item.v
		bot.Text = text
		bot.CharIndex = 0
		wpm := bot.Info.WPM
		bot.mu.Unlock()

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

			bot.mu.Lock()
			bot.CharIndex = i
			bot.mu.Unlock()

			// Broadcast 24/7 devil cursor (devils operate whether someone is watching or not!)
			if m.hub != nil {
				// Demons trigger periodic shroud or typo effects
				var ability string
				shroudRange := 0
				if i > 0 && i%15 == 0 {
					ability = "shroud"
					shroudRange = 3
				}

				m.hub.BroadcastDemonCursor(item.trans, item.book, item.ch, item.v, GhostCursor{
					UID:         bot.Info.ID,
					DisplayName: bot.Info.Icon + " " + bot.Info.Name,
					AvatarColor: bot.Info.Color,
					CharIndex:   i,
					WPM:         wpm,
					IsDemon:     true,
					DemonRank:   bot.Info.Rank,
					DemonLevel:  bot.Info.Level,
					CastAbility: ability,
					ShroudRange: shroudRange,
				})
			}

			// Add small jitter
			jitter := time.Duration((rand.Float64()*0.3 - 0.15) * float64(charDelay))
			sleepDur := charDelay + jitter
			if sleepDur < 10*time.Millisecond {
				sleepDur = 10 * time.Millisecond
			}
			time.Sleep(sleepDur)
		}

		// Verse completed by 24/7 devil!
		m.mu.Lock()
		m.devil24_7VersesCount++
		m.mu.Unlock()

		// Brief pause between verses
		time.Sleep(2 * time.Second)
	}
}

// SummonDemon creates an active demon adversary to challenge the user on their verse
func (m *DemonManager) SummonDemon(uid, rank string, level int, trans, book string, chapter, verse int, text string) *DemonInfo {
	m.DismissDemon(uid)

	template, ok := demonTemplates[rank]
	if !ok {
		template = demonTemplates["imp"]
	}

	if level < 1 {
		level = 1
	}
	if level > template.MaxLevel {
		level = template.MaxLevel
	}

	template.Level = level
	template.WPM = GetDemonWPM(template.Rank, level)

	bot := &DemonBot{
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
	m.activeDemons[uid] = bot
	m.mu.Unlock()

	go m.runDemonCompanionLoop(uid, bot)

	info := template
	info.BookName = book
	info.Chapter = chapter
	info.Verse = verse
	return &info
}

func (m *DemonManager) runDemonCompanionLoop(uid string, bot *DemonBot) {
	for {
		bot.mu.RLock()
		running := bot.IsRunning
		wpm := bot.Info.WPM
		text := bot.Text
		trans := bot.Translation
		book := bot.BookName
		ch := bot.Chapter
		v := bot.Verse
		rank := bot.Info.Rank
		lvl := bot.Info.Level
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

			bot.mu.Lock()
			if !bot.IsRunning {
				bot.mu.Unlock()
				return
			}
			bot.CharIndex = i
			bot.mu.Unlock()

			// Demons cast abilities as they type:
			// Every 8-10 chars: Shroud (blocks upcoming text for 2.5-3.5s) or Typo Glitch
			var ability string
			shroudRange := 0
			if i > 0 && i%10 == 0 {
				if rand.Float64() < 0.6 {
					ability = "shroud"
					shroudRange = 2 + (lvl / 2) // Higher level demons shroud more chars
				} else {
					ability = "typo"
					shroudRange = 1
				}
			}

			if m.hub != nil {
				m.hub.BroadcastDemonCursor(trans, book, ch, v, GhostCursor{
					UID:         bot.Info.ID,
					DisplayName: bot.Info.Icon + " " + bot.Info.Name,
					AvatarColor: bot.Info.Color,
					CharIndex:   i,
					WPM:         wpm,
					IsDemon:     true,
					DemonRank:   rank,
					DemonLevel:  lvl,
					CastAbility: ability,
					ShroudRange: shroudRange,
				})
			}

			time.Sleep(charDelay)
		}

		// When demon completes the passage before the user:
		if m.hub != nil {
			m.hub.SendToUID(uid, Message{
				Type:    "demon_complete",
				Content: fmt.Sprintf("%s %s (Lv. %d) outpaced you! Cleanse the holy scripture to banish the adversary!", bot.Info.Icon, bot.Info.Name, lvl),
				Demon:   &bot.Info,
			})
		}

		time.Sleep(3 * time.Second)
	}
}

// DismissDemon stops any active adversary demon for a user
func (m *DemonManager) DismissDemon(uid string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if bot, ok := m.activeDemons[uid]; ok {
		bot.mu.Lock()
		if bot.IsRunning {
			bot.IsRunning = false
			close(bot.StopChan)
		}
		bot.mu.Unlock()
		delete(m.activeDemons, uid)
	}
}

// GetActiveDemon returns current active adversary demon info for a user or nil
func (m *DemonManager) GetActiveDemon(uid string) *DemonInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bot, ok := m.activeDemons[uid]
	if !ok {
		return nil
	}
	bot.mu.RLock()
	defer bot.mu.RUnlock()
	info := bot.Info
	info.BookName = bot.BookName
	info.Chapter = bot.Chapter
	info.Verse = bot.Verse
	info.CharIndex = bot.CharIndex
	return &info
}

// UpdateDemonLocation informs the adversary of a new verse location
func (m *DemonManager) UpdateDemonLocation(uid, trans, book string, chapter, verse int, text string) {
	m.mu.RLock()
	bot, ok := m.activeDemons[uid]
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

// ---- Wild Roaming Demons Implementation ----

func initWildDemons() []*WildDemon {
	demons := []*WildDemon{
		{
			ID:          "wild-imp-1",
			Rank:        "imp",
			Level:       1,
			Name:        "Lilith's Whispering Imp",
			Title:       "Tempter of Eden",
			Icon:        "😈",
			Color:       "#ef4444",
			Translation: "kjv",
			BookName:    "Genesis",
			Chapter:     3,
			Verse:       1,
			Text:        "Now the serpent was more subtil than any beast of the field which the LORD God had made.",
			WPM:         12,
			MaxHP:       120,
			CurrentHP:   120,
			EnrageCount: 0,
			CharIndex:   0,
		},
		{
			ID:          "wild-fiend-1",
			Rank:        "fiend",
			Level:       1,
			Name:        "Brimstone Stalker",
			Title:       "Wanderer in the Earth",
			Icon:        "👹",
			Color:       "#dc2626",
			Translation: "kjv",
			BookName:    "Job",
			Chapter:     1,
			Verse:       7,
			Text:        "And the LORD said unto Satan, Whence comest thou? Then Satan answered the LORD, and said, From going to and fro in the earth, and from walking up and down in it.",
			WPM:         14,
			MaxHP:       180,
			CurrentHP:   180,
			EnrageCount: 0,
			CharIndex:   0,
		},
		{
			ID:          "wild-fiend-2",
			Rank:        "fiend",
			Level:       2,
			Name:        "Deceiver of the Wilderness",
			Title:       "Stumbling Stone",
			Icon:        "👹",
			Color:       "#dc2626",
			Translation: "kjv",
			BookName:    "Matthew",
			Chapter:     4,
			Verse:       1,
			Text:        "Then was Jesus led up of the Spirit into the wilderness to be tempted of the devil.",
			WPM:         16,
			MaxHP:       240,
			CurrentHP:   240,
			EnrageCount: 0,
			CharIndex:   0,
		},
		{
			ID:          "wild-tormentor-1",
			Rank:        "tormentor",
			Level:       1,
			Name:        "Lightning of the Pit",
			Title:       "Fallen Star",
			Icon:        "👺",
			Color:       "#b91c1c",
			Translation: "kjv",
			BookName:    "Luke",
			Chapter:     10,
			Verse:       18,
			Text:        "And he said unto them, I beheld Satan as lightning fall from heaven.",
			WPM:         18,
			MaxHP:       300,
			CurrentHP:   300,
			EnrageCount: 0,
			CharIndex:   0,
		},
		{
			ID:          "wild-tormentor-2",
			Rank:        "tormentor",
			Level:       2,
			Name:        "Accuser of the Brethren",
			Title:       "Ancient Serpent",
			Icon:        "👺",
			Color:       "#b91c1c",
			Translation: "kjv",
			BookName:    "Revelation",
			Chapter:     12,
			Verse:       9,
			Text:        "And the great dragon was cast out, that old serpent, called the Devil, and Satan, which deceiveth the whole world: he was cast out into the earth, and his angels were cast out with him.",
			WPM:         20,
			MaxHP:       360,
			CurrentHP:   360,
			EnrageCount: 0,
			CharIndex:   0,
		},
		{
			ID:          "wild-imp-2",
			Rank:        "imp",
			Level:       2,
			Name:        "Roaring Shadow",
			Title:       "Prowling Adversary",
			Icon:        "😈",
			Color:       "#ef4444",
			Translation: "kjv",
			BookName:    "1 Peter",
			Chapter:     5,
			Verse:       8,
			Text:        "Be sober, be vigilant; because your adversary the devil, as a roaring lion, walketh about, seeking whom he may devour:",
			WPM:         15,
			MaxHP:       150,
			CurrentHP:   150,
			EnrageCount: 0,
			CharIndex:   0,
		},
		{
			ID:          "wild-tormentor-3",
			Rank:        "tormentor",
			Level:       3,
			Name:        "Shadow of Apollyon",
			Title:       "Prince of Locusts",
			Icon:        "👺",
			Color:       "#991b1b",
			Translation: "kjv",
			BookName:    "Revelation",
			Chapter:     9,
			Verse:       11,
			Text:        "And they had a king over them, which is the angel of the bottomless pit, whose name in the Hebrew tongue is Abaddon, but in the Greek tongue hath his name Apollyon.",
			WPM:         24,
			MaxHP:       450,
			CurrentHP:   450,
			EnrageCount: 0,
			CharIndex:   0,
		},
	}
	now := time.Now().UTC()
	for _, d := range demons {
		d.SpawnedAt = now.Add(-10 * time.Minute)
		d.LastEvolve = d.SpawnedAt
		d.MaxLevel = 5
	}
	return demons
}

func (m *DemonManager) runWildDemonLoop(d *WildDemon) {
	for {
		m.mu.RLock()
		wpm := d.WPM
		if wpm < 10 {
			wpm = 10
		}
		text := d.Text
		trans := d.Translation
		book := d.BookName
		ch := d.Chapter
		v := d.Verse
		hp := d.CurrentHP
		m.mu.RUnlock()

		if hp <= 0 {
			return
		}
		if text == "" {
			time.Sleep(1 * time.Second)
			continue
		}

		textRunes := []rune(text)
		totalChars := len(textRunes)

		// Optimization for 666 demons: if no human players are on this verse, advance in background ticks
		if m.hub != nil && !m.hub.HasClientsOnVerse(trans, book, ch, v) {
			charsPerTick := int(float64(wpm*5) * (2.0 / 60.0))
			if charsPerTick < 2 {
				charsPerTick = 2
			}
			time.Sleep(2 * time.Second)

			m.mu.Lock()
			if d.CurrentHP <= 0 {
				m.mu.Unlock()
				return
			}
			d.CharIndex += charsPerTick
			reachedEnd := d.CharIndex >= totalChars
			m.mu.Unlock()

			if reachedEnd {
				m.AdvanceWildDemonToNextVerse(d.ID)
				time.Sleep(1 * time.Second)
			}
			continue
		}

		charDelay := time.Duration(float64(time.Minute) / float64(wpm*5))
		if charDelay < 50*time.Millisecond {
			charDelay = 50 * time.Millisecond
		}

		completed := true
		for i := 0; i <= totalChars; i++ {
			m.mu.Lock()
			if d.CurrentHP <= 0 {
				m.mu.Unlock()
				completed = false
				break
			}
			d.CharIndex = i
			curWpm := d.WPM
			curHP := d.CurrentHP
			maxHP := d.MaxHP
			m.mu.Unlock()

			if m.hub != nil {
				m.hub.BroadcastDemonCursor(trans, book, ch, v, GhostCursor{
					UID:         d.ID,
					DisplayName: d.Icon + " " + d.Name,
					AvatarColor: d.Color,
					CharIndex:   i,
					WPM:         curWpm,
					IsDemon:     true,
					DemonRank:   d.Rank,
					DemonLevel:  d.Level,
					CurrentHP:   curHP,
					MaxHP:       maxHP,
				})
			}

			time.Sleep(charDelay)
		}

		if completed {
			m.AdvanceWildDemonToNextVerse(d.ID)
		}

		time.Sleep(3 * time.Second)
	}
}

// AdvanceWildDemonToNextVerse moves a wild demon that completed typing its current verse onto the next canonical verse
func (m *DemonManager) AdvanceWildDemonToNextVerse(demonID string) (*WildDemon, Verse, bool) {
	m.mu.Lock()
	var target *WildDemon
	for _, d := range m.wildDemons {
		if d.ID == demonID && d.CurrentHP > 0 {
			target = d
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return nil, Verse{}, false
	}

	oldTrans := target.Translation
	oldBook := target.BookName
	oldCh := target.Chapter
	oldV := target.Verse

	nextV, ok := getNextCanonicalVerse(oldTrans, oldBook, oldCh, oldV)
	if !ok {
		target.CharIndex = 0
		res := *target
		m.mu.Unlock()
		return &res, Verse{}, false
	}

	// Step past occupied verses if needed (up to 10 steps)
	for step := 0; step < 10 && m.isVerseOccupiedLocked(nextV.BookName, nextV.Chapter, nextV.Verse); step++ {
		if cand, ok2 := getNextCanonicalVerse(oldTrans, nextV.BookName, nextV.Chapter, nextV.Verse); ok2 {
			nextV = cand
		} else {
			break
		}
	}

	target.BookName = nextV.BookName
	target.Chapter = nextV.Chapter
	target.Verse = nextV.Verse
	target.Text = cleanVerseText(nextV.Text)
	target.CharIndex = 0
	demonCopy := *target
	allDemons := m.getWildDemonsLocked()
	m.mu.Unlock()

	if m.hub != nil {
		m.hub.BroadcastDemonEscaped(oldTrans, oldBook, oldCh, oldV, demonCopy, nextV)
		m.hub.BroadcastWildDemonsList(allDemons)
	}
	return &demonCopy, nextV, true
}

// SlayWildDemonByMichael purges a wild demon when Big Mike completes or sanctifies its verse
func (m *DemonManager) SlayWildDemonByMichael(trans, book string, chapter, verse int) *WildDemon {
	m.mu.Lock()
	var slain *WildDemon
	for idx, d := range m.wildDemons {
		if d.BookName == book && d.Chapter == chapter && d.Verse == verse {
			d.CurrentHP = 0
			cp := *d
			slain = &cp
			m.wildDemons = append(m.wildDemons[:idx], m.wildDemons[idx+1:]...)
			m.slainDemons[d.ID] = d
			break
		}
	}
	var allDemons []WildDemon
	if slain != nil {
		allDemons = m.getWildDemonsLocked()
	}
	m.mu.Unlock()

	if slain != nil && m.hub != nil {
		content := fmt.Sprintf("⚔️ Big Mike sanctified %s %d:%d and slew %s %s (Lv. %d)!", book, chapter, verse, slain.Icon, slain.Name, slain.Level)
		m.hub.BroadcastMichaelSlay(trans, book, chapter, verse, *slain, content)
		m.hub.BroadcastWildDemonsList(allDemons)
	}
	return slain
}

func (m *DemonManager) GetWildDemons() []WildDemon {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]WildDemon, len(m.wildDemons))
	for i, d := range m.wildDemons {
		res[i] = *d
	}
	return res
}

func (m *DemonManager) GetWildDemonAt(trans, book string, chapter, verse int) *WildDemon {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, d := range m.wildDemons {
		if d.Translation == trans && d.BookName == book && d.Chapter == chapter && d.Verse == verse {
			res := *d
			return &res
		}
	}
	return nil
}

func (m *DemonManager) GetWildDemon(id string) *WildDemon {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, d := range m.wildDemons {
		if d.ID == id {
			res := *d
			return &res
		}
	}
	return nil
}

func (m *DemonManager) ApplyWildDemonDamage(demonID string, damage int) (*WildDemon, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for idx, d := range m.wildDemons {
		if d.ID == demonID {
			if d.CurrentHP > 0 {
				d.CurrentHP -= damage
				if d.CurrentHP <= 0 {
					d.CurrentHP = 0
					res := *d
					// The demon lived until defeated! Slay and remove from active wild demons
					m.wildDemons = append(m.wildDemons[:idx], m.wildDemons[idx+1:]...)
					m.slainDemons[d.ID] = d
					return &res, true
				}
			}
			res := *d
			return &res, false
		}
	}
	return nil, false
}

func (m *DemonManager) EnrageWildDemonOnMistake(demonID string) *WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, d := range m.wildDemons {
		if d.ID == demonID {
			heal := int(float64(d.MaxHP) * 0.20)
			if heal < 10 {
				heal = 10
			}
			d.CurrentHP += heal
			if d.CurrentHP > d.MaxHP {
				d.CurrentHP = d.MaxHP
			}
			d.WPM += 2
			d.EnrageCount++
			res := *d
			return &res
		}
	}
	return nil
}

func (m *DemonManager) RespawnWildDemon(demonID string) *WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, d := range m.wildDemons {
		if d.ID == demonID {
			d.CurrentHP = d.MaxHP
			d.EnrageCount = 0
			d.CharIndex = 0
			if base, ok := m.wildDemonBases[d.ID]; ok {
				d.WPM = base
			}
			shiftWildDemonLocation(d)
			res := *d
			return &res
		}
	}

	if d, ok := m.slainDemons[demonID]; ok {
		delete(m.slainDemons, demonID)
		d.CurrentHP = d.MaxHP
		d.EnrageCount = 0
		d.CharIndex = 0
		if base, ok := m.wildDemonBases[d.ID]; ok {
			d.WPM = base
		}
		shiftWildDemonLocation(d)
		m.wildDemons = append(m.wildDemons, d)
		go m.runWildDemonLoop(d)
		res := *d
		return &res
	}

	return nil
}

func shiftWildDemonLocation(d *WildDemon) {
	type loc struct {
		book string
		ch   int
		v    int
		text string
	}
	roamMap := map[string][]loc{
		"wild-imp-1": {
			{"Genesis", 3, 1, "Now the serpent was more subtil than any beast of the field which the LORD God had made."},
			{"Genesis", 3, 4, "And the serpent said unto the woman, Ye shall not surely die:"},
			{"Proverbs", 16, 18, "Pride goeth before destruction, and an haughty spirit before a fall."},
		},
		"wild-fiend-1": {
			{"Job", 1, 7, "And the LORD said unto Satan, Whence comest thou? Then Satan answered the LORD, and said, From going to and fro in the earth, and from walking up and down in it."},
			{"Job", 2, 2, "And the LORD said unto Satan, From whence comest thou? And Satan answered the LORD, and said, From going to and fro in the earth, and from walking up and down in it."},
			{"Zechariah", 3, 1, "And he shewed me Joshua the high priest standing before the angel of the LORD, and Satan standing at his right hand to resist him."},
		},
		"wild-fiend-2": {
			{"Matthew", 4, 1, "Then was Jesus led up of the Spirit into the wilderness to be tempted of the devil."},
			{"Matthew", 4, 3, "And when the tempter came to him, he said, If thou be the Son of God, command that these stones be made bread."},
			{"Luke", 4, 2, "Being forty days tempted of the devil. And in those days he did eat nothing: and when they were ended, he afterward hungered."},
		},
		"wild-tormentor-1": {
			{"Luke", 10, 18, "And he said unto them, I beheld Satan as lightning fall from heaven."},
			{"Ephesians", 6, 11, "Put on the whole armour of God, that ye may be able to stand against the wiles of the devil."},
			{"James", 4, 7, "Submit yourselves therefore to God. Resist the devil, and he will flee from you."},
		},
		"wild-tormentor-2": {
			{"Revelation", 12, 9, "And the great dragon was cast out, that old serpent, called the Devil, and Satan, which deceiveth the whole world: he was cast out into the earth, and his angels were cast out with him."},
			{"Revelation", 12, 7, "And there was war in heaven: Michael and his angels fought against the dragon; and the dragon fought and his angels,"},
			{"Revelation", 20, 2, "And he laid hold on the dragon, that old serpent, which is the Devil, and Satan, and bound him a thousand years,"},
		},
		"wild-imp-2": {
			{"1 Peter", 5, 8, "Be sober, be vigilant; because your adversary the devil, as a roaring lion, walketh about, seeking whom he may devour:"},
			{"2 Corinthians", 11, 14, "And no marvel; for Satan himself is transformed into an angel of light."},
			{"Genesis", 3, 5, "For God doth know that in the day ye eat thereof, then your eyes shall be opened, and ye shall be as gods, knowing good and evil."},
		},
		"wild-tormentor-3": {
			{"Revelation", 9, 11, "And they had a king over them, which is the angel of the bottomless pit, whose name in the Hebrew tongue is Abaddon, but in the Greek tongue hath his name Apollyon."},
			{"Revelation", 19, 20, "And the beast was taken, and with him the false prophet that wrought miracles before him..."},
			{"Revelation", 20, 10, "And the devil that deceived them was cast into the lake of fire and brimstone..."},
		},
	}

	locs, ok := roamMap[d.ID]
	if !ok || len(locs) == 0 {
		return
	}
	next := locs[rand.Intn(len(locs))]
	d.BookName = next.book
	d.Chapter = next.ch
	d.Verse = next.v
	d.Text = cleanVerseText(next.text)
	if td, ok := translations[d.Translation]; ok {
		if vList, ok := td.VersesByBook[d.BookName]; ok {
			for _, v := range vList {
				if v.Chapter == d.Chapter && v.Verse == d.Verse {
					d.Text = cleanVerseText(v.Text)
					break
				}
			}
		}
	}
}

// ---- Periodic Spawning & Book Progress Encounter System ----

// Notable warfare / spiritual conflict scripture passages for atmospheric periodic spawns
var atmosphericDemonicPassages = []struct {
	Book    string
	Chapter int
	Verse   int
	Rank    string
	Level   int
	Name    string
	Title   string
	Icon    string
	Color   string
}{
	{"Genesis", 3, 1, "imp", 1, "Lilith's Whispering Imp", "Tempter of Eden", "😈", "#ef4444"},
	{"Genesis", 3, 4, "imp", 1, "Deceiver of the Garden", "Serpent's Whisper", "😈", "#ef4444"},
	{"Job", 1, 7, "fiend", 1, "Brimstone Stalker", "Wanderer in the Earth", "👹", "#dc2626"},
	{"Job", 2, 2, "fiend", 2, "Earth Roamer", "Stumbling Stone", "👹", "#dc2626"},
	{"Matthew", 4, 1, "fiend", 2, "Deceiver of the Wilderness", "Wilderness Tempter", "👹", "#dc2626"},
	{"Luke", 10, 18, "tormentor", 1, "Lightning of the Pit", "Fallen Star", "👺", "#b91c1c"},
	{"Revelation", 12, 9, "tormentor", 2, "Accuser of the Brethren", "Ancient Dragon", "👺", "#b91c1c"},
	{"1 Peter", 5, 8, "imp", 2, "Roaring Shadow", "Prowling Adversary", "😈", "#ef4444"},
	{"Revelation", 9, 11, "tormentor", 3, "Shadow of Apollyon", "Prince of Locusts", "👺", "#991b1b"},
	{"Ephesians", 6, 12, "tormentor", 2, "Ruler of Darkness", "Spiritual Wickedness", "👺", "#b91c1c"},
	{"2 Corinthians", 11, 14, "fiend", 2, "Counterfeit Radiance", "False Angel of Light", "👹", "#dc2626"},
	{"Zechariah", 3, 1, "tormentor", 2, "Satanic Accuser", "Resister of Joshua", "👺", "#b91c1c"},
	{"James", 4, 7, "imp", 2, "Fleeing Tempter", "Resisted Devil", "😈", "#ef4444"},
	{"Revelation", 20, 2, "apollyon", 3, "Abyssal Drake", "Bound Deceiver", "👑🔥", "#991b1b"},
	{"Mark", 5, 9, "tormentor", 3, "Legion of the Tombs", "Multitude of Chains", "👺", "#b91c1c"},
	{"Isaiah", 14, 12, "tormentor", 2, "Morning Star Cast Down", "Weakener of Nations", "👺", "#b91c1c"},
	{"Proverbs", 16, 18, "imp", 1, "Imp of Pride", "Haughty Spirit", "😈", "#ef4444"},
}

func (m *DemonManager) SetSpawnInterval(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawnInterval = d
}

func (m *DemonManager) SetMaxWildDemons(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxWildDemons = n
}

func (m *DemonManager) SetEvolveInterval(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evolveInterval = d
}

func (m *DemonManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stopSpawnChan:
	default:
		close(m.stopSpawnChan)
	}
}

// GetDynamicSpawnInterval calculates spawn rate based on Inverse Pressure at extreme limits:
// - Extreme Low (< 66 demons, < 10% of 666): Abyss retaliates fiercely, spawning every 45s
// - Moderate Low (66 - 200 demons): rapid incursion, spawning every 90s
// - Balanced Standard (200 - 500 demons): balanced pulse, spawning every 150s (2.5 mins)
// - High Incursion (500 - 600 demons): scripture resistance slows spawning to 240s (4 mins)
// - Extreme Limit (> 600 demons, > 90% of 666): overcrowding resistance slows spawning to 360s (6 mins)
func (m *DemonManager) GetDynamicSpawnInterval() time.Duration {
	m.mu.RLock()
	count := len(m.wildDemons)
	m.mu.RUnlock()

	if count < 66 {
		return 45 * time.Second
	} else if count < 200 {
		return 90 * time.Second
	} else if count < 500 {
		return 150 * time.Second
	} else if count < 600 {
		return 240 * time.Second
	}
	return 360 * time.Second
}

func (m *DemonManager) runPeriodicSpawnLoop() {
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()

	lastSpawn := time.Now().UTC()
	lastEvolve := time.Now().UTC()

	for {
		select {
		case <-m.stopSpawnChan:
			return
		case now := <-heartbeat.C:
			utcNow := now.UTC()
			todayStr := utcNow.Format("2006-01-02")

			// 1. Swarm Schedule Checks: Sunday Major Swarm & Daily Mini Swarm
			m.mu.Lock()
			isSunday := utcNow.Weekday() == time.Sunday
			needsSundaySwarm := isSunday && m.lastSundaySwarmDate != todayStr
			needsDailySwarm := !isSunday && m.lastDailySwarmDate != todayStr
			if needsSundaySwarm {
				m.lastSundaySwarmDate = todayStr
			}
			if needsDailySwarm {
				m.lastDailySwarmDate = todayStr
			}
			m.mu.Unlock()

			if needsSundaySwarm {
				m.TriggerMajorSwarm()
			} else if needsDailySwarm {
				m.TriggerMiniSwarm("")
			}

			// 2. Demon Aging / Evolution Check (demons increase in level the longer they live)
			if utcNow.Sub(lastEvolve) >= 1*time.Minute {
				lastEvolve = utcNow
				evolved := m.EvolveLivingDemons()
				if len(evolved) > 0 && m.hub != nil {
					m.hub.BroadcastWildDemonsList(m.GetWildDemons())
				}
			}

			// 3. Dynamic Inverse Pressure Interval Spawning
			dynInterval := m.GetDynamicSpawnInterval()
			if utcNow.Sub(lastSpawn) >= dynInterval {
				lastSpawn = utcNow
				m.mu.Lock()
				count := len(m.wildDemons)
				m.mu.Unlock()

				if count < m.maxWildDemons {
					newDemon := m.SpawnPeriodicWildDemon()
					if newDemon != nil && m.hub != nil {
						m.hub.BroadcastWildDemonsList(m.GetWildDemons())
					}
				}
			}
		}
	}
}

// EvolveLivingDemons levels up demons that have survived uncontested over time
func (m *DemonManager) EvolveLivingDemons() []WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	var evolved []WildDemon
	now := time.Now().UTC()
	for _, d := range m.wildDemons {
		if d.CurrentHP <= 0 {
			continue
		}
		if d.SpawnedAt.IsZero() {
			d.SpawnedAt = now.Add(-10 * time.Minute)
			d.LastEvolve = d.SpawnedAt
		}
		d.AgeMinutes = int(now.Sub(d.SpawnedAt).Minutes())

		if d.MaxLevel <= 0 {
			d.MaxLevel = 5
		}

		if d.Level < d.MaxLevel && now.Sub(d.LastEvolve) >= m.evolveInterval {
			d.Level++
			d.LastEvolve = now
			d.MaxHP += 50
			d.CurrentHP += 50
			if d.WPM < 24 {
				d.WPM++
			}
			// Update title to reflect ancient growing power
			prefix := ""
			switch d.Level {
			case 2:
				prefix = "Awakened "
			case 3:
				prefix = "Ancient "
			case 4:
				prefix = "Festering "
			case 5:
				prefix = "Dread "
			}
			if !strings.HasPrefix(d.Title, prefix) {
				d.Title = prefix + d.Title
			}
			evolved = append(evolved, *d)
		}
	}

	return evolved
}

// TriggerMiniSwarm spawns a cluster of 12-18 demons in a targeted scripture theme
func (m *DemonManager) TriggerMiniSwarm(targetTheme string) int {
	m.mu.Lock()
	countToSpawn := 12 + rand.Intn(7) // 12-18 demons
	if len(m.wildDemons)+countToSpawn > m.maxWildDemons {
		countToSpawn = m.maxWildDemons - len(m.wildDemons)
	}
	m.mu.Unlock()

	if countToSpawn <= 0 {
		return 0
	}

	spawned := 0
	for i := 0; i < countToSpawn; i++ {
		d := m.SpawnPeriodicWildDemon()
		if d != nil {
			spawned++
		}
	}

	if spawned > 0 && m.hub != nil {
		activeCount := len(m.GetWildDemons())
		sanct := int((1.0 - float64(activeCount)/666.0) * 100)
		if sanct < 0 {
			sanct = 0
		}
		themeLabel := targetTheme
		if themeLabel == "" {
			themeLabel = "the Holy Scriptures"
		}
		msg := fmt.Sprintf("🔥 DAILY MINI-SWARM! A surge of %d adversaries has breached %s! Rally the fellowship!", spawned, themeLabel)
		m.hub.BroadcastSwarmEvent(msg, activeCount, 666, sanct)
		m.hub.BroadcastWildDemonsList(m.GetWildDemons())
	}

	return spawned
}

// TriggerMajorSwarm spawns a massive Sabbath siege of 40-66 demons across the scriptures
func (m *DemonManager) TriggerMajorSwarm() int {
	m.mu.Lock()
	countToSpawn := 40 + rand.Intn(27) // 40-66 demons
	if len(m.wildDemons)+countToSpawn > m.maxWildDemons {
		countToSpawn = m.maxWildDemons - len(m.wildDemons)
	}
	m.mu.Unlock()

	if countToSpawn <= 0 {
		return 0
	}

	spawned := 0
	for i := 0; i < countToSpawn; i++ {
		d := m.SpawnPeriodicWildDemon()
		if d != nil {
			spawned++
		}
	}

	if spawned > 0 && m.hub != nil {
		activeCount := len(m.GetWildDemons())
		sanct := int((1.0 - float64(activeCount)/666.0) * 100)
		if sanct < 0 {
			sanct = 0
		}
		msg := fmt.Sprintf("⚡ SUNDAY MAJOR SWARM! The Sabbath Incursion has begun! %d ancient adversaries have breached sacred scripture! Unite to defend the Word!", spawned)
		m.hub.BroadcastSwarmEvent(msg, activeCount, 666, sanct)
		m.hub.BroadcastWildDemonsList(m.GetWildDemons())
	}

	return spawned
}

func (m *DemonManager) isVerseOccupiedLocked(book string, chapter, verse int) bool {
	for _, d := range m.wildDemons {
		if d.BookName == book && d.Chapter == chapter && d.Verse == verse {
			return true
		}
	}
	return false
}

func (m *DemonManager) findUnoccupiedVerseLocked() (string, int, int, string) {
	trans := "kjv"
	td, ok := translations[trans]
	if !ok || len(td.CanonicalBooks) == 0 {
		return "", 0, 0, ""
	}

	// 1. Try atmospheric spiritual warfare passages first
	perm := rand.Perm(len(atmosphericDemonicPassages))
	for _, idx := range perm {
		p := atmosphericDemonicPassages[idx]
		if !m.isVerseOccupiedLocked(p.Book, p.Chapter, p.Verse) {
			if vList, ok := td.VersesByBook[p.Book]; ok {
				for _, v := range vList {
					if v.Chapter == p.Chapter && v.Verse == p.Verse {
						return p.Book, p.Chapter, p.Verse, cleanVerseText(v.Text)
					}
				}
			}
		}
	}

	// 2. Otherwise pick randomly across canonical scriptures (all 31,102 verses)
	bookPerm := rand.Perm(len(td.CanonicalBooks))
	for _, bIdx := range bookPerm {
		bookName := td.CanonicalBooks[bIdx]
		vList := td.VersesByBook[bookName]
		if len(vList) == 0 {
			continue
		}
		versePerm := rand.Perm(len(vList))
		for _, vIdx := range versePerm {
			v := vList[vIdx]
			if !m.isVerseOccupiedLocked(bookName, v.Chapter, v.Verse) {
				return bookName, v.Chapter, v.Verse, cleanVerseText(v.Text)
			}
		}
	}

	return "", 0, 0, ""
}

// SpawnPeriodicWildDemon periodically spawns a wild demon onto an open scripture passage
func (m *DemonManager) SpawnPeriodicWildDemon() *WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.wildDemons) >= m.maxWildDemons {
		return nil
	}

	book, ch, v, text := m.findUnoccupiedVerseLocked()
	if book == "" || text == "" {
		return nil
	}

	now := time.Now().UTC()
	rankWeights := []string{"imp", "imp", "fiend", "fiend", "tormentor", "apollyon"}
	chosenRank := rankWeights[rand.Intn(len(rankWeights))]

	level := 1
	var icon, color, title, name string
	switch chosenRank {
	case "imp":
		icon = "😈"
		color = "#ef4444"
		title = "Tempter of the Void"
		name = fmt.Sprintf("Whispering Imp of %s", book)
	case "fiend":
		icon = "👹"
		color = "#dc2626"
		title = "Stumbling Stone"
		name = fmt.Sprintf("Brimstone Fiend of %s", book)
	case "tormentor":
		level = 2
		icon = "👺"
		color = "#b91c1c"
		title = "Accuser of the Brethren"
		name = fmt.Sprintf("Tormentor of %s", book)
	case "apollyon":
		level = 3
		icon = "👑🔥"
		color = "#991b1b"
		title = "Harbinger of Abaddon"
		name = fmt.Sprintf("Apollyon Harbinger of %s", book)
	}

	id := fmt.Sprintf("wild-interval-%d", time.Now().UnixNano()%1000000)
	wpm := 10 + rand.Intn(13) // 10 - 22 slow WPM
	hp := 100 + level*60

	demon := &WildDemon{
		ID:          id,
		Rank:        chosenRank,
		Level:       level,
		MaxLevel:    5,
		Name:        name,
		Title:       title,
		Icon:        icon,
		Color:       color,
		Translation: "kjv",
		BookName:    book,
		Chapter:     ch,
		Verse:       v,
		Text:        text,
		WPM:         wpm,
		MaxHP:       hp,
		CurrentHP:   hp,
		EnrageCount: 0,
		CharIndex:   0,
		SpawnedAt:   now,
		LastEvolve:  now,
		AgeMinutes:  0,
	}

	m.wildDemons = append(m.wildDemons, demon)
	m.wildDemonBases[demon.ID] = demon.WPM
	go m.runWildDemonLoop(demon)
	return demon
}

// CalculateEncounterChance computes the ambush probability based on completed verses in the current book.
// The more verses completed in a book, the higher the chance of coming across a demon!
func CalculateEncounterChance(completedVerses, totalVerses int) float64 {
	if totalVerses <= 0 || completedVerses < 2 {
		return 0.0 // Peaceful opening for the first 2 verses of any book
	}

	ratio := float64(completedVerses) / float64(totalVerses)
	if ratio > 1.0 {
		ratio = 1.0
	}

	verseBonus := float64(completedVerses) * 0.005
	if verseBonus > 0.20 {
		verseBonus = 0.20
	}

	chance := 0.04 + (ratio * 0.30) + verseBonus
	if chance > 0.50 {
		chance = 0.50
	}
	if chance < 0.04 {
		chance = 0.04
	}
	return chance
}

// CheckOrSpawnWildDemonForUser evaluates demon presence and progress-based encounter chance on verse arrival
func (m *DemonManager) CheckOrSpawnWildDemonForUser(trans string, v Verse, currentVerseIdx, totalVerses int) (*WildDemon, float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Check if a wild demon is already roaming on this verse (lives until defeated)
	for _, d := range m.wildDemons {
		if d.Translation == trans && d.BookName == v.BookName && d.Chapter == v.Chapter && d.Verse == v.Verse {
			res := *d
			return &res, 1.0, false
		}
	}

	// 2. Calculate encounter chance based on verses completed in this book
	chance := CalculateEncounterChance(currentVerseIdx, totalVerses)
	if chance <= 0.0 {
		return nil, 0.0, false
	}

	// 3. Pool capacity check
	if len(m.wildDemons) >= m.maxWildDemons {
		return nil, chance, false
	}

	// 4. Roll for dynamic encounter
	if rand.Float64() < chance {
		demon := m.createDemonForProgress(trans, v, float64(currentVerseIdx)/float64(totalVerses))
		m.wildDemons = append(m.wildDemons, demon)
		m.wildDemonBases[demon.ID] = demon.WPM
		go m.runWildDemonLoop(demon)

		if m.hub != nil {
			m.hub.BroadcastWildDemonsList(m.getWildDemonsLocked())
		}
		res := *demon
		return &res, chance, true
	}

	return nil, chance, false
}

func (m *DemonManager) getWildDemonsLocked() []WildDemon {
	res := make([]WildDemon, len(m.wildDemons))
	for i, d := range m.wildDemons {
		res[i] = *d
	}
	return res
}

func (m *DemonManager) createDemonForProgress(trans string, v Verse, progressRatio float64) *WildDemon {
	var rank string
	var level int
	var color string
	var icon string
	var namePrefix string
	var title string

	if progressRatio < 0.25 {
		rank = "imp"
		level = 1
		color = "#ef4444"
		icon = "😈"
		titles := []string{"Lurker of Eden", "Tempter of Early Verse", "Whispering Imp", "Shadow of Distraction"}
		title = titles[rand.Intn(len(titles))]
		namePrefix = "Whispering Imp"
	} else if progressRatio < 0.60 {
		rank = "fiend"
		level = 2
		color = "#dc2626"
		icon = "👹"
		titles := []string{"Stumbling Stone", "Wanderer in the Earth", "Brimstone Stalker", "Deceiver of Mid-Scroll"}
		title = titles[rand.Intn(len(titles))]
		namePrefix = "Brimstone Fiend"
	} else if progressRatio < 0.85 {
		rank = "tormentor"
		level = 3
		color = "#b91c1c"
		icon = "👺"
		titles := []string{"Accuser of the Brethren", "Lightning of the Pit", "Fallen Star", "Dragon's Scribe"}
		title = titles[rand.Intn(len(titles))]
		namePrefix = "Brimstone Tormentor"
	} else {
		rank = "apollyon"
		level = 4
		color = "#991b1b"
		icon = "👑🔥"
		titles := []string{"Angel of the Bottomless Pit", "Abyssal Destroyer", "Prince of the Underworld", "Shadow Sovereign"}
		title = titles[rand.Intn(len(titles))]
		namePrefix = "Apollyon Harbinger"
	}

	name := fmt.Sprintf("%s of %s", namePrefix, v.BookName)
	wpm := 10 + rand.Intn(13) // 10 - 22 WPM
	hp := 120 + level*60
	id := fmt.Sprintf("wild-ambush-%d", time.Now().UnixNano()%1000000)

	now := time.Now().UTC()
	return &WildDemon{
		ID:          id,
		Rank:        rank,
		Level:       level,
		MaxLevel:    5,
		Name:        name,
		Title:       title,
		Icon:        icon,
		Color:       color,
		Translation: trans,
		BookName:    v.BookName,
		Chapter:     v.Chapter,
		Verse:       v.Verse,
		Text:        cleanVerseText(v.Text),
		WPM:         wpm,
		MaxHP:       hp,
		CurrentHP:   hp,
		EnrageCount: 0,
		CharIndex:   0,
		SpawnedAt:   now,
		LastEvolve:  now,
		AgeMinutes:  0,
	}
}

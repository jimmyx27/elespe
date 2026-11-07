package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// DemonSyncBalanceConfig holds tunable parameters for word-in-sync combat stack scaling,
// punctuation checkpoints, dynamic demon WPM rubber-banding, and passage finish burst damage.
// Kept centralized here for future balancing adjustments.
type DemonSyncBalanceConfig struct {
	StacksPerWord            int     `json:"stacks_per_word"`              // Stacks gained per word typed in sync (default: 1)
	StacksPerDamageTier      int     `json:"stacks_per_damage_tier"`       // Number of stacks required to increase damage (default: 5)
	FlatDamagePerTier        int     `json:"flat_damage_per_tier"`         // Flat bonus damage per tier (every 5 stacks) (default: 1)
	DamageMultPerTier        float64 `json:"damage_mult_per_tier"`         // Multiplicative bonus per tier (e.g. 0.25 = +25% per 5 stacks)
	MaxStacks                int     `json:"max_stacks"`                   // Optional cap on stacks (0 = uncapped)
	CharSyncTolerance        int     `json:"char_sync_tolerance"`          // Max character distance from demon to count as "in sync" / radiance window (default: 5)
	PunctuationPerCheckpoint int     `json:"punctuation_per_checkpoint"`   // Number of punctuation marks reached to lock in a checkpoint (default: 2)
	StacksLostOnBacklash     int     `json:"stacks_lost_on_backlash"`      // Sync stacks lost on unwarded mistake backlash (default: 2)
	BacklashHealPct          float64 `json:"backlash_heal_pct"`            // Fraction of MaxHP healed by demon on mistake (default: 0.10)
	PaceAdjustThreshold      int     `json:"pace_adjust_threshold"`        // Char lead/lag threshold where dynamic WPM increase/decrease kicks in (default: 5)
	PaceStepChars            int     `json:"pace_step_chars"`              // Additional chars beyond threshold per WPM step (default: 3)
	PaceWPMPerStep           int     `json:"pace_wpm_per_step"`            // WPM change per step ahead/behind (default: 2)
	MinDemonWPM              int     `json:"min_demon_wpm"`                // Minimum dynamic WPM floor when user is slower (default: 6)
	MaxDemonWPMBonus         int     `json:"max_demon_wpm_bonus"`          // Max bonus WPM above base when user is ahead (default: 18)
	MaxDemonWPMReduction     int     `json:"max_demon_wpm_reduction"`      // Max WPM reduction below base when user is slower (default: 8)
	PassageBurstBaseDamage   int     `json:"passage_burst_base_damage"`    // Base burst damage when user finishes a passage before the demon (default: 30)
	PassageBurstPerCharAhead int     `json:"passage_burst_per_char_ahead"` // Bonus burst damage per char the demon has left when user finishes (default: 1)
	PassageBurstMaxCharBonus int     `json:"passage_burst_max_char_bonus"` // Cap on bonus damage from char lead at passage finish (default: 30)
	PassageBurstPerSyncStack int     `json:"passage_burst_per_sync_stack"` // Bonus burst damage per active sync stack on passage finish (default: 2)
	PassageBurstMultiplier   float64 `json:"passage_burst_multiplier"`     // Adjustable multiplier for passage finish burst damage (default: 1.0)
	MinPassageBurstDamage    int     `json:"min_passage_burst_damage"`     // Minimum burst damage floor (default: 5)
	MaxPassageBurstDamage    int     `json:"max_passage_burst_damage"`     // Maximum burst damage cap (default: 300)
}

var demonBalanceMu sync.RWMutex

// DefaultDemonSyncBalance defines the active balance values for demon sync stacks, checkpoints, dynamic pacing, and burst damage.
var DefaultDemonSyncBalance = DemonSyncBalanceConfig{
	StacksPerWord:            1,
	StacksPerDamageTier:      5,
	FlatDamagePerTier:        1,
	DamageMultPerTier:        0.25,
	MaxStacks:                0,
	CharSyncTolerance:        5,
	PunctuationPerCheckpoint: 2,
	StacksLostOnBacklash:     2,
	BacklashHealPct:          0.10,
	PaceAdjustThreshold:      5,
	PaceStepChars:            3,
	PaceWPMPerStep:           2,
	MinDemonWPM:              6,
	MaxDemonWPMBonus:         18,
	MaxDemonWPMReduction:     8,
	PassageBurstBaseDamage:   30,
	PassageBurstPerCharAhead: 1,
	PassageBurstMaxCharBonus: 30,
	PassageBurstPerSyncStack: 2,
	PassageBurstMultiplier:   1.0,
	MinPassageBurstDamage:    5,
	MaxPassageBurstDamage:    300,
}

func GetDemonSyncBalance() DemonSyncBalanceConfig {
	demonBalanceMu.RLock()
	defer demonBalanceMu.RUnlock()
	return DefaultDemonSyncBalance
}

func SetDemonSyncBalance(cfg DemonSyncBalanceConfig) {
	demonBalanceMu.Lock()
	DefaultDemonSyncBalance = cfg
	demonBalanceMu.Unlock()
}

func titleCaseASCII(s string) string {
	words := strings.Fields(strings.ReplaceAll(s, "_", " "))
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, " ")
}

// IsCheckpointPunctuation returns true if the character is a scripture punctuation mark that counts toward a checkpoint.
func IsCheckpointPunctuation(r rune) bool {
	switch r {
	case ',', ';', ':', '.', '!', '?', '—', '-':
		return true
	default:
		return false
	}
}

// CalculatePunctuationCheckpoint returns the checkpoint character index and total punctuation marks crossed
// for a given passage text and number of characters typed.
func CalculatePunctuationCheckpoint(text string, typedLen int) (checkpointIdx int, punctCount int) {
	return CalculatePunctuationCheckpointWithConfig(text, typedLen, GetDemonSyncBalance())
}

// CalculatePunctuationCheckpointWithConfig calculates the checkpoint index using a custom balance config.
func CalculatePunctuationCheckpointWithConfig(text string, typedLen int, cfg DemonSyncBalanceConfig) (checkpointIdx int, punctCount int) {
	runes := []rune(text)
	if typedLen > len(runes) {
		typedLen = len(runes)
	}
	if typedLen <= 0 {
		return 0, 0
	}
	perCP := cfg.PunctuationPerCheckpoint
	if perCP <= 0 {
		perCP = 2
	}
	for i := 0; i < typedLen; i++ {
		if IsCheckpointPunctuation(runes[i]) {
			punctCount++
			if punctCount%perCP == 0 {
				nextIdx := i + 1
				if nextIdx < len(runes) && runes[nextIdx] == ' ' && nextIdx+1 <= typedLen {
					nextIdx++
				}
				checkpointIdx = nextIdx
			}
		}
	}
	return checkpointIdx, punctCount
}

// CalculateDynamicDemonWPM adjusts the demon's WPM dynamically based on whether the player is typing
// 5+ chars ahead (increases demon WPM) or typing slower / falling behind (decreases demon WPM).
func CalculateDynamicDemonWPM(baseWPM, userCharIdx, demonCharIdx, userWPM int) int {
	return CalculateDynamicDemonWPMWithConfig(baseWPM, userCharIdx, demonCharIdx, userWPM, GetDemonSyncBalance())
}

// CalculateDynamicDemonWPMWithConfig computes dynamic demon WPM using a custom balance config.
func CalculateDynamicDemonWPMWithConfig(baseWPM, userCharIdx, demonCharIdx, userWPM int, cfg DemonSyncBalanceConfig) int {
	if baseWPM < cfg.MinDemonWPM {
		baseWPM = cfg.MinDemonWPM
	}
	threshold := cfg.PaceAdjustThreshold
	if threshold <= 0 {
		threshold = 5
	}
	stepChars := cfg.PaceStepChars
	if stepChars <= 0 {
		stepChars = 3
	}
	wpmStep := cfg.PaceWPMPerStep
	if wpmStep <= 0 {
		wpmStep = 2
	}

	charDiff := userCharIdx - demonCharIdx

	// 1. User is typing 5+ chars ahead of the demon: increase demon WPM
	if charDiff >= threshold {
		steps := 1 + (charDiff-threshold)/stepChars
		bonus := steps * wpmStep
		if userWPM > baseWPM {
			bonus += (userWPM - baseWPM) / 4
		}
		if cfg.MaxDemonWPMBonus > 0 && bonus > cfg.MaxDemonWPMBonus {
			bonus = cfg.MaxDemonWPMBonus
		}
		return baseWPM + bonus
	}

	// 2. User is 5+ chars behind the demon: decrease demon WPM so player can recover
	if charDiff <= -threshold {
		behind := -charDiff
		steps := 1 + (behind-threshold)/stepChars
		reduction := steps * wpmStep
		if userWPM > 0 && userWPM < baseWPM {
			reduction += (baseWPM - userWPM) / 4
		}
		if cfg.MaxDemonWPMReduction > 0 && reduction > cfg.MaxDemonWPMReduction {
			reduction = cfg.MaxDemonWPMReduction
		}
		res := baseWPM - reduction
		if res < cfg.MinDemonWPM {
			res = cfg.MinDemonWPM
		}
		return res
	}

	// 3. Within the ±5 char radiance window: subtle dynamic response to faster/slower live WPM
	if charDiff < 0 && userWPM > 0 && userWPM < baseWPM {
		reduction := (baseWPM - userWPM) / 3
		if reduction < 1 {
			reduction = 1
		}
		maxInWindowRed := cfg.MaxDemonWPMReduction / 2
		if maxInWindowRed > 0 && reduction > maxInWindowRed {
			reduction = maxInWindowRed
		}
		res := baseWPM - reduction
		if res < cfg.MinDemonWPM {
			res = cfg.MinDemonWPM
		}
		return res
	}

	if charDiff > 0 && userWPM > baseWPM+4 {
		bonus := (userWPM - baseWPM) / 5
		if bonus < 1 {
			bonus = 1
		}
		maxInWindowBonus := cfg.MaxDemonWPMBonus / 3
		if maxInWindowBonus > 0 && bonus > maxInWindowBonus {
			bonus = maxInWindowBonus
		}
		return baseWPM + bonus
	}

	return baseWPM
}

// GetSyncDamageTier returns the number of 5-stack damage tiers achieved for a given stack count.
func GetSyncDamageTier(syncStacks int) int {
	return GetSyncDamageTierWithConfig(syncStacks, GetDemonSyncBalance())
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
	return CalculateSyncStackDamageWithConfig(baseDamage, syncStacks, GetDemonSyncBalance())
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

// CalculatePassageFinishBurstDamage computes the burst damage applied when a user finishes
// typing a passage before a wild demon. customBaseBurst overrides the default base burst if > 0.
func CalculatePassageFinishBurstDamage(baseStrikeDamage, syncStacks, charsAhead, customBaseBurst int) int {
	return CalculatePassageFinishBurstDamageWithConfig(baseStrikeDamage, syncStacks, charsAhead, customBaseBurst, GetDemonSyncBalance())
}

// CalculatePassageFinishBurstDamageWithConfig computes passage finish burst damage using a custom balance config.
func CalculatePassageFinishBurstDamageWithConfig(baseStrikeDamage, syncStacks, charsAhead, customBaseBurst int, cfg DemonSyncBalanceConfig) int {
	if baseStrikeDamage < 1 {
		baseStrikeDamage = 1
	}
	if syncStacks < 0 {
		syncStacks = 0
	}
	if cfg.MaxStacks > 0 && syncStacks > cfg.MaxStacks {
		syncStacks = cfg.MaxStacks
	}
	if charsAhead < 0 {
		charsAhead = 0
	}

	burst := cfg.PassageBurstBaseDamage
	if burst <= 0 {
		burst = 30
	}
	if customBaseBurst > 0 {
		burst = customBaseBurst
	}

	// Guardian power scaling (+5 burst per strike damage point above 1)
	if baseStrikeDamage > 1 {
		burst += (baseStrikeDamage - 1) * 5
	}

	// Bonus for characters the demon still had remaining when user finished
	if charsAhead > 0 && cfg.PassageBurstPerCharAhead > 0 {
		charBonus := charsAhead * cfg.PassageBurstPerCharAhead
		if cfg.PassageBurstMaxCharBonus > 0 && charBonus > cfg.PassageBurstMaxCharBonus {
			charBonus = cfg.PassageBurstMaxCharBonus
		}
		burst += charBonus
	}

	// Flat bonus per active sync stack
	if syncStacks > 0 && cfg.PassageBurstPerSyncStack > 0 {
		burst += syncStacks * cfg.PassageBurstPerSyncStack
	}

	// Apply sync stack damage tier scaling
	burst = CalculateSyncStackDamageWithConfig(burst, syncStacks, cfg)

	// Apply configurable burst multiplier
	mult := cfg.PassageBurstMultiplier
	if mult <= 0 {
		mult = 1.0
	}
	burst = int(math.Round(float64(burst) * mult))

	minBurst := cfg.MinPassageBurstDamage
	if minBurst <= 0 {
		minBurst = 5
	}
	if burst < minBurst {
		burst = minBurst
	}
	if cfg.MaxPassageBurstDamage > 0 && burst > cfg.MaxPassageBurstDamage {
		burst = cfg.MaxPassageBurstDamage
	}
	return burst
}

var demonTypesMu sync.RWMutex

func defaultDemonTypeMap() (map[string]DemonInfo, []string) {
	m := map[string]DemonInfo{
		"imp": {
			ID:                   "demon-imp",
			Rank:                 "imp",
			Name:                 "Mischievous Imp",
			Title:                "Shadow Whisperer",
			Description:          "A sly imp that casts billowing smoke clouds over holy words for 2.5 seconds to distract your focus.",
			Level:                1,
			MaxLevel:             5,
			WPM:                  15,
			BaseLevelWPM:         15,
			WPMPerLevel:          5,
			WildMinWPM:           10,
			WildMaxWPM:           22,
			WildBaseHP:           100,
			WildHPPerLevel:       60,
			SpawnWeight:          2,
			MinProgressRatio:     0.0,
			MaxProgressRatio:     0.25,
			AbilityIntervalChars: 10,
			ShroudChance:         0.60,
			BaseShroudRange:      2,
			Color:                "#ef4444",
			Icon:                 "😈",
			BlockDuration:        2.5,
		},
		"fiend": {
			ID:                   "demon-fiend",
			Rank:                 "fiend",
			Name:                 "Shadow Fiend",
			Title:                "Stumbling Stone",
			Description:          "A looming shadow demon that shrouds scripture in dark fog and casts demonic typo curses to test your spiritual resilience.",
			Level:                1,
			MaxLevel:             4,
			WPM:                  35,
			BaseLevelWPM:         35,
			WPMPerLevel:          10,
			WildMinWPM:           10,
			WildMaxWPM:           22,
			WildBaseHP:           100,
			WildHPPerLevel:       60,
			SpawnWeight:          2,
			MinProgressRatio:     0.25,
			MaxProgressRatio:     0.60,
			AbilityIntervalChars: 10,
			ShroudChance:         0.60,
			BaseShroudRange:      2,
			Color:                "#dc2626",
			Icon:                 "👹",
			BlockDuration:        3.0,
		},
		"tormentor": {
			ID:                   "demon-tormentor",
			Rank:                 "tormentor",
			Name:                 "Brimstone Tormentor",
			Title:                "Accuser of the Brethren",
			Description:          "Fierce fallen herald armed with brimstone smog. Obscures entire phrases and attempts to swap letters into demonic typos.",
			Level:                1,
			MaxLevel:             4,
			WPM:                  70,
			BaseLevelWPM:         70,
			WPMPerLevel:          15,
			WildMinWPM:           10,
			WildMaxWPM:           22,
			WildBaseHP:           100,
			WildHPPerLevel:       60,
			SpawnWeight:          1,
			MinProgressRatio:     0.60,
			MaxProgressRatio:     0.85,
			AbilityIntervalChars: 10,
			ShroudChance:         0.60,
			BaseShroudRange:      2,
			Color:                "#b91c1c",
			Icon:                 "👺",
			BlockDuration:        3.5,
		},
		"apollyon": {
			ID:                   "demon-apollyon",
			Rank:                 "apollyon",
			Name:                 "Apollyon the Destroyer",
			Title:                "Angel of the Bottomless Pit",
			Description:          "The infernal sovereign of Abaddon (Rev 9:11). Casts abyssal darkness across verses, operating 24/7 without rest.",
			Level:                2,
			MaxLevel:             4,
			WPM:                  160,
			BaseLevelWPM:         130,
			WPMPerLevel:          30,
			WildMinWPM:           10,
			WildMaxWPM:           22,
			WildBaseHP:           100,
			WildHPPerLevel:       60,
			SpawnWeight:          1,
			MinProgressRatio:     0.85,
			MaxProgressRatio:     1.0,
			AbilityIntervalChars: 10,
			ShroudChance:         0.60,
			BaseShroudRange:      2,
			Color:                "#991b1b",
			Icon:                 "👑🔥",
			BlockDuration:        4.0,
			IsBoss:               true,
			TotalHP:              6000,
			CurrentHP:            6000,
		},
	}
	order := []string{"imp", "fiend", "tormentor", "apollyon"}
	return m, order
}

// Predefined Demon Adversary Profiles with Levels
var demonTemplates, canonicalDemons = defaultDemonTypeMap()

func isBuiltInDemonRank(rank string) bool {
	return rank == "imp" || rank == "fiend" || rank == "tormentor" || rank == "apollyon"
}

func sanitizeDemonType(d DemonInfo) DemonInfo {
	d.Rank = strings.ToLower(strings.TrimSpace(d.Rank))
	d.Rank = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, d.Rank)
	if d.Rank == "" {
		d.Rank = fmt.Sprintf("custom_%d", time.Now().Unix()%10000)
	}
	if d.ID == "" {
		d.ID = "demon-" + d.Rank
	}
	if strings.TrimSpace(d.Name) == "" {
		d.Name = titleCaseASCII(d.Rank) + " Adversary"
	}
	if strings.TrimSpace(d.Title) == "" {
		d.Title = "Demonic Adversary"
	}
	if strings.TrimSpace(d.Description) == "" {
		d.Description = "A shadowy adversary that obscures scripture and tests your focus."
	}
	if strings.TrimSpace(d.Icon) == "" {
		d.Icon = "😈"
	}
	if strings.TrimSpace(d.Color) == "" {
		d.Color = "#ef4444"
	}
	if d.Level < 1 {
		d.Level = 1
	}
	if d.MaxLevel < 1 {
		d.MaxLevel = 5
	}
	if d.Level > d.MaxLevel {
		d.Level = d.MaxLevel
	}
	if d.BaseLevelWPM <= 0 {
		if d.WPM > 0 {
			d.BaseLevelWPM = d.WPM
		} else {
			d.BaseLevelWPM = 20
		}
	}
	if d.WPMPerLevel < 0 {
		d.WPMPerLevel = 0
	}
	if d.WPM <= 0 {
		d.WPM = d.BaseLevelWPM + (d.Level-1)*d.WPMPerLevel
	}
	if d.WildMinWPM < 4 {
		d.WildMinWPM = 10
	}
	if d.WildMaxWPM < d.WildMinWPM {
		d.WildMaxWPM = d.WildMinWPM + 12
	}
	if d.WildBaseHP < 10 {
		d.WildBaseHP = 100
	}
	if d.WildHPPerLevel < 0 {
		d.WildHPPerLevel = 60
	}
	if d.SpawnWeight < 0 {
		d.SpawnWeight = 0
	}
	if d.BlockDuration <= 0 {
		d.BlockDuration = 2.8
	}
	if d.AbilityIntervalChars < 2 {
		d.AbilityIntervalChars = 10
	}
	if d.ShroudChance < 0 || d.ShroudChance > 1 {
		d.ShroudChance = 0.60
	}
	if d.BaseShroudRange < 1 {
		d.BaseShroudRange = 2
	}
	if d.MaxProgressRatio <= 0 && d.MinProgressRatio <= 0 {
		d.MaxProgressRatio = 1.0
	}
	d.IsCustom = !isBuiltInDemonRank(d.Rank)
	return d
}

func GetDemonType(rank string) (DemonInfo, bool) {
	demonTypesMu.RLock()
	defer demonTypesMu.RUnlock()
	t, ok := demonTemplates[rank]
	return t, ok
}

func GetDemonTypes() []DemonInfo {
	demonTypesMu.RLock()
	defer demonTypesMu.RUnlock()
	list := make([]DemonInfo, 0, len(canonicalDemons))
	for _, r := range canonicalDemons {
		if t, ok := demonTemplates[r]; ok {
			t.WPM = getDemonWPMLocked(t, t.Level)
			list = append(list, t)
		}
	}
	return list
}

func SetDemonTypes(types []DemonInfo) {
	if len(types) == 0 {
		return
	}
	demonTypesMu.Lock()
	newMap := make(map[string]DemonInfo, len(types))
	newOrder := make([]string, 0, len(types))
	for _, raw := range types {
		clean := sanitizeDemonType(raw)
		if _, exists := newMap[clean.Rank]; !exists {
			newOrder = append(newOrder, clean.Rank)
		}
		newMap[clean.Rank] = clean
	}
	// Ensure the 4 canonical ranks always exist unless explicitly overridden
	defMap, defOrder := defaultDemonTypeMap()
	for _, r := range defOrder {
		if _, ok := newMap[r]; !ok {
			newMap[r] = defMap[r]
			newOrder = append(newOrder, r)
		}
	}
	demonTemplates = newMap
	canonicalDemons = newOrder
	demonTypesMu.Unlock()
}

func UpsertDemonType(info DemonInfo) DemonInfo {
	clean := sanitizeDemonType(info)
	demonTypesMu.Lock()
	_, exists := demonTemplates[clean.Rank]
	demonTemplates[clean.Rank] = clean
	if !exists {
		canonicalDemons = append(canonicalDemons, clean.Rank)
	}
	demonTypesMu.Unlock()
	clean.WPM = GetDemonWPM(clean.Rank, clean.Level)
	return clean
}

func DeleteDemonType(rank string) bool {
	rank = strings.ToLower(strings.TrimSpace(rank))
	demonTypesMu.Lock()
	defer demonTypesMu.Unlock()
	if isBuiltInDemonRank(rank) {
		// Reset built-in rank back to default instead of removing canonical rank
		defMap, _ := defaultDemonTypeMap()
		if def, ok := defMap[rank]; ok {
			demonTemplates[rank] = def
			return true
		}
		return false
	}
	if _, ok := demonTemplates[rank]; !ok {
		return false
	}
	delete(demonTemplates, rank)
	for i, r := range canonicalDemons {
		if r == rank {
			canonicalDemons = append(canonicalDemons[:i], canonicalDemons[i+1:]...)
			break
		}
	}
	return true
}

func ResetDefaultDemonTypes() []DemonInfo {
	demonTypesMu.Lock()
	demonTemplates, canonicalDemons = defaultDemonTypeMap()
	demonTypesMu.Unlock()
	return GetDemonTypes()
}

func getDemonWPMLocked(t DemonInfo, level int) int {
	if level < 1 {
		level = 1
	}
	maxLvl := t.MaxLevel
	if maxLvl < 1 {
		maxLvl = 5
	}
	if level > maxLvl {
		level = maxLvl
	}
	base := t.BaseLevelWPM
	if base <= 0 {
		base = t.WPM
	}
	if base <= 0 {
		base = 20
	}
	return base + (level-1)*t.WPMPerLevel
}

// GetDemonWPM calculates the exact speed for a demon rank and level
func GetDemonWPM(rank string, level int) int {
	demonTypesMu.RLock()
	t, ok := demonTemplates[rank]
	demonTypesMu.RUnlock()
	if ok {
		return getDemonWPMLocked(t, level)
	}
	if level < 1 {
		level = 1
	}
	return 20 + (level-1)*5
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

var (
	globalDemonMu      sync.RWMutex
	globalDemonManager *DemonManager
)

func getGlobalDemonManager() *DemonManager {
	globalDemonMu.RLock()
	defer globalDemonMu.RUnlock()
	return globalDemonManager
}

func setGlobalDemonManager(dm *DemonManager) {
	globalDemonMu.Lock()
	prev := globalDemonManager
	globalDemonManager = dm
	globalDemonMu.Unlock()
	if prev != nil && prev != dm {
		prev.Stop()
	}
}

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
	return GetDemonTypes()
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
	m.StopDevil24_7()

	template, ok := GetDemonType(defaultRank)
	if !ok {
		template, _ = GetDemonType("imp")
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

// StopDevil24_7 stops the autonomous 24/7 devil loop if running.
func (m *DemonManager) StopDevil24_7() {
	m.mu.Lock()
	bot := m.devil24_7
	m.devil24_7 = nil
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

// SetDevil24_7Config updates the rank and level of the 24/7 devil bot if running.
func (m *DemonManager) SetDevil24_7Config(rank string, level int) {
	m.mu.RLock()
	running := m.devil24_7 != nil
	curRank := ""
	curLevel := 0
	if m.devil24_7 != nil {
		m.devil24_7.mu.RLock()
		curRank = m.devil24_7.Info.Rank
		curLevel = m.devil24_7.Info.Level
		m.devil24_7.mu.RUnlock()
	}
	m.mu.RUnlock()

	if running && (curRank != rank || curLevel != level) {
		m.StartDevil24_7(rank, level)
	}
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
		select {
		case <-bot.StopChan:
			return
		case <-m.stopSpawnChan:
			return
		default:
		}

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
			case <-m.stopSpawnChan:
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
					TrailUpTo:   i,
				})
			}

			// Add small jitter
			jitter := time.Duration((rand.Float64()*0.3 - 0.15) * float64(charDelay))
			sleepDur := charDelay + jitter
			if sleepDur < 10*time.Millisecond {
				sleepDur = 10 * time.Millisecond
			}
			select {
			case <-bot.StopChan:
				return
			case <-m.stopSpawnChan:
				return
			case <-time.After(sleepDur):
			}
		}

		// Verse completed by 24/7 devil!
		m.mu.Lock()
		m.devil24_7VersesCount++
		m.mu.Unlock()

		// Brief pause between verses
		select {
		case <-bot.StopChan:
			return
		case <-m.stopSpawnChan:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// SummonDemon creates an active demon adversary to challenge the user on their verse
func (m *DemonManager) SummonDemon(uid, rank string, level int, trans, book string, chapter, verse int, text string) *DemonInfo {
	m.DismissDemon(uid)

	template, ok := GetDemonType(rank)
	if !ok {
		template, _ = GetDemonType("imp")
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
		intervalChars := bot.Info.AbilityIntervalChars
		shroudChance := bot.Info.ShroudChance
		baseShroudRange := bot.Info.BaseShroudRange
		bot.mu.RUnlock()

		if intervalChars < 2 {
			intervalChars = 10
		}
		if shroudChance <= 0 {
			shroudChance = 0.60
		}
		if baseShroudRange < 1 {
			baseShroudRange = 2
		}

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
		completed := true

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
			// If user moved to a new passage while the demon was mid-verse, restart on the new passage
			if bot.Translation != trans || bot.BookName != book || bot.Chapter != ch || bot.Verse != v {
				completed = false
				bot.mu.Unlock()
				break
			}
			bot.CharIndex = i
			bot.mu.Unlock()

			// Demons cast abilities as they type according to their configured DemonInfo parameters
			var ability string
			shroudRange := 0
			if i > 0 && i%intervalChars == 0 {
				if rand.Float64() < shroudChance {
					ability = "shroud"
					shroudRange = baseShroudRange + (lvl / 2)
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
					TrailUpTo:   i,
				})
			}

			select {
			case <-bot.StopChan:
				return
			case <-m.stopSpawnChan:
				return
			case <-time.After(charDelay):
			}
		}

		if !completed {
			continue
		}

		// When demon completes the passage before the user:
		if m.hub != nil {
			m.hub.SendToUID(uid, Message{
				Type:    "demon_complete",
				Content: fmt.Sprintf("%s %s (Lv. %d) outpaced you! Cleanse the holy scripture to banish the adversary!", bot.Info.Icon, bot.Info.Name, lvl),
				Demon:   &bot.Info,
			})
		}

		select {
		case <-bot.StopChan:
			return
		case <-m.stopSpawnChan:
			return
		case <-time.After(3 * time.Second):
		}
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
		d.BaseWPM = d.WPM
		d.SpawnedAt = now.Add(-10 * time.Minute)
		d.LastEvolve = d.SpawnedAt
		d.MaxLevel = 5
	}
	return demons
}

func (m *DemonManager) runWildDemonLoop(d *WildDemon) {
	for {
		select {
		case <-m.stopSpawnChan:
			return
		default:
		}

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
		epoch := d.verseEpoch
		prevBook := d.prevBook
		prevCh := d.prevChapter
		prevV := d.prevVerse
		escapedAt := d.escapedAt
		m.mu.RUnlock()

		if hp <= 0 {
			return
		}
		if text == "" {
			select {
			case <-m.stopSpawnChan:
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}

		textRunes := []rune(text)
		totalChars := len(textRunes)

		// Optimization for 666 demons: if no human players are on this verse, advance in background ticks
		if m.hub != nil && !m.hub.HasClientsOnVerse(trans, book, ch, v) {
			// If this demon just escaped from the previous verse and a player is still finishing that previous verse,
			// hold at the start of the new verse briefly so the player can pursue it onto the next passage!
			if !escapedAt.IsZero() && time.Since(escapedAt) < 30*time.Second && prevBook != "" && m.hub.HasClientsOnVerse(trans, prevBook, prevCh, prevV) {
				select {
				case <-m.stopSpawnChan:
					return
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}

			charsPerTick := int(float64(wpm*5) * (2.0 / 60.0))
			if charsPerTick < 2 {
				charsPerTick = 2
			}
			select {
			case <-m.stopSpawnChan:
				return
			case <-time.After(2 * time.Second):
			}

			m.mu.Lock()
			if d.CurrentHP <= 0 {
				m.mu.Unlock()
				return
			}
			// If the demon's verse changed or a player arrived while sleeping, restart cleanly at char 0
			if d.verseEpoch != epoch || d.Translation != trans || d.BookName != book || d.Chapter != ch || d.Verse != v || (m.hub != nil && m.hub.HasClientsOnVerse(trans, book, ch, v)) {
				m.mu.Unlock()
				continue
			}
			d.CharIndex += charsPerTick
			if d.CharIndex > d.TaintedUpTo {
				d.TaintedUpTo = d.CharIndex
			}
			reachedEnd := d.CharIndex >= totalChars
			demonID := d.ID
			m.mu.Unlock()

			if reachedEnd {
				m.AdvanceWildDemonToNextVerse(demonID)
				select {
				case <-m.stopSpawnChan:
					return
				case <-time.After(1 * time.Second):
				}
			}
			continue
		}

		completed := true
		var demonID string
		for i := 0; i <= totalChars; i++ {
			select {
			case <-m.stopSpawnChan:
				return
			default:
			}

			var playerCharIdx, playerWPM int
			var hasActivePlayer bool
			if m.hub != nil {
				playerCharIdx, playerWPM, hasActivePlayer = m.hub.GetActivePlayerOnVerse(trans, book, ch, v)
			}

			m.mu.Lock()
			if d.CurrentHP <= 0 {
				m.mu.Unlock()
				completed = false
				break
			}
			if d.verseEpoch != epoch || d.Translation != trans || d.BookName != book || d.Chapter != ch || d.Verse != v {
				m.mu.Unlock()
				completed = false
				break
			}
			// If the player has already reached the end of the passage (completion submission in flight),
			// hold the demon at totalChars-1 so the player's finish & burst damage take precedence.
			if hasActivePlayer && playerCharIdx >= totalChars && i >= totalChars {
				if totalChars > 0 {
					d.CharIndex = totalChars - 1
				}
				m.mu.Unlock()
				completed = false
				break
			}
			d.CharIndex = i
			if i > d.TaintedUpTo {
				d.TaintedUpTo = i
			}
			taintedUpTo := d.TaintedUpTo
			if hasActivePlayer {
				baseWPM, ok := m.wildDemonBases[d.ID]
				if !ok || baseWPM <= 0 {
					baseWPM = wpm
				}
				d.BaseWPM = baseWPM
				baseWPM += d.EnrageCount * 2
				d.WPM = CalculateDynamicDemonWPM(baseWPM, playerCharIdx, i, playerWPM)
			}
			if d.DebuffStacks > 0 {
				slowFactor := 1.0 - float64(d.DebuffStacks)*0.20
				if slowFactor < 0.40 {
					slowFactor = 0.40
				}
				d.WPM = int(float64(d.WPM) * slowFactor)
			}
			curWpm := d.WPM
			minWpm := GetDemonSyncBalance().MinDemonWPM
			if curWpm < minWpm {
				curWpm = minWpm
			}
			curHP := d.CurrentHP
			maxHP := d.MaxHP
			demonID = d.ID
			dIcon := d.Icon
			dName := d.Name
			dColor := d.Color
			dRank := d.Rank
			dLevel := d.Level
			m.mu.Unlock()

			if m.hub != nil {
				m.hub.BroadcastDemonCursor(trans, book, ch, v, GhostCursor{
					UID:         demonID,
					DisplayName: dIcon + " " + dName,
					AvatarColor: dColor,
					CharIndex:   i,
					WPM:         curWpm,
					IsDemon:     true,
					DemonRank:   dRank,
					DemonLevel:  dLevel,
					CurrentHP:   curHP,
					MaxHP:       maxHP,
					TrailUpTo:   taintedUpTo,
				})
			}

			stepDelay := time.Duration(float64(time.Minute) / float64(curWpm*5))
			if stepDelay < 50*time.Millisecond {
				stepDelay = 50 * time.Millisecond
			}
			select {
			case <-m.stopSpawnChan:
				return
			case <-time.After(stepDelay):
			}
		}

		if completed && demonID != "" {
			m.AdvanceWildDemonToNextVerse(demonID)
		}

		select {
		case <-m.stopSpawnChan:
			return
		case <-time.After(3 * time.Second):
		}
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
		target.TaintedUpTo = 0
		target.verseEpoch++
		res := *target
		m.mu.Unlock()
		return &res, Verse{}, false
	}

	hasPlayerFighting := m.hub != nil && m.hub.HasClientsOnVerse(oldTrans, oldBook, oldCh, oldV)
	if hasPlayerFighting {
		// Ensure the demon lands directly on the next passage so the player can continue fighting it seamlessly.
		// If another background demon occupies nextV, relocate that other demon.
		for _, other := range m.wildDemons {
			if other.ID != target.ID && other.BookName == nextV.BookName && other.Chapter == nextV.Chapter && other.Verse == nextV.Verse {
				shiftWildDemonLocation(other)
				other.CharIndex = 0
				other.TaintedUpTo = 0
				other.verseEpoch++
			}
		}
	} else {
		// Step past occupied verses if needed (up to 10 steps)
		for step := 0; step < 10 && m.isVerseOccupiedLocked(nextV.BookName, nextV.Chapter, nextV.Verse); step++ {
			if cand, ok2 := getNextCanonicalVerse(oldTrans, nextV.BookName, nextV.Chapter, nextV.Verse); ok2 {
				nextV = cand
			} else {
				break
			}
		}
	}

	target.prevBook = oldBook
	target.prevChapter = oldCh
	target.prevVerse = oldV
	target.escapedAt = time.Now().UTC()
	target.BookName = nextV.BookName
	target.Chapter = nextV.Chapter
	target.Verse = nextV.Verse
	target.Text = cleanVerseText(nextV.Text)
	target.CharIndex = 0
	target.TaintedUpTo = 0
	target.AngelicBeacon = false
	target.verseEpoch++
	if baseWPM, ok := m.wildDemonBases[target.ID]; ok && baseWPM > 0 {
		target.BaseWPM = baseWPM
		target.WPM = baseWPM + target.EnrageCount*2
	}
	demonCopy := *target
	allDemons := m.getWildDemonsLocked()
	m.mu.Unlock()

	if m.hub != nil {
		m.hub.BroadcastDemonEscaped(oldTrans, oldBook, oldCh, oldV, demonCopy, nextV)
		m.hub.BroadcastWildDemonsList(allDemons)
	}
	return &demonCopy, nextV, true
}

// DriveWildDemonToNextVerse drives a surviving wild demon onto the user's next passage when the user finishes a passage before the demon.
func (m *DemonManager) DriveWildDemonToNextVerse(demonID string, destVerse *Verse) (*WildDemon, Verse, bool) {
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

	var nextV Verse
	var ok bool
	if destVerse != nil && destVerse.BookName != "" {
		nextV = *destVerse
		ok = true
	} else {
		nextV, ok = getNextCanonicalVerse(oldTrans, oldBook, oldCh, oldV)
	}
	if !ok {
		target.CharIndex = 0
		target.TaintedUpTo = 0
		target.verseEpoch++
		if baseWPM, okB := m.wildDemonBases[target.ID]; okB && baseWPM > 0 {
			target.BaseWPM = baseWPM
			target.WPM = baseWPM + target.EnrageCount*2
		}
		res := *target
		m.mu.Unlock()
		return &res, Verse{}, false
	}

	// If another demon occupies nextV, shift that other demon so this pursued demon stays with the user
	for _, other := range m.wildDemons {
		if other.ID != target.ID && other.BookName == nextV.BookName && other.Chapter == nextV.Chapter && other.Verse == nextV.Verse {
			shiftWildDemonLocation(other)
			other.CharIndex = 0
			other.TaintedUpTo = 0
			other.verseEpoch++
		}
	}

	target.prevBook = oldBook
	target.prevChapter = oldCh
	target.prevVerse = oldV
	target.escapedAt = time.Now().UTC()
	target.BookName = nextV.BookName
	target.Chapter = nextV.Chapter
	target.Verse = nextV.Verse
	target.Text = cleanVerseText(nextV.Text)
	target.CharIndex = 0
	target.TaintedUpTo = 0
	target.AngelicBeacon = false
	target.verseEpoch++
	if baseWPM, okB := m.wildDemonBases[target.ID]; okB && baseWPM > 0 {
		target.BaseWPM = baseWPM
		target.WPM = baseWPM + target.EnrageCount*2
	}
	demonCopy := *target
	allDemons := m.getWildDemonsLocked()
	m.mu.Unlock()

	if m.hub != nil {
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
				if d.DebuffStacks > 0 && damage > 0 {
					bonus := (damage * 25 * d.DebuffStacks) / 100
					if bonus < 1 {
						bonus = 1
					}
					damage += bonus
				}
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

// DebuffWildDemonByRighteousness applies a Sanctified Rebuke debuff to a wild demon when a player purifies its tainted letters.
// It slows the demon's WPM by 20%, increases its DebuffStacks (making it take +25% bonus damage per stack), and deals rebuke damage.
func (m *DemonManager) DebuffWildDemonByRighteousness(demonID string, rebukeDamage int) (*WildDemon, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	minWpm := GetDemonSyncBalance().MinDemonWPM
	if minWpm < 6 {
		minWpm = 6
	}

	for idx, d := range m.wildDemons {
		if d.ID == demonID && d.CurrentHP > 0 {
			d.Debuffed = true
			d.DebuffStacks++
			d.WPM = int(float64(d.WPM) * 0.80)
			if d.WPM < minWpm {
				d.WPM = minWpm
			}
			if base, ok := m.wildDemonBases[d.ID]; ok && base > minWpm {
				newBase := int(float64(base) * 0.85)
				if newBase < minWpm {
					newBase = minWpm
				}
				m.wildDemonBases[d.ID] = newBase
				d.BaseWPM = newBase
			}
			if rebukeDamage > 0 {
				d.CurrentHP -= rebukeDamage
				if d.CurrentHP <= 0 {
					d.CurrentHP = 0
					res := *d
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

// SkirmishWildDemonByGuardian allows an autonomous Guardian Angel ghost to deal chip damage (never killing, leaving >= 1 HP)
// and mark an AngelicBeacon on a wild demon occupying the same verse.
func (m *DemonManager) SkirmishWildDemonByGuardian(trans, book string, chapter, verse, chipDamage int) *WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, d := range m.wildDemons {
		if d.Translation == trans && d.BookName == book && d.Chapter == chapter && d.Verse == verse && d.CurrentHP > 0 {
			d.AngelicBeacon = true
			if chipDamage > 0 && d.CurrentHP > 1 {
				d.CurrentHP -= chipDamage
				if d.CurrentHP < 1 {
					d.CurrentHP = 1
				}
			}
			res := *d
			return &res
		}
	}
	return nil
}

// AdjustWildDemonPaceForPlayer updates the wild demon's WPM on the given verse when a player's cursor moves.
func (m *DemonManager) AdjustWildDemonPaceForPlayer(trans, book string, chapter, verse, userCharIdx, userWPM int) *WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, d := range m.wildDemons {
		if d.Translation == trans && d.BookName == book && d.Chapter == chapter && d.Verse == verse && d.CurrentHP > 0 {
			baseWPM, ok := m.wildDemonBases[d.ID]
			if !ok || baseWPM <= 0 {
				baseWPM = d.WPM
				m.wildDemonBases[d.ID] = baseWPM
			}
			d.BaseWPM = baseWPM
			baseWPM += d.EnrageCount * 2
			d.WPM = CalculateDynamicDemonWPM(baseWPM, userCharIdx, d.CharIndex, userWPM)
			if d.DebuffStacks > 0 {
				slowFactor := 1.0 - float64(d.DebuffStacks)*0.20
				if slowFactor < 0.40 {
					slowFactor = 0.40
				}
				d.WPM = int(float64(d.WPM) * slowFactor)
				minWpm := GetDemonSyncBalance().MinDemonWPM
				if d.WPM < minWpm {
					d.WPM = minWpm
				}
			}
			res := *d
			return &res
		}
	}
	return nil
}

func (m *DemonManager) EnrageWildDemonOnMistake(demonID string) *WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, d := range m.wildDemons {
		if d.ID == demonID {
			healPct := GetDemonSyncBalance().BacklashHealPct
			if healPct <= 0 {
				healPct = 0.10
			}
			heal := int(float64(d.MaxHP) * healPct)
			if heal < 5 {
				heal = 5
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
			d.TaintedUpTo = 0
			d.Debuffed = false
			d.DebuffStacks = 0
			d.AngelicBeacon = false
			d.verseEpoch++
			if base, ok := m.wildDemonBases[d.ID]; ok {
				d.BaseWPM = base
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
		d.TaintedUpTo = 0
		d.Debuffed = false
		d.DebuffStacks = 0
		d.AngelicBeacon = false
		d.verseEpoch++
		if base, ok := m.wildDemonBases[d.ID]; ok {
			d.BaseWPM = base
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
	if n < 1 {
		n = 666
	}
	m.maxWildDemons = n
}

func (m *DemonManager) GetMaxWildDemons() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.maxWildDemons <= 0 {
		return 666
	}
	return m.maxWildDemons
}

func (m *DemonManager) SetEvolveInterval(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evolveInterval = d
}

func (m *DemonManager) Stop() {
	m.StopDevil24_7()

	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stopSpawnChan:
	default:
		close(m.stopSpawnChan)
	}
	for uid, bot := range m.activeDemons {
		bot.mu.Lock()
		if bot.IsRunning {
			bot.IsRunning = false
			close(bot.StopChan)
		}
		bot.mu.Unlock()
		delete(m.activeDemons, uid)
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
	override := m.spawnInterval
	count := len(m.wildDemons)
	m.mu.RUnlock()

	if override > 0 && override != 150*time.Second {
		return override
	}
	if adm := getGlobalAdmin(); adm != nil {
		opts := adm.GetOptions()
		if opts.SpawnIntervalOverrideSec > 0 {
			return time.Duration(opts.SpawnIntervalOverrideSec) * time.Second
		}
	}

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
				maxCap := m.maxWildDemons
				m.mu.Unlock()

				if count < maxCap {
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
			if base, ok := m.wildDemonBases[d.ID]; ok && base < 24 {
				m.wildDemonBases[d.ID] = base + 1
				d.BaseWPM = base + 1
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
		maxDemons := m.GetMaxWildDemons()
		sanct := int((1.0 - float64(activeCount)/float64(maxDemons)) * 100)
		if sanct < 0 {
			sanct = 0
		}
		themeLabel := targetTheme
		if themeLabel == "" {
			themeLabel = "the Holy Scriptures"
		}
		msg := fmt.Sprintf("🔥 DAILY MINI-SWARM! A surge of %d adversaries has breached %s! Rally the fellowship!", spawned, themeLabel)
		m.hub.BroadcastSwarmEvent(msg, activeCount, maxDemons, sanct)
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
		maxDemons := m.GetMaxWildDemons()
		sanct := int((1.0 - float64(activeCount)/float64(maxDemons)) * 100)
		if sanct < 0 {
			sanct = 0
		}
		msg := fmt.Sprintf("⚡ SUNDAY MAJOR SWARM! The Sabbath Incursion has begun! %d ancient adversaries have breached sacred scripture! Unite to defend the Word!", spawned)
		m.hub.BroadcastSwarmEvent(msg, activeCount, maxDemons, sanct)
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
	allTypes := GetDemonTypes()
	var weightedPool []DemonInfo
	for _, dt := range allTypes {
		w := dt.SpawnWeight
		if w <= 0 && isBuiltInDemonRank(dt.Rank) {
			w = 1
		}
		for i := 0; i < w; i++ {
			weightedPool = append(weightedPool, dt)
		}
	}
	var chosen DemonInfo
	if len(weightedPool) > 0 {
		chosen = weightedPool[rand.Intn(len(weightedPool))]
	} else if len(allTypes) > 0 {
		chosen = allTypes[rand.Intn(len(allTypes))]
	} else {
		chosen = DemonInfo{Rank: "imp", Name: "Whispering Imp", Title: "Tempter of the Void", Icon: "😈", Color: "#ef4444", Level: 1, MaxLevel: 5, WildMinWPM: 10, WildMaxWPM: 22, WildBaseHP: 100, WildHPPerLevel: 60}
	}

	level := chosen.Level
	if level < 1 {
		level = 1
	}
	switch chosen.Rank {
	case "tormentor":
		if level < 2 {
			level = 2
		}
	case "apollyon":
		if level < 3 {
			level = 3
		}
	}
	maxLvl := chosen.MaxLevel
	if maxLvl < 1 {
		maxLvl = 5
	}

	icon := chosen.Icon
	color := chosen.Color
	title := chosen.Title
	name := fmt.Sprintf("%s of %s", chosen.Name, book)
	switch chosen.Rank {
	case "imp":
		if chosen.Name == "Mischievous Imp" {
			name = fmt.Sprintf("Whispering Imp of %s", book)
			title = "Tempter of the Void"
		}
	case "fiend":
		if chosen.Name == "Shadow Fiend" {
			name = fmt.Sprintf("Brimstone Fiend of %s", book)
		}
	case "tormentor":
		if chosen.Name == "Brimstone Tormentor" {
			name = fmt.Sprintf("Tormentor of %s", book)
		}
	case "apollyon":
		if chosen.Name == "Apollyon the Destroyer" {
			name = fmt.Sprintf("Apollyon Harbinger of %s", book)
			title = "Harbinger of Abaddon"
		}
	}

	minW := chosen.WildMinWPM
	if minW < 4 {
		minW = 10
	}
	maxW := chosen.WildMaxWPM
	if maxW < minW {
		maxW = minW
	}
	wpm := minW
	if maxW > minW {
		wpm = minW + rand.Intn(maxW-minW+1)
	}

	baseHP := chosen.WildBaseHP
	if baseHP < 10 {
		baseHP = 100
	}
	hpPerLvl := chosen.WildHPPerLevel
	if hpPerLvl <= 0 {
		hpPerLvl = 60
	}
	hp := baseHP + level*hpPerLvl

	id := fmt.Sprintf("wild-interval-%d", time.Now().UnixNano()%1000000)

	demon := &WildDemon{
		ID:          id,
		Rank:        chosen.Rank,
		Level:       level,
		MaxLevel:    maxLvl,
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
		BaseWPM:     wpm,
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
	peacefulVerses := 2
	baseChance := 0.04
	maxChance := 0.50
	if adm := getGlobalAdmin(); adm != nil {
		opts := adm.GetOptions()
		if opts.EncounterPeacefulIntroVerses >= 0 {
			peacefulVerses = opts.EncounterPeacefulIntroVerses
		}
		if opts.EncounterBaseChance > 0 {
			baseChance = opts.EncounterBaseChance
		}
		if opts.EncounterMaxChance > 0 {
			maxChance = opts.EncounterMaxChance
		}
	}

	if totalVerses <= 0 || completedVerses < peacefulVerses {
		return 0.0 // Peaceful opening for the first N verses of any book
	}

	ratio := float64(completedVerses) / float64(totalVerses)
	if ratio > 1.0 {
		ratio = 1.0
	}

	verseBonus := float64(completedVerses) * 0.005
	if verseBonus > 0.20 {
		verseBonus = 0.20
	}

	chance := baseChance + (ratio * 0.30) + verseBonus
	if chance > maxChance {
		chance = maxChance
	}
	if chance < baseChance {
		chance = baseChance
	}
	return chance
}

// CheckOrSpawnWildDemonForUser evaluates demon presence and progress-based encounter chance on verse arrival
func (m *DemonManager) CheckOrSpawnWildDemonForUser(trans string, v Verse, currentVerseIdx, totalVerses int) (*WildDemon, float64, bool) {
	// Calculate encounter chance before locking DemonManager to avoid lock-order inversion with AdminManager
	chance := CalculateEncounterChance(currentVerseIdx, totalVerses)

	m.mu.Lock()

	// 1. Check if a wild demon is already roaming on this verse (lives until defeated)
	for _, d := range m.wildDemons {
		if d.Translation == trans && d.BookName == v.BookName && d.Chapter == v.Chapter && d.Verse == v.Verse {
			// Reset CharIndex to 0 and restore base WPM when a user arrives on the verse so the duel starts cleanly
			d.CharIndex = 0
			d.verseEpoch++
			if baseWPM, ok := m.wildDemonBases[d.ID]; ok && baseWPM > 0 {
				d.BaseWPM = baseWPM
				d.WPM = baseWPM + d.EnrageCount*2
			}
			res := *d
			m.mu.Unlock()
			return &res, 1.0, false
		}
	}

	// 2. Check encounter chance based on verses completed in this book
	if chance <= 0.0 {
		m.mu.Unlock()
		return nil, 0.0, false
	}

	// 3. Pool capacity check
	if len(m.wildDemons) >= m.maxWildDemons {
		m.mu.Unlock()
		return nil, chance, false
	}

	// 4. Roll for dynamic encounter
	if rand.Float64() < chance {
		demon := m.createDemonForProgress(trans, v, float64(currentVerseIdx)/float64(totalVerses))
		m.wildDemons = append(m.wildDemons, demon)
		m.wildDemonBases[demon.ID] = demon.WPM
		go m.runWildDemonLoop(demon)

		allDemons := m.getWildDemonsLocked()
		res := *demon
		m.mu.Unlock()

		if m.hub != nil {
			m.hub.BroadcastWildDemonsList(allDemons)
		}
		return &res, chance, true
	}

	m.mu.Unlock()
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
	allTypes := GetDemonTypes()
	var candidates []DemonInfo
	for _, dt := range allTypes {
		if dt.SpawnWeight <= 0 && !isBuiltInDemonRank(dt.Rank) {
			continue
		}
		minR := dt.MinProgressRatio
		maxR := dt.MaxProgressRatio
		if maxR <= 0 {
			maxR = 1.0
		}
		if progressRatio >= minR && progressRatio <= maxR {
			candidates = append(candidates, dt)
		}
	}

	var chosen DemonInfo
	if len(candidates) > 0 {
		chosen = candidates[rand.Intn(len(candidates))]
	} else {
		var fallbackRank string
		if progressRatio < 0.25 {
			fallbackRank = "imp"
		} else if progressRatio < 0.60 {
			fallbackRank = "fiend"
		} else if progressRatio < 0.85 {
			fallbackRank = "tormentor"
		} else {
			fallbackRank = "apollyon"
		}
		if dt, ok := GetDemonType(fallbackRank); ok {
			chosen = dt
		} else if len(allTypes) > 0 {
			chosen = allTypes[0]
		}
	}

	rank := chosen.Rank
	level := chosen.Level
	if level < 1 {
		level = 1
	}
	switch rank {
	case "fiend":
		if level < 2 {
			level = 2
		}
	case "tormentor":
		if level < 3 {
			level = 3
		}
	case "apollyon":
		if level < 4 {
			level = 4
		}
	}

	maxLvl := chosen.MaxLevel
	if maxLvl < 1 {
		maxLvl = 5
	}

	minW := chosen.WildMinWPM
	if minW < 4 {
		minW = 10
	}
	maxW := chosen.WildMaxWPM
	if maxW < minW {
		maxW = minW
	}
	wpm := minW
	if maxW > minW {
		wpm = minW + rand.Intn(maxW-minW+1)
	}

	baseHP := chosen.WildBaseHP
	if baseHP < 10 {
		baseHP = 120
	}
	hpPerLvl := chosen.WildHPPerLevel
	if hpPerLvl <= 0 {
		hpPerLvl = 60
	}
	hp := baseHP + level*hpPerLvl

	name := fmt.Sprintf("%s of %s", chosen.Name, v.BookName)
	id := fmt.Sprintf("wild-ambush-%d", time.Now().UnixNano()%1000000)

	now := time.Now().UTC()
	return &WildDemon{
		ID:          id,
		Rank:        rank,
		Level:       level,
		MaxLevel:    maxLvl,
		Name:        name,
		Title:       chosen.Title,
		Icon:        chosen.Icon,
		Color:       chosen.Color,
		Translation: trans,
		BookName:    v.BookName,
		Chapter:     v.Chapter,
		Verse:       v.Verse,
		Text:        cleanVerseText(v.Text),
		WPM:         wpm,
		BaseWPM:     wpm,
		MaxHP:       hp,
		CurrentHP:   hp,
		EnrageCount: 0,
		CharIndex:   0,
		SpawnedAt:   now,
		LastEvolve:  now,
		AgeMinutes:  0,
	}
}

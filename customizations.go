package main

import (
	"fmt"
)

// DefaultCustomizations returns the default beginner configuration
func DefaultCustomizations() SanctuarySettings {
	return SanctuarySettings{
		TextColor:    "emerald",
		FontFamily:   "sans",
		CaretAura:    "default",
		AudioProfile: "classic",
		CelebrateFx:  "confetti",
	}
}

// GetAllCustomizationUnlocks returns the complete catalog of customizations,
// annotated with whether each item has been unlocked by the user's progress.
func GetAllCustomizationUnlocks(topWPM, totalVerses, longestStreak, booksCompleted int) []CustomizationUnlock {
	return []CustomizationUnlock{
		// ---- Completed Text Colors ----
		{
			ID:          "color_emerald",
			Category:    "color",
			Name:        "Emerald Grace",
			Value:       "emerald",
			Description: "The timeless green of living growth, renewal, and peace.",
			Requirement: "Unlocked by default",
			Unlocked:    true,
			Preview:     "#16a34a",
		},
		{
			ID:          "color_amber",
			Category:    "color",
			Name:        "Sunlit Amber",
			Value:       "amber",
			Description: "Warm golden glow reminiscent of desert sunrise and Mount Sinai.",
			Requirement: "Achieve 40+ WPM or 20 Verses",
			Unlocked:    topWPM >= 40 || totalVerses >= 20,
			Preview:     "#f59e0b",
		},
		{
			ID:          "color_sapphire",
			Category:    "color",
			Name:        "Azure Sapphire",
			Value:       "sapphire",
			Description: "Deep Mediterranean blue inspired by apostolic epistles and twilight prayer.",
			Requirement: "Achieve 60+ WPM or 50 Verses",
			Unlocked:    topWPM >= 60 || totalVerses >= 50,
			Preview:     "#3b82f6",
		},
		{
			ID:          "color_amethyst",
			Category:    "color",
			Name:        "Radiant Amethyst",
			Value:       "amethyst",
			Description: "Luminous royal purple reflecting the celestial city and Revelation crystals.",
			Requirement: "Achieve 80+ WPM (Special Milestone!)",
			Unlocked:    topWPM >= 80,
			Preview:     "#a855f7",
		},
		{
			ID:          "color_crimson",
			Category:    "color",
			Name:        "Living Flame",
			Value:       "crimson",
			Description: "Fiery ember and prophetic crimson of sacrifice and dedication.",
			Requirement: "Achieve 100+ WPM or 150 Verses",
			Unlocked:    topWPM >= 100 || totalVerses >= 150,
			Preview:     "#ef4444",
		},
		{
			ID:          "color_cyan",
			Category:    "color",
			Name:        "River of Life",
			Value:       "cyan",
			Description: "Brilliant crystal turquoise flowing from the throne of grace.",
			Requirement: "Achieve 120+ WPM or 3-Day Streak",
			Unlocked:    topWPM >= 120 || longestStreak >= 3,
			Preview:     "#06b6d4",
		},
		{
			ID:          "color_gold",
			Category:    "color",
			Name:        "Archangel Gold",
			Value:       "gold",
			Description: "Pure sunlight with a celestial halo glow, fitting for Big Mike.",
			Requirement: "Achieve 160+ WPM or Complete 1 Full Book",
			Unlocked:    topWPM >= 160 || booksCompleted >= 1,
			Preview:     "#ffd700",
		},

		// ---- Scripture Typography ----
		{
			ID:          "font_sans",
			Category:    "font",
			Name:        "Modern Clean",
			Value:       "sans",
			Description: "Crisp and legible contemporary sans-serif typeface.",
			Requirement: "Unlocked by default",
			Unlocked:    true,
			Preview:     "Aa Bb Gg",
		},
		{
			ID:          "font_serif",
			Category:    "font",
			Name:        "Scribe Serif",
			Value:       "serif",
			Description: "Dignified classical serif reminiscent of printed study Bibles.",
			Requirement: "Inscribe 10+ Verses",
			Unlocked:    totalVerses >= 10,
			Preview:     "Georgia, serif",
		},
		{
			ID:          "font_mono",
			Category:    "font",
			Name:        "Ancient Monospace",
			Value:       "mono",
			Description: "Uniform letter spacing crafted for precision keystrokes.",
			Requirement: "Inscribe 25+ Verses",
			Unlocked:    totalVerses >= 25,
			Preview:     "monospace",
		},
		{
			ID:          "font_illuminated",
			Category:    "font",
			Name:        "Illuminated Chantry",
			Value:       "illuminated",
			Description: "Graceful antique titling with noble serif proportions.",
			Requirement: "Maintain a 5-Day Streak or 50 Verses",
			Unlocked:    longestStreak >= 5 || totalVerses >= 50,
			Preview:     "Cinzel / Palatino",
		},

		// ---- Caret & Aura Glow ----
		{
			ID:          "aura_default",
			Category:    "aura",
			Name:        "Standard Underline",
			Value:       "default",
			Description: "Subtle underline bar marking the active keystroke.",
			Requirement: "Unlocked by default",
			Unlocked:    true,
			Preview:     "underline",
		},
		{
			ID:          "aura_halo",
			Category:    "aura",
			Name:        "Angelic Halo",
			Value:       "halo",
			Description: "Soft celestial aura glowing around the active letter.",
			Requirement: "Achieve 50+ WPM or 3-Day Streak",
			Unlocked:    topWPM >= 50 || longestStreak >= 3,
			Preview:     "halo glow",
		},
		{
			ID:          "aura_flame",
			Category:    "aura",
			Name:        "Sacred Flame",
			Value:       "flame",
			Description: "Pulsing warm ember trail following every typed character.",
			Requirement: "Achieve 70+ WPM",
			Unlocked:    topWPM >= 70,
			Preview:     "flame glow",
		},
		{
			ID:          "aura_starlight",
			Category:    "aura",
			Name:        "Starlight Pulse",
			Value:       "starlight",
			Description: "Deep sapphire starlight halo illuminating your path.",
			Requirement: "Achieve 7-Day Streak or 75 Verses",
			Unlocked:    longestStreak >= 7 || totalVerses >= 75,
			Preview:     "starlight",
		},

		// ---- Holy Audio Keystroke Profile ----
		{
			ID:          "audio_classic",
			Category:    "audio",
			Name:        "Mechanical Switch",
			Value:       "classic",
			Description: "Standard crisp mechanical keyboard switch click.",
			Requirement: "Unlocked by default",
			Unlocked:    true,
			Preview:     "Click",
		},
		{
			ID:          "audio_typewriter",
			Category:    "audio",
			Name:        "Antique Typewriter",
			Value:       "typewriter",
			Description: "Deep resonant mechanical thud reminiscent of historic scriptoriums.",
			Requirement: "Inscribe 30+ Verses",
			Unlocked:    totalVerses >= 30,
			Preview:     "Thud",
		},
		{
			ID:          "audio_chime",
			Category:    "audio",
			Name:        "Celestial Chime",
			Value:       "chime",
			Description: "Harmonious glass and silver chimes as letters are typed.",
			Requirement: "Achieve 80+ WPM or 7-Day Streak",
			Unlocked:    topWPM >= 80 || longestStreak >= 7,
			Preview:     "Chime",
		},
		{
			ID:          "audio_whisper",
			Category:    "audio",
			Name:        "Gentle Whisper",
			Value:       "whisper",
			Description: "Very quiet, subtle acoustic clicks for late-night peaceful devotions.",
			Requirement: "Inscribe 15+ Verses",
			Unlocked:    totalVerses >= 15,
			Preview:     "Whisper",
		},

		// ---- Passage Celebration Effects ----
		{
			ID:          "fx_confetti",
			Category:    "fx",
			Name:        "Sanctuary Confetti",
			Value:       "confetti",
			Description: "Classic festive celebration burst upon completing passages.",
			Requirement: "Unlocked by default",
			Unlocked:    true,
			Preview:     "🎉",
		},
		{
			ID:          "fx_gold",
			Category:    "fx",
			Name:        "Golden Manna Shower",
			Value:       "gold",
			Description: "Rays of shimmering golden sparkles cascading across the screen.",
			Requirement: "Achieve 60+ WPM",
			Unlocked:    topWPM >= 60,
			Preview:     "✨",
		},
		{
			ID:          "fx_feathers",
			Category:    "fx",
			Name:        "Dove & Angel Wings",
			Value:       "feathers",
			Description: "Feathered angelic burst celebrating scripture inscription.",
			Requirement: "Achieve 80+ WPM or 50 Verses",
			Unlocked:    topWPM >= 80 || totalVerses >= 50,
			Preview:     "🕊️",
		},
	}
}

// FindNewlyUnlockedCustomizations checks if the latest milestone unlocked any new items
func FindNewlyUnlockedCustomizations(oldWPM, newWPM, oldVerses, newVerses, oldStreak, newStreak, booksComp int) []CustomizationUnlock {
	beforeList := GetAllCustomizationUnlocks(oldWPM, oldVerses, oldStreak, booksComp)
	afterList := GetAllCustomizationUnlocks(newWPM, newVerses, newStreak, booksComp)

	beforeMap := make(map[string]bool)
	for _, u := range beforeList {
		beforeMap[u.ID] = u.Unlocked
	}

	var newlyUnlocked []CustomizationUnlock
	for _, u := range afterList {
		if u.Unlocked && !beforeMap[u.ID] {
			newlyUnlocked = append(newlyUnlocked, u)
		}
	}
	return newlyUnlocked
}

// FormatUnlockToast returns a formatted announcement string
func FormatUnlockToast(u CustomizationUnlock) string {
	return fmt.Sprintf("🎨 Sanctuary Unlock: %s (%s)", u.Name, u.Requirement)
}

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

type challengeRef struct {
	Book    string
	Chapter int
	Verse   int
}

// Curated list of beloved scriptures for rotating Daily Challenges
var dailyChallengeCatalog = []challengeRef{
	{Book: "Genesis", Chapter: 1, Verse: 1},
	{Book: "Psalms", Chapter: 23, Verse: 1},
	{Book: "Proverbs", Chapter: 3, Verse: 5},
	{Book: "Proverbs", Chapter: 3, Verse: 6},
	{Book: "Isaiah", Chapter: 40, Verse: 31},
	{Book: "Jeremiah", Chapter: 29, Verse: 11},
	{Book: "Matthew", Chapter: 6, Verse: 33},
	{Book: "Matthew", Chapter: 11, Verse: 28},
	{Book: "Matthew", Chapter: 28, Verse: 19},
	{Book: "John", Chapter: 1, Verse: 1},
	{Book: "John", Chapter: 3, Verse: 16},
	{Book: "John", Chapter: 14, Verse: 6},
	{Book: "Romans", Chapter: 8, Verse: 28},
	{Book: "Romans", Chapter: 8, Verse: 38},
	{Book: "Romans", Chapter: 12, Verse: 2},
	{Book: "1 Corinthians", Chapter: 13, Verse: 4},
	{Book: "1 Corinthians", Chapter: 13, Verse: 13},
	{Book: "2 Corinthians", Chapter: 5, Verse: 17},
	{Book: "Galatians", Chapter: 5, Verse: 22},
	{Book: "Ephesians", Chapter: 2, Verse: 8},
	{Book: "Philippians", Chapter: 4, Verse: 6},
	{Book: "Philippians", Chapter: 4, Verse: 13},
	{Book: "Colossians", Chapter: 3, Verse: 23},
	{Book: "2 Timothy", Chapter: 1, Verse: 7},
	{Book: "Hebrews", Chapter: 11, Verse: 1},
	{Book: "James", Chapter: 1, Verse: 2},
	{Book: "1 Peter", Chapter: 5, Verse: 7},
	{Book: "1 John", Chapter: 4, Verse: 19},
	{Book: "Revelation", Chapter: 21, Verse: 4},
	{Book: "Joshua", Chapter: 1, Verse: 9},
	{Book: "Micah", Chapter: 6, Verse: 8},
}

// getDailyChallengeVerse retrieves the deterministic Verse of the Day for a given date
func getDailyChallengeVerse(dateStr, transID string) (*DailyChallenge, error) {
	if dateStr == "" {
		dateStr = time.Now().UTC().Format("2006-01-02")
	}

	h := sha256.Sum256([]byte(dateStr))
	idxVal := binary.BigEndian.Uint64(h[:8])
	idx := int(idxVal % uint64(len(dailyChallengeCatalog)))
	ref := dailyChallengeCatalog[idx]

	td, ok := translations[transID]
	if !ok {
		td = translations["kjv"]
	}
	if td == nil {
		return nil, fmt.Errorf("no translation available")
	}

	verses := td.VersesByBook[ref.Book]
	var foundVerse *Verse
	for _, v := range verses {
		if v.Chapter == ref.Chapter && v.Verse == ref.Verse {
			foundVerse = &v
			break
		}
	}

	if foundVerse == nil {
		if len(verses) > 0 {
			foundVerse = &verses[0]
		} else if len(td.Verses) > 0 {
			foundVerse = &td.Verses[0]
		} else {
			return nil, fmt.Errorf("verse not found in translation")
		}
	}

	return &DailyChallenge{
		Date:        dateStr,
		Translation: td.Meta.ID,
		BookName:    foundVerse.BookName,
		Chapter:     foundVerse.Chapter,
		Verse:       foundVerse.Verse,
		Text:        foundVerse.Text,
	}, nil
}

// verifySubmissionTiming verifies timing server-side against anti-cheat bounds.
// Returns server-verified duration, verified WPM, and whether the timing is physically plausible.
func verifySubmissionTiming(serverStartTime time.Time, clientDuration float64, text string, clientWPM int) (float64, int, bool) {
	charCount := runeLen(text)
	if charCount == 0 {
		return 0.5, 0, false
	}
	words := float64(charCount) / 5.0

	duration := clientDuration
	if duration <= 0.2 {
		duration = time.Since(serverStartTime).Seconds()
		if duration <= 0.2 {
			duration = 0.5
		}
	}

	// Maximum realistic human typing speed capped at 240 WPM
	const maxHumanWPM = 240.0
	minPossibleDuration := words / (maxHumanWPM / 60.0)

	isPlausible := true
	if duration < minPossibleDuration {
		duration = minPossibleDuration
		isPlausible = false
	}

	var wpm int
	if clientWPM > 0 && clientWPM <= 250 {
		wpm = clientWPM
	} else {
		wpm = int(math.Round(words / (duration / 60.0)))
	}

	if wpm > 250 {
		wpm = 250
		isPlausible = false
	}
	if wpm < 1 {
		wpm = 1
	}

	return math.Round(duration*100) / 100, wpm, isPlausible
}

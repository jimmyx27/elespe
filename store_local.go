package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type localSession struct {
	UID         string       `json:"uid"`
	Translation string       `json:"translation"`
	BookName    string       `json:"bookName"`
	Stats       RuntimeStats `json:"stats"`
	Duration    float64      `json:"duration,omitempty"`
	CreatedAt   time.Time    `json:"createdAt,omitempty"`
}

type localData struct {
	Progress               map[string]map[string]BookProgress `json:"progress"` // uid -> key -> BookProgress
	Sessions               map[int]*localSession              `json:"sessions"`
	NextSession            int                                `json:"nextSession"`
	Favorites              map[string][]Favorite              `json:"favorites"`
	Streaks                map[string]*UserStreak             `json:"streaks"`
	Badges                 map[string][]Badge                 `json:"badges"`
	Accounts               map[string]*UserAccount            `json:"accounts"` // uid -> UserAccount
	NextCommentID          int                                `json:"nextCommentID"`
	Comments               []VerseComment                     `json:"comments"`
	NextChatID             int                                `json:"nextChatID"`
	Chat                   []ChatMessage                      `json:"chat"`
	NextRaceID             int                                `json:"nextRaceID"`
	Races                  []RaceResult                       `json:"races"`
	UserVerseCompletions   map[string]map[string]int          `json:"user_verse_completions"`   // uid -> key -> count
	GlobalVerseCompletions map[string]int                     `json:"global_verse_completions"` // key -> count
	SanctuarySettings      map[string]SanctuarySettings       `json:"sanctuary_settings"`       // uid -> settings
	BossRaid               *BossRaid                          `json:"boss_raid,omitempty"`
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
			Progress:               make(map[string]map[string]BookProgress),
			Sessions:               make(map[int]*localSession),
			Favorites:              make(map[string][]Favorite),
			Streaks:                make(map[string]*UserStreak),
			Badges:                 make(map[string][]Badge),
			Accounts:               make(map[string]*UserAccount),
			Comments:               make([]VerseComment, 0),
			Chat:                   make([]ChatMessage, 0),
			Races:                  make([]RaceResult, 0),
			UserVerseCompletions:   make(map[string]map[string]int),
			GlobalVerseCompletions: make(map[string]int),
			SanctuarySettings:      make(map[string]SanctuarySettings),
			BossRaid:               defaultBossRaid(),
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
			Progress:               make(map[string]map[string]BookProgress),
			Sessions:               make(map[int]*localSession),
			Favorites:              make(map[string][]Favorite),
			Streaks:                make(map[string]*UserStreak),
			Badges:                 make(map[string][]Badge),
			Accounts:               make(map[string]*UserAccount),
			Comments:               make([]VerseComment, 0),
			Chat:                   make([]ChatMessage, 0),
			Races:                  make([]RaceResult, 0),
			UserVerseCompletions:   make(map[string]map[string]int),
			GlobalVerseCompletions: make(map[string]int),
			SanctuarySettings:      make(map[string]SanctuarySettings),
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
	if s.data.Accounts == nil {
		s.data.Accounts = make(map[string]*UserAccount)
	}
	if s.data.Comments == nil {
		s.data.Comments = make([]VerseComment, 0)
	}
	if s.data.Chat == nil {
		s.data.Chat = make([]ChatMessage, 0)
	}
	if s.data.Races == nil {
		s.data.Races = make([]RaceResult, 0)
	}
	if s.data.UserVerseCompletions == nil {
		s.data.UserVerseCompletions = make(map[string]map[string]int)
	}
	if s.data.GlobalVerseCompletions == nil {
		s.data.GlobalVerseCompletions = make(map[string]int)
	}
	if s.data.SanctuarySettings == nil {
		s.data.SanctuarySettings = make(map[string]SanctuarySettings)
	}
	if s.data.BossRaid == nil {
		s.data.BossRaid = defaultBossRaid()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Accounts[uid]; !ok {
		s.data.Accounts[uid] = &UserAccount{
			UID:         uid,
			Username:    "guest_" + uid[:min(len(uid), 6)],
			DisplayName: "Typist " + uid[:min(len(uid), 4)],
			AvatarColor: "#6366f1",
			CreatedAt:   time.Now().UTC(),
			LastActive:  time.Now().UTC(),
		}
		return s.save()
	}
	s.data.Accounts[uid].LastActive = time.Now().UTC()
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
		CreatedAt:   time.Now().UTC(),
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

func (s *localStore) RecordTypingSession(ctx context.Context, uid, translation, bookName string, chars, correct, wpm int, duration float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.NextSession++
	id := s.data.NextSession
	s.data.Sessions[id] = &localSession{
		UID:         uid,
		Translation: translation,
		BookName:    bookName,
		Stats: RuntimeStats{
			CharsTyped:   chars,
			CorrectChars: correct,
			WPM:          wpm,
		},
		Duration:  duration,
		CreatedAt: time.Now().UTC(),
	}
	return s.save()
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
			return nil
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
			return false, nil
		}
	}
	s.data.Badges[uid] = append(s.data.Badges[uid], badge)
	return true, s.save()
}

// ---- Accounts & Identity Implementation ----

func (s *localStore) RegisterAccount(ctx context.Context, username, email, passwordHash, displayName, avatarColor, anonUID string) (*UserAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username cannot be empty")
	}

	// Check existing username
	for _, acc := range s.data.Accounts {
		if strings.EqualFold(acc.Username, username) {
			return nil, errors.New("username already taken")
		}
	}

	if displayName == "" {
		displayName = username
	}
	if avatarColor == "" {
		avatarColor = "#8b5cf6"
	}

	accountUID := "acc_" + username
	acc := &UserAccount{
		UID:          accountUID,
		Username:     username,
		DisplayName:  displayName,
		Email:        email,
		PasswordHash: passwordHash,
		AvatarColor:  avatarColor,
		CreatedAt:    time.Now().UTC(),
		LastActive:   time.Now().UTC(),
	}
	s.data.Accounts[accountUID] = acc

	// Claim/merge progress from anonymous UID if present
	if anonUID != "" && anonUID != accountUID {
		s.mergeUserProgressLocked(accountUID, anonUID)
	}

	if err := s.save(); err != nil {
		return nil, err
	}
	return acc, nil
}

func (s *localStore) LoginAccount(ctx context.Context, usernameOrEmail, passwordHash, anonUID string) (*UserAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.TrimSpace(usernameOrEmail)
	var found *UserAccount
	for _, acc := range s.data.Accounts {
		if strings.EqualFold(acc.Username, key) || (acc.Email != "" && strings.EqualFold(acc.Email, key)) {
			if acc.PasswordHash == passwordHash {
				found = acc
				break
			}
			return nil, errors.New("invalid password")
		}
	}

	if found == nil {
		return nil, errors.New("account not found")
	}

	found.LastActive = time.Now().UTC()

	// Merge progress non-destructively
	if anonUID != "" && anonUID != found.UID {
		s.mergeUserProgressLocked(found.UID, anonUID)
	}

	if err := s.save(); err != nil {
		return nil, err
	}
	return found, nil
}

func (s *localStore) GetAccount(ctx context.Context, uid string) (*UserAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, ok := s.data.Accounts[uid]
	if !ok {
		return nil, errors.New("account not found")
	}
	res := *acc
	return &res, nil
}

func (s *localStore) UpdateProfile(ctx context.Context, uid, displayName, avatarColor string) (*UserAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	acc, ok := s.data.Accounts[uid]
	if !ok {
		acc = &UserAccount{
			UID:         uid,
			Username:    "user_" + uid[:min(len(uid), 6)],
			CreatedAt:   time.Now().UTC(),
			LastActive:  time.Now().UTC(),
		}
		s.data.Accounts[uid] = acc
	}
	if displayName != "" {
		acc.DisplayName = displayName
	}
	if avatarColor != "" {
		acc.AvatarColor = avatarColor
	}
	acc.LastActive = time.Now().UTC()
	if err := s.save(); err != nil {
		return nil, err
	}
	res := *acc
	return &res, nil
}

func (s *localStore) MergeUserProgress(ctx context.Context, targetUID, sourceUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mergeUserProgressLocked(targetUID, sourceUID)
	return s.save()
}

// mergeUserProgressLocked performs a robust, non-destructive merge of all progress,
// favorites, streaks, badges, and history into targetUID.
func (s *localStore) mergeUserProgressLocked(targetUID, sourceUID string) {
	if targetUID == sourceUID || sourceUID == "" {
		return
	}

	// 1. Merge Book Progress
	if srcProg, ok := s.data.Progress[sourceUID]; ok {
		if s.data.Progress[targetUID] == nil {
			s.data.Progress[targetUID] = make(map[string]BookProgress)
		}
		tgtProg := s.data.Progress[targetUID]

		for bookKey, sBP := range srcProg {
			tBP, exists := tgtProg[bookKey]
			if !exists {
				tgtProg[bookKey] = sBP
			} else {
				// Conflict resolution: max progress, union completions
				merged := tBP
				if sBP.CurrentVerse > merged.CurrentVerse {
					merged.CurrentVerse = sBP.CurrentVerse
				}
				if sBP.CorrectEntries > merged.CorrectEntries {
					merged.CorrectEntries = sBP.CorrectEntries
				}
				merged.Mistakes += sBP.Mistakes
				merged.IntroCompleted = merged.IntroCompleted || sBP.IntroCompleted
				merged.ReviewCompleted = merged.ReviewCompleted || sBP.ReviewCompleted
				if sBP.CompletedAt != nil && (merged.CompletedAt == nil || sBP.CompletedAt.Before(*merged.CompletedAt)) {
					merged.CompletedAt = sBP.CompletedAt
				}
				tgtProg[bookKey] = merged
			}
		}
	}

	// 2. Merge Favorites (union without duplicates)
	if srcFavs, ok := s.data.Favorites[sourceUID]; ok {
		tgtFavs := s.data.Favorites[targetUID]
		for _, sf := range srcFavs {
			exists := false
			for _, tf := range tgtFavs {
				if tf.Translation == sf.Translation && tf.BookName == sf.BookName && tf.Chapter == sf.Chapter && tf.Verse == sf.Verse {
					exists = true
					break
				}
			}
			if !exists {
				sfCopy := sf
				sfCopy.UID = targetUID
				sfCopy.ID = len(tgtFavs) + 1
				tgtFavs = append(tgtFavs, sfCopy)
			}
		}
		s.data.Favorites[targetUID] = tgtFavs
	}

	// 3. Merge Badges (union)
	if srcBadges, ok := s.data.Badges[sourceUID]; ok {
		tgtBadges := s.data.Badges[targetUID]
		for _, sb := range srcBadges {
			exists := false
			for _, tb := range tgtBadges {
				if tb.ID == sb.ID {
					exists = true
					break
				}
			}
			if !exists {
				tgtBadges = append(tgtBadges, sb)
			}
		}
		s.data.Badges[targetUID] = tgtBadges
	}

	// 4. Merge Streaks (max streak, combined totals)
	srcStreak, hasSrcStreak := s.data.Streaks[sourceUID]
	tgtStreak, hasTgtStreak := s.data.Streaks[targetUID]

	if hasSrcStreak {
		if !hasTgtStreak {
			newSt := *srcStreak
			newSt.UID = targetUID
			s.data.Streaks[targetUID] = &newSt
		} else {
			if srcStreak.CurrentStreak > tgtStreak.CurrentStreak {
				tgtStreak.CurrentStreak = srcStreak.CurrentStreak
			}
			if srcStreak.LongestStreak > tgtStreak.LongestStreak {
				tgtStreak.LongestStreak = srcStreak.LongestStreak
			}
			tgtStreak.TotalVersesTyped += srcStreak.TotalVersesTyped
			tgtStreak.TotalCharsTyped += srcStreak.TotalCharsTyped
			tgtStreak.TotalSeconds += srcStreak.TotalSeconds
			tgtStreak.TodayVersesCount += srcStreak.TodayVersesCount
		}
	}

	// 5. Reassign Sessions
	for _, sess := range s.data.Sessions {
		if sess.UID == sourceUID {
			sess.UID = targetUID
		}
	}

	// 6. Reassign Comments & Races
	for i := range s.data.Comments {
		if s.data.Comments[i].UID == sourceUID {
			s.data.Comments[i].UID = targetUID
		}
	}
	for i := range s.data.Races {
		if s.data.Races[i].UID == sourceUID {
			s.data.Races[i].UID = targetUID
		}
	}

	// 7. Merge Verse Completions
	if srcComps, ok := s.data.UserVerseCompletions[sourceUID]; ok {
		if s.data.UserVerseCompletions[targetUID] == nil {
			s.data.UserVerseCompletions[targetUID] = make(map[string]int)
		}
		for k, count := range srcComps {
			s.data.UserVerseCompletions[targetUID][k] += count
		}
	}

	// 8. Sanctuary Customizations
	if _, hasTgtSanctuary := s.data.SanctuarySettings[targetUID]; !hasTgtSanctuary {
		if srcSanctuary, hasSrcSanctuary := s.data.SanctuarySettings[sourceUID]; hasSrcSanctuary {
			s.data.SanctuarySettings[targetUID] = srcSanctuary
		}
	}
}

// ---- Verse Comments & Reflection Implementation ----

func (s *localStore) GetVerseComments(ctx context.Context, translation, bookName string, chapter, verse int, userUID string) ([]VerseComment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []VerseComment
	for _, c := range s.data.Comments {
		if c.Translation == translation && c.BookName == bookName && c.Chapter == chapter && c.Verse == verse {
			// Include public comments, or user's own private reflections
			if !c.IsPrivate || c.UID == userUID {
				result = append(result, c)
			}
		}
	}
	return result, nil
}

func (s *localStore) AddVerseComment(ctx context.Context, comment VerseComment) (*VerseComment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(comment.Content) == "" {
		return nil, errors.New("comment content cannot be empty")
	}

	s.data.NextCommentID++
	comment.ID = s.data.NextCommentID
	comment.CreatedAt = time.Now().UTC()
	if comment.Reactions == nil {
		comment.Reactions = make(map[string]int)
	}
	s.data.Comments = append(s.data.Comments, comment)

	if err := s.save(); err != nil {
		return nil, err
	}
	res := comment
	return &res, nil
}

func (s *localStore) ReactVerseComment(ctx context.Context, commentID int, reaction string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Comments {
		if s.data.Comments[i].ID == commentID {
			if s.data.Comments[i].Reactions == nil {
				s.data.Comments[i].Reactions = make(map[string]int)
			}
			s.data.Comments[i].Reactions[reaction]++
			return s.save()
		}
	}
	return errors.New("comment not found")
}

func (s *localStore) FlagVerseComment(ctx context.Context, commentID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Comments {
		if s.data.Comments[i].ID == commentID {
			s.data.Comments[i].Flagged = true
			return s.save()
		}
	}
	return errors.New("comment not found")
}

// ---- Live Fellowship Chat Implementation ----

func (s *localStore) GetRecentChat(ctx context.Context, channel string, limit int) ([]ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var filtered []ChatMessage
	for _, m := range s.data.Chat {
		if m.Channel == channel || channel == "global" {
			filtered = append(filtered, m)
		}
	}

	if limit <= 0 || limit > len(filtered) {
		limit = min(50, len(filtered))
	}
	start := len(filtered) - limit
	if start < 0 {
		start = 0
	}
	return filtered[start:], nil
}

func (s *localStore) AddChatMessage(ctx context.Context, msg ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.NextChatID++
	msg.ID = s.data.NextChatID
	msg.CreatedAt = time.Now().UTC()
	s.data.Chat = append(s.data.Chat, msg)

	// Keep chat backlog bounded to 500 messages
	if len(s.data.Chat) > 500 {
		s.data.Chat = s.data.Chat[len(s.data.Chat)-500:]
	}

	return s.save()
}

// ---- Daily Challenge Racing Implementation ----

func (s *localStore) SaveRaceResult(ctx context.Context, res RaceResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.NextRaceID++
	res.ID = s.data.NextRaceID
	res.CreatedAt = time.Now().UTC()
	s.data.Races = append(s.data.Races, res)
	return s.save()
}

func (s *localStore) GetDailyRaceResults(ctx context.Context, dateStr string) ([]RaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []RaceResult
	for _, r := range s.data.Races {
		if r.Date == dateStr {
			list = append(list, r)
		}
	}

	// Sort by WPM descending, then duration ascending
	sort.Slice(list, func(i, j int) bool {
		if list[i].WPM == list[j].WPM {
			return list[i].Duration < list[j].Duration
		}
		return list[i].WPM > list[j].WPM
	})

	for i := range list {
		list[i].Rank = i + 1
	}
	return list, nil
}

// ---- Analytics & Leaderboard Implementation ----

func (s *localStore) GetLeaderboards(ctx context.Context, dateStr string) (*LeaderboardsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp := &LeaderboardsResponse{
		DailyRace:   make([]LeaderboardEntry, 0),
		TopWPM:      make([]LeaderboardEntry, 0),
		TopStreaks:  make([]LeaderboardEntry, 0),
		TotalVerses: make([]LeaderboardEntry, 0),
	}

	// 1. Daily Race Leaderboard
	var daily []RaceResult
	for _, r := range s.data.Races {
		if r.Date == dateStr {
			daily = append(daily, r)
		}
	}
	sort.Slice(daily, func(i, j int) bool {
		if daily[i].WPM == daily[j].WPM {
			return daily[i].Duration < daily[j].Duration
		}
		return daily[i].WPM > daily[j].WPM
	})
	for i, r := range daily {
		if i >= 10 {
			break
		}
		resp.DailyRace = append(resp.DailyRace, LeaderboardEntry{
			Rank:        i + 1,
			UID:         r.UID,
			DisplayName: r.DisplayName,
			AvatarColor: r.AvatarColor,
			Score:       r.WPM,
			SubText:     fmt.Sprintf("%.1fs · %d%% acc", r.Duration, r.Accuracy),
		})
	}

	// 2. Streaks Leaderboard
	type streakPair struct {
		UID    string
		Streak *UserStreak
	}
	var streaksList []streakPair
	for uid, st := range s.data.Streaks {
		streaksList = append(streaksList, streakPair{UID: uid, Streak: st})
	}
	sort.Slice(streaksList, func(i, j int) bool {
		return streaksList[i].Streak.CurrentStreak > streaksList[j].Streak.CurrentStreak
	})
	for i, sp := range streaksList {
		if i >= 10 {
			break
		}
		name := "Typist " + sp.UID[:min(len(sp.UID), 4)]
		color := "#3b82f6"
		if acc, ok := s.data.Accounts[sp.UID]; ok {
			name = acc.DisplayName
			color = acc.AvatarColor
		}
		resp.TopStreaks = append(resp.TopStreaks, LeaderboardEntry{
			Rank:        i + 1,
			UID:         sp.UID,
			DisplayName: name,
			AvatarColor: color,
			Score:       sp.Streak.CurrentStreak,
			SubText:     fmt.Sprintf("Longest: %dd · %d verses", sp.Streak.LongestStreak, sp.Streak.TotalVersesTyped),
		})
	}

	// 3. Top WPM Leaderboard
	type userBestWPM struct {
		UID     string
		BestWPM int
	}
	bestMap := make(map[string]int)
	for _, sess := range s.data.Sessions {
		if sess.Stats.WPM > bestMap[sess.UID] {
			bestMap[sess.UID] = sess.Stats.WPM
		}
	}
	for _, r := range s.data.Races {
		if r.WPM > bestMap[r.UID] {
			bestMap[r.UID] = r.WPM
		}
	}
	var wpmList []userBestWPM
	for u, w := range bestMap {
		if w > 0 {
			wpmList = append(wpmList, userBestWPM{UID: u, BestWPM: w})
		}
	}
	sort.Slice(wpmList, func(i, j int) bool {
		return wpmList[i].BestWPM > wpmList[j].BestWPM
	})
	for i, ub := range wpmList {
		if i >= 10 {
			break
		}
		name := "Typist " + ub.UID[:min(len(ub.UID), 4)]
		color := "#10b981"
		if acc, ok := s.data.Accounts[ub.UID]; ok {
			name = acc.DisplayName
			color = acc.AvatarColor
		}
		resp.TopWPM = append(resp.TopWPM, LeaderboardEntry{
			Rank:        i + 1,
			UID:         ub.UID,
			DisplayName: name,
			AvatarColor: color,
			Score:       ub.BestWPM,
			SubText:     "WPM record",
		})
	}

	// 4. Total Verses Typed Leaderboard
	sort.Slice(streaksList, func(i, j int) bool {
		return streaksList[i].Streak.TotalVersesTyped > streaksList[j].Streak.TotalVersesTyped
	})
	for i, sp := range streaksList {
		if i >= 10 {
			break
		}
		name := "Typist " + sp.UID[:min(len(sp.UID), 4)]
		color := "#f59e0b"
		if acc, ok := s.data.Accounts[sp.UID]; ok {
			name = acc.DisplayName
			color = acc.AvatarColor
		}
		resp.TotalVerses = append(resp.TotalVerses, LeaderboardEntry{
			Rank:        i + 1,
			UID:         sp.UID,
			DisplayName: name,
			AvatarColor: color,
			Score:       sp.Streak.TotalVersesTyped,
			SubText:     fmt.Sprintf("%d chars total", sp.Streak.TotalCharsTyped),
		})
	}

	return resp, nil
}

func (s *localStore) GetUserAnalytics(ctx context.Context, uid string) (*UserAnalytics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	an := &UserAnalytics{
		UID:             uid,
		DisplayName:     "Typist " + uid[:min(len(uid), 4)],
		RecentSessions:  make([]SessionSummary, 0),
		PercentileSpeed: 50,
	}

	if acc, ok := s.data.Accounts[uid]; ok {
		an.DisplayName = acc.DisplayName
	}

	if st, ok := s.data.Streaks[uid]; ok {
		an.TotalVerses = st.TotalVersesTyped
		an.TotalChars = st.TotalCharsTyped
		an.TotalSeconds = st.TotalSeconds
		an.CurrentStreak = st.CurrentStreak
		an.LongestStreak = st.LongestStreak
	}

	// Calculate Average & Top WPM and collect recent sessions
	var userWPMs []int
	var allSessionsWPM []int

	for _, sess := range s.data.Sessions {
		if sess.Stats.WPM > 0 {
			allSessionsWPM = append(allSessionsWPM, sess.Stats.WPM)
			if sess.UID == uid {
				userWPMs = append(userWPMs, sess.Stats.WPM)
				if sess.Stats.WPM > an.TopWPM {
					an.TopWPM = sess.Stats.WPM
				}
				an.RecentSessions = append(an.RecentSessions, SessionSummary{
					BookName: sess.BookName,
					WPM:      sess.Stats.WPM,
					Chars:    sess.Stats.CharsTyped,
					Date:     sess.CreatedAt,
				})
			}
		}
	}

	if len(userWPMs) > 0 {
		sum := 0
		for _, w := range userWPMs {
			sum += w
		}
		an.AverageWPM = sum / len(userWPMs)
	}

	// Calculate percentile ranking against all typing sessions
	if len(allSessionsWPM) > 0 && an.AverageWPM > 0 {
		slowerCount := 0
		for _, w := range allSessionsWPM {
			if w < an.AverageWPM {
				slowerCount++
			}
		}
		an.PercentileSpeed = int(math.Round((float64(slowerCount) / float64(len(allSessionsWPM))) * 100))
		if an.PercentileSpeed < 5 {
			an.PercentileSpeed = 5
		}
		if an.PercentileSpeed > 99 {
			an.PercentileSpeed = 99
		}
	}

	// Sort recent sessions descending by date, keep last 10
	sort.Slice(an.RecentSessions, func(i, j int) bool {
		return an.RecentSessions[i].Date.After(an.RecentSessions[j].Date)
	})
	if len(an.RecentSessions) > 10 {
		an.RecentSessions = an.RecentSessions[:10]
	}

	return an, nil
}

func (s *localStore) GetCrowdMetrics(ctx context.Context) (*CrowdMetrics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm := &CrowdMetrics{
		TypersByBook: make(map[string]int),
		TrendingBook: "Genesis",
	}

	today := time.Now().UTC().Format("2006-01-02")
	oneHourAgo := time.Now().UTC().Add(-1 * time.Hour)

	for _, sess := range s.data.Sessions {
		cm.TotalVersesGlobal++
		if sess.CreatedAt.After(oneHourAgo) {
			cm.VersesInLastHour++
		}
		if sess.CreatedAt.Format("2006-01-02") == today {
			cm.TodayVersesGlobal++
		}
	}

	return cm, nil
}

// ---- Verse Completion Counts & Sanctuary Customizations Implementation ----

func localVerseKey(trans, book string, chapter, verse int) string {
	return fmt.Sprintf("%s:%s:%d:%d", trans, book, chapter, verse)
}

func (s *localStore) GetVerseCompletions(ctx context.Context, uid, translation, bookName string, chapter, verse int) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k := localVerseKey(translation, bookName, chapter, verse)
	globalCount := s.data.GlobalVerseCompletions[k]
	userCount := 0
	if uMap, ok := s.data.UserVerseCompletions[uid]; ok {
		userCount = uMap[k]
	}
	return userCount, globalCount, nil
}

func (s *localStore) RecordVerseCompletion(ctx context.Context, uid, translation, bookName string, chapter, verse int) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k := localVerseKey(translation, bookName, chapter, verse)
	s.data.GlobalVerseCompletions[k]++
	globalCount := s.data.GlobalVerseCompletions[k]

	userCount := 0
	if uid != "" && !strings.HasPrefix(uid, "angel-") {
		if s.data.UserVerseCompletions[uid] == nil {
			s.data.UserVerseCompletions[uid] = make(map[string]int)
		}
		s.data.UserVerseCompletions[uid][k]++
		userCount = s.data.UserVerseCompletions[uid][k]
	}

	_ = s.save()
	return userCount, globalCount, nil
}

func (s *localStore) GetCustomizations(ctx context.Context, uid string) (*SanctuarySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg, ok := s.data.SanctuarySettings[uid]; ok {
		cCopy := cfg
		if cCopy.GuardianLevel < 1 {
			cCopy.GuardianLevel = 1
		}
		cCopy.GuardianWards = (cCopy.GuardianLevel - 1) / 3
		if cCopy.GuardianPower == 0 {
			cCopy.GuardianPower = 10 + (cCopy.GuardianLevel-1)*2
		}
		if cCopy.GuardianRadiance == 0 {
			cCopy.GuardianRadiance = 100 + (cCopy.GuardianLevel-1)*5
		}
		cCopy.TrackerUnlocked = cCopy.DemonsDefeated >= 6
		return &cCopy, nil
	}
	return &SanctuarySettings{
		TextColor:        "emerald",
		FontFamily:       "sans",
		CaretAura:        "default",
		AudioProfile:     "classic",
		CelebrateFx:      "confetti",
		GuardianLevel:    1,
		GuardianXP:       0,
		DemonsDefeated:   0,
		GuardianWards:    0,
		GuardianPower:    10,
		GuardianRadiance: 100,
		TrackerUnlocked:  false,
	}, nil
}

func (s *localStore) SaveCustomizations(ctx context.Context, uid string, settings SanctuarySettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.SanctuarySettings == nil {
		s.data.SanctuarySettings = make(map[string]SanctuarySettings)
	}
	if settings.GuardianLevel < 1 {
		settings.GuardianLevel = 1
	}
	settings.GuardianWards = (settings.GuardianLevel - 1) / 3
	if settings.GuardianPower == 0 {
		settings.GuardianPower = 10 + (settings.GuardianLevel-1)*2
	}
	if settings.GuardianRadiance == 0 {
		settings.GuardianRadiance = 100 + (settings.GuardianLevel-1)*5
	}
	settings.TrackerUnlocked = settings.DemonsDefeated >= 6
	s.data.SanctuarySettings[uid] = settings
	return s.save()
}

func (s *localStore) RecordWildDemonDefeat(ctx context.Context, uid string, demonRank string, demonLevel int) (*SanctuarySettings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.SanctuarySettings == nil {
		s.data.SanctuarySettings = make(map[string]SanctuarySettings)
	}

	cfg, ok := s.data.SanctuarySettings[uid]
	if !ok {
		cfg = SanctuarySettings{
			TextColor:        "emerald",
			FontFamily:       "sans",
			CaretAura:        "default",
			AudioProfile:     "classic",
			CelebrateFx:      "confetti",
			GuardianLevel:    1,
			GuardianXP:       0,
			DemonsDefeated:   0,
			GuardianWards:    0,
			GuardianPower:    10,
			GuardianRadiance: 100,
			TrackerUnlocked:  false,
		}
	}

	if cfg.GuardianLevel < 1 {
		cfg.GuardianLevel = 1
	}

	cfg.DemonsDefeated++
	if demonLevel < 1 {
		demonLevel = 1
	}
	xpGained := 100 + (demonLevel-1)*30
	cfg.GuardianXP += xpGained

	leveledUp := false
	neededXP := cfg.GuardianLevel * 100
	for cfg.GuardianXP >= neededXP {
		cfg.GuardianXP -= neededXP
		cfg.GuardianLevel++
		leveledUp = true
		neededXP = cfg.GuardianLevel * 100
	}

	cfg.GuardianWards = (cfg.GuardianLevel - 1) / 3
	cfg.GuardianPower = 10 + (cfg.GuardianLevel-1)*2
	cfg.GuardianRadiance = 100 + (cfg.GuardianLevel-1)*5
	cfg.TrackerUnlocked = cfg.DemonsDefeated >= 6

	s.data.SanctuarySettings[uid] = cfg
	_ = s.save()
	res := cfg
	return &res, leveledUp, nil
}

func defaultBossRaid() *BossRaid {
	return defaultBossRaidForLevel(2, false)
}

func defaultBossRaidForLevel(level int, isDemon bool) *BossRaid {
	if level < 1 {
		level = 1
	}
	if level > 4 {
		level = 4
	}

	if isDemon {
		wpms := []int{130, 160, 190, 220}
		hps := []int{4000, 6000, 9000, 12000}
		reqs := []int{65, 80, 95, 110}
		titles := []string{
			"Angel of the Bottomless Pit",
			"Prince of Abyssal Darkness",
			"The Ancient Devourer",
			"Sovereign of the Netherworld",
		}
		return &BossRaid{
			ID:          fmt.Sprintf("boss-apollyon-lv%d", level),
			BossName:    "Apollyon",
			BossTitle:   titles[level-1],
			BossRank:    "apollyon",
			BossWPM:     wpms[level-1],
			Level:       level,
			MaxLevel:    4,
			IsDemonBoss: true,
			MaxHP:       hps[level-1],
			CurrentHP:   hps[level-1],
			RequiredWPM: reqs[level-1],
			Translation: "kjv",
			BookName:    "Revelation",
			Chapter:     9,
			Verse:       11,
			Text:        "And they had a king over them, which is the angel of the bottomless pit, whose name in the Hebrew tongue is Abaddon, but in the Greek tongue hath his name Apollyon.",
			Conquered:   false,
			Conquerors:  []BossConqueror{},
			UpdatedAt:   time.Now().UTC(),
		}
	}

	wpms := []int{140, 160, 180, 200}
	hps := []int{3500, 5000, 7500, 10000}
	reqs := []int{70, 80, 90, 100}
	titles := []string{
		"Vanguard of Heaven",
		"Captain of the Heavenly Host",
		"Archangel of Divine Might",
		"Supreme Commander of the Seraphim",
	}
	return &BossRaid{
		ID:          fmt.Sprintf("boss-michael-lv%d", level),
		BossName:    "Big Mike",
		BossTitle:   titles[level-1],
		BossRank:    "michael",
		BossWPM:     wpms[level-1],
		Level:       level,
		MaxLevel:    4,
		IsDemonBoss: false,
		MaxHP:       hps[level-1],
		CurrentHP:   hps[level-1],
		RequiredWPM: reqs[level-1],
		Translation: "kjv",
		BookName:    "Revelation",
		Chapter:     12,
		Verse:       7,
		Text:        "And there was war in heaven: Michael and his angels fought against the dragon; and the dragon fought and his angels,",
		Conquered:   false,
		Conquerors:  []BossConqueror{},
		UpdatedAt:   time.Now().UTC(),
	}
}

func (s *localStore) GetBossRaid(ctx context.Context) (*BossRaid, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.BossRaid == nil {
		s.data.BossRaid = defaultBossRaid()
	}
	if s.data.BossRaid.BossName == "Archangel Michael" {
		s.data.BossRaid.BossName = "Big Mike"
	}
	res := *s.data.BossRaid
	return &res, nil
}

func (s *localStore) GetBossRaidForLevel(ctx context.Context, level int, isDemon bool) (*BossRaid, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.BossRaid != nil && s.data.BossRaid.Level == level && s.data.BossRaid.IsDemonBoss == isDemon {
		if s.data.BossRaid.BossName == "Archangel Michael" {
			s.data.BossRaid.BossName = "Big Mike"
		}
		res := *s.data.BossRaid
		return &res, nil
	}

	s.data.BossRaid = defaultBossRaidForLevel(level, isDemon)
	_ = s.save()
	res := *s.data.BossRaid
	return &res, nil
}

func (s *localStore) RecordBossStrike(ctx context.Context, uid, displayName, avatarColor string, wpm, damage int) (*BossRaid, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.BossRaid == nil {
		s.data.BossRaid = defaultBossRaid()
	}

	b := s.data.BossRaid
	if b.CurrentHP > 0 {
		b.CurrentHP -= damage
		if b.CurrentHP <= 0 {
			b.CurrentHP = 0
			b.Conquered = true
		}
	}

	conq := BossConqueror{
		UID:         uid,
		DisplayName: displayName,
		AvatarColor: avatarColor,
		WPM:         wpm,
		Damage:      damage,
		Timestamp:   time.Now().UTC(),
	}
	b.Conquerors = append([]BossConqueror{conq}, b.Conquerors...)
	if len(b.Conquerors) > 50 {
		b.Conquerors = b.Conquerors[:50]
	}
	b.UpdatedAt = time.Now().UTC()

	_ = s.save()
	res := *b
	return &res, nil
}

func (s *localStore) ResetBossRaid(ctx context.Context, raid BossRaid) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.BossRaid = &raid
	return s.save()
}


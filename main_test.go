package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCleanVerseTextComprehensive(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "¶ In the beginning God created the heaven and the earth.",
			expected: "In the beginning God created the heaven and the earth.",
		},
		{
			input:    "The grace of our Lord Jesus Christ [be] with you all. Amen.",
			expected: "The grace of our Lord Jesus Christ be with you all. Amen.",
		},
		{
			input:    "“Come,” he said, ‘follow me!’ — and they went.",
			expected: "\"Come,\" he said, 'follow me!' - and they went.",
		},
		{
			input:    "I will observe thy statutes: Oh forsake me not utterly. ב BETH.",
			expected: "I will observe thy statutes: Oh forsake me not utterly. BETH.",
		},
		{
			input:    "   Multiple    spaces   and\ttabs\nshould   be clean.  ",
			expected: "Multiple spaces and tabs should be clean.",
		},
	}

	for _, c := range cases {
		out := cleanVerseText(c.input)
		if out != c.expected {
			t.Errorf("cleanVerseText(%q) = %q, expected %q", c.input, out, c.expected)
		}
	}
}

func TestDatasetsLoaded(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatalf("Failed to load translations and guides: %v", err)
	}

	if len(translations) != 8 {
		t.Errorf("Expected 8 translations, got %d", len(translations))
	}

	allTrans := []string{"kjv", "esv", "niv", "nlt", "hcsb", "asv", "bbe", "rvr1909"}
	for _, transID := range allTrans {
		td, ok := translations[transID]
		if !ok {
			t.Fatalf("Missing translation %s", transID)
		}
		if len(td.Verses) < 30000 {
			t.Errorf("Translation %s has unexpectedly few verses: %d", transID, len(td.Verses))
		}
		if len(td.CanonicalBooks) != 66 {
			t.Errorf("Translation %s canonical books count != 66: %d", transID, len(td.CanonicalBooks))
		}
		if td.CanonicalBooks[0] != "Genesis" || td.CanonicalBooks[65] != "Revelation" {
			t.Errorf("Canonical order invalid for %s: first=%s, last=%s", transID, td.CanonicalBooks[0], td.CanonicalBooks[65])
		}
	}

	if len(bookGuides) != 66 {
		t.Errorf("Expected 66 book guides, got %d", len(bookGuides))
	}
	if len(bookGuidesES) != 66 {
		t.Errorf("Expected 66 Spanish book guides, got %d", len(bookGuidesES))
	}

	genGuide := bookGuides["Genesis"]
	if genGuide.Order != 1 || genGuide.Description == "" || genGuide.Review == "" {
		t.Errorf("Invalid Genesis guide: %+v", genGuide)
	}

	genGuideES, okES := getBookGuide("rvr1909", "Genesis")
	if !okES || genGuideES.Order != 1 || genGuideES.Name != "Génesis" || genGuideES.Description == "" || genGuideES.Review == "" {
		t.Errorf("Invalid Spanish Genesis guide: %+v", genGuideES)
	}
}

func TestLocalStoreProgressAndFeatures(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	storePath := filepath.Join(tmpDir, "data.json")
	ls, err := newLocalStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	uid := "test_user_42"

	// 1. Progress test
	bp, err := ls.GetBookProgress(ctx, uid, "kjv", "Genesis", 1533)
	if err != nil {
		t.Fatalf("GetBookProgress error: %v", err)
	}
	if bp.CurrentVerse != 0 || bp.TotalVerses != 1533 || bp.IntroCompleted {
		t.Errorf("Unexpected bp: %+v", bp)
	}

	bp.CurrentVerse = 5
	bp.CorrectEntries = 5
	bp.IntroCompleted = true
	if err := ls.UpdateBookProgress(ctx, uid, "kjv", "Genesis", bp); err != nil {
		t.Fatalf("UpdateBookProgress error: %v", err)
	}

	bp2, err := ls.GetBookProgress(ctx, uid, "kjv", "Genesis", 1533)
	if err != nil || bp2.CurrentVerse != 5 || !bp2.IntroCompleted {
		t.Errorf("Expected currentVerse 5 and IntroCompleted true, got %+v (err %v)", bp2, err)
	}

	// 2. Favorites test
	fav := Favorite{
		UID:         uid,
		Translation: "kjv",
		BookName:    "Genesis",
		Book:        1,
		Chapter:     1,
		Verse:       1,
		Text:        "In the beginning God created the heaven and the earth.",
	}
	if err := ls.AddFavorite(ctx, fav); err != nil {
		t.Fatalf("AddFavorite error: %v", err)
	}

	isFav, err := ls.IsFavorite(ctx, uid, "kjv", "Genesis", 1, 1)
	if err != nil || !isFav {
		t.Errorf("Expected isFav true, got %v (err %v)", isFav, err)
	}

	favList, err := ls.GetFavorites(ctx, uid)
	if err != nil || len(favList) != 1 {
		t.Errorf("Expected 1 favorite, got %d (err %v)", len(favList), err)
	}

	if err := ls.RecordFavoriteTyped(ctx, uid, "kjv", "Genesis", 1, 1); err != nil {
		t.Fatalf("RecordFavoriteTyped error: %v", err)
	}
	favListAfter, _ := ls.GetFavorites(ctx, uid)
	if favListAfter[0].TimesTyped != 1 {
		t.Errorf("Expected times_typed 1, got %d", favListAfter[0].TimesTyped)
	}

	// 3. Streak test
	streak, err := ls.RecordVerseCompleted(ctx, uid, 50, 10.0)
	if err != nil {
		t.Fatalf("RecordVerseCompleted error: %v", err)
	}
	if streak.CurrentStreak < 1 || streak.TotalVersesTyped != 1 {
		t.Errorf("Unexpected streak: %+v", streak)
	}

	// 4. Badges test
	badge := Badge{
		ID:          "verse_1",
		Type:        "milestone",
		Name:        "First Verse Scribe",
		Description: "First verse typed",
		Icon:        "🌱",
	}
	awarded, err := ls.AwardBadge(ctx, uid, badge)
	if err != nil || !awarded {
		t.Errorf("Expected badge awarded true, got %v (err %v)", awarded, err)
	}
	awardedAgain, _ := ls.AwardBadge(ctx, uid, badge)
	if awardedAgain {
		t.Errorf("Expected duplicate badge awarded false, got true")
	}

	badges, err := ls.GetBadges(ctx, uid)
	if err != nil || len(badges) != 1 {
		t.Errorf("Expected 1 badge, got %d", len(badges))
	}
}

func TestWebSocketFlowWithMandatoryIntroAndLockedReview(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatal(err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_ws_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	store = ls

	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?uid=test_ws_user2&trans=kjv"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer wsConn.Close()

	readUntilType := func(targetType string) (Message, error) {
		for {
			var m Message
			if err := wsConn.ReadJSON(&m); err != nil {
				return m, err
			}
			if m.Type == targetType {
				return m, nil
			}
		}
	}

	// 1. Should receive books message with 66 canonical books
	booksMsg, err := readUntilType("books")
	if err != nil {
		t.Fatalf("Failed to read books: %v", err)
	}
	if len(booksMsg.Books) != 66 {
		t.Fatalf("Expected 66 books, got %d", len(booksMsg.Books))
	}

	// 2. Select Book Genesis
	if err := wsConn.WriteJSON(Message{Type: "select_book", Content: "Genesis"}); err != nil {
		t.Fatal(err)
	}

	// 3. Since Genesis is fresh, first message MUST be mandatory intro!
	introMsg, err := readUntilType("intro")
	if err != nil {
		t.Fatal(err)
	}
	if !introMsg.Mandatory {
		t.Fatalf("Expected intro to be mandatory before starting book, got mandatory=false")
	}
	if introMsg.ReviewLocked != true {
		t.Fatalf("Expected review to be locked while starting book")
	}

	// 4. Test that trying to open review while verses are incomplete returns review_locked
	if err := wsConn.WriteJSON(Message{Type: "start_review", Content: "Genesis"}); err != nil {
		t.Fatal(err)
	}
	lockedMsg, err := readUntilType("review_locked")
	if err != nil {
		t.Fatal(err)
	}
	if !lockedMsg.ReviewLocked {
		t.Fatalf("Expected review_locked to be true")
	}

	// 5. Type the mandatory intro
	if err := wsConn.WriteJSON(Message{Type: "content", Content: introMsg.Content, WPM: 65, Duration: 4.5}); err != nil {
		t.Fatal(err)
	}

	modeCompleteMsg, err := readUntilType("modeComplete")
	if err != nil {
		t.Fatal(err)
	}
	if modeCompleteMsg.Mode != "intro" {
		t.Fatalf("Expected modeComplete intro, got %s", modeCompleteMsg.Mode)
	}

	// 6. Now Chapter 1 Verse 1 should be unlocked and delivered!
	verseMsg, err := readUntilType("verse")
	if err != nil {
		t.Fatal(err)
	}
	if verseMsg.Content != "In the beginning God created the heaven and the earth." {
		t.Fatalf("Unexpected verse 1 text: %q", verseMsg.Content)
	}
	if verseMsg.Number != 1 {
		t.Fatalf("Expected verse number 1, got %d", verseMsg.Number)
	}

	// 7. Type verse 1 with accurate WPM and duration
	if err := wsConn.WriteJSON(Message{Type: "content", Content: verseMsg.Content, WPM: 70, Duration: 3.2}); err != nil {
		t.Fatal(err)
	}

	correctMsg, err := readUntilType("correct")
	if err != nil {
		t.Fatal(err)
	}
	if correctMsg.WPM != 70 {
		t.Fatalf("Expected WPM 70, got %d", correctMsg.WPM)
	}

	// Verse 2 was automatically advanced and sent
	v2Msg, err := readUntilType("verse")
	if err != nil {
		t.Fatal(err)
	}
	if v2Msg.Number != 2 {
		t.Fatalf("Expected verse number 2, got %d", v2Msg.Number)
	}

	// 8. Toggle Favorite
	if err := wsConn.WriteJSON(Message{Type: "toggle_favorite"}); err != nil {
		t.Fatal(err)
	}
	favMsg, err := readUntilType("favoriteToggled")
	if err != nil {
		t.Fatal(err)
	}
	if !favMsg.IsFavorite {
		t.Fatalf("Expected isFavorite true, got false")
	}

	// 9. Verse 2 was unlocked when verse 1 was completed.
	// Trying to jump to verse 5 (locked) MUST be rejected by the server!
	if err := wsConn.WriteJSON(Message{Type: "jump_verse", Number: 5}); err != nil {
		t.Fatal(err)
	}
	wrongJumpMsg, err := readUntilType("wrong")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrongJumpMsg.Content, "locked") {
		t.Fatalf("Expected locked message when jumping to verse 5, got %q", wrongJumpMsg.Content)
	}

	// 10. Jumping back to verse 1 (already unlocked) MUST succeed!
	if err := wsConn.WriteJSON(Message{Type: "jump_verse", Number: 1}); err != nil {
		t.Fatal(err)
	}
	v1AgainMsg, err := readUntilType("verse")
	if err != nil {
		t.Fatal(err)
	}
	if v1AgainMsg.Number != 1 {
		t.Fatalf("Expected verse number 1, got %d", v1AgainMsg.Number)
	}
}

func TestAccountRegistrationLoginAndProgressMerge(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_auth_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	anonUID := "anon_guest_999"

	// 1. Anon user builds progress, favorites, streak, badge
	bp := &BookProgress{
		CurrentVerse:   10,
		TotalVerses:    1533,
		IntroCompleted: true,
	}
	_ = ls.UpdateBookProgress(ctx, anonUID, "kjv", "Genesis", bp)
	_ = ls.AddFavorite(ctx, Favorite{
		UID:         anonUID,
		Translation: "kjv",
		BookName:    "Genesis",
		Chapter:     1,
		Verse:       1,
		Text:        "In the beginning...",
	})
	_, _ = ls.RecordVerseCompleted(ctx, anonUID, 100, 15.0)
	_, _ = ls.AwardBadge(ctx, anonUID, Badge{
		ID:   "verse_1",
		Name: "First Verse",
	})

	// 2. User registers an account and claims progress
	acc, err := ls.RegisterAccount(ctx, "brother_david", "david@example.com", hashPassword("sacredpass123"), "David", "#3b82f6", anonUID)
	if err != nil {
		t.Fatalf("Failed to register account: %v", err)
	}
	if acc.Username != "brother_david" || acc.DisplayName != "David" {
		t.Errorf("Unexpected account details: %+v", acc)
	}

	// 3. Verify merged progress in account
	accountBP, err := ls.GetBookProgress(ctx, acc.UID, "kjv", "Genesis", 1533)
	if err != nil || accountBP.CurrentVerse != 10 || !accountBP.IntroCompleted {
		t.Errorf("Expected claimed progress currentVerse=10, introCompleted=true, got %+v", accountBP)
	}

	accountFavs, _ := ls.GetFavorites(ctx, acc.UID)
	if len(accountFavs) != 1 {
		t.Errorf("Expected 1 claimed favorite, got %d", len(accountFavs))
	}

	accountBadges, _ := ls.GetBadges(ctx, acc.UID)
	if len(accountBadges) != 1 || accountBadges[0].ID != "verse_1" {
		t.Errorf("Expected claimed badge, got %+v", accountBadges)
	}

	accountStreak, _ := ls.GetStreak(ctx, acc.UID)
	if accountStreak.CurrentStreak < 1 || accountStreak.TotalVersesTyped < 1 {
		t.Errorf("Expected claimed streak, got %+v", accountStreak)
	}

	// 4. Test Login
	loggedIn, err := ls.LoginAccount(ctx, "brother_david", hashPassword("sacredpass123"), "")
	if err != nil || loggedIn.UID != acc.UID {
		t.Errorf("Login failed: %v", err)
	}

	// 5. Test Conflict Resolution: anon user has further verse, account has other badge
	anon2 := "anon_device_2"
	_ = ls.UpdateBookProgress(ctx, anon2, "kjv", "Genesis", &BookProgress{
		CurrentVerse:   25,
		TotalVerses:    1533,
		IntroCompleted: true,
	})
	_, _ = ls.AwardBadge(ctx, anon2, Badge{ID: "verse_25", Name: "25 Verses"})

	// Login and merge anon2
	merged, err := ls.LoginAccount(ctx, "brother_david", hashPassword("sacredpass123"), anon2)
	if err != nil {
		t.Fatalf("Login with merge failed: %v", err)
	}

	mergedBP, _ := ls.GetBookProgress(ctx, merged.UID, "kjv", "Genesis", 1533)
	if mergedBP.CurrentVerse != 25 {
		t.Errorf("Expected max verse 25, got %d", mergedBP.CurrentVerse)
	}

	mergedBadges, _ := ls.GetBadges(ctx, merged.UID)
	if len(mergedBadges) != 2 {
		t.Errorf("Expected union of badges (2), got %d", len(mergedBadges))
	}
}

func TestVerseCommentsAndReflections(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_comments_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// 1. Add public comment
	pubCmt, err := ls.AddVerseComment(ctx, VerseComment{
		UID:         "user_1",
		DisplayName: "Hannah",
		AvatarColor: "#ec4899",
		StreakDays:  5,
		Translation: "kjv",
		BookName:    "John",
		Chapter:     3,
		Verse:       16,
		PromptType:  "reflection",
		Content:     "For God so loved the world. A beautiful verse of hope.",
		IsPrivate:   false,
	})
	if err != nil {
		t.Fatalf("AddVerseComment error: %v", err)
	}
	if pubCmt.ID == 0 {
		t.Errorf("Expected assigned comment ID")
	}

	// 2. Add private reflection journal
	privCmt, err := ls.AddVerseComment(ctx, VerseComment{
		UID:         "user_1",
		DisplayName: "Hannah",
		Translation: "kjv",
		BookName:    "John",
		Chapter:     3,
		Verse:       16,
		PromptType:  "prayer",
		Content:     "My personal prayer today.",
		IsPrivate:   true,
	})
	if err != nil {
		t.Fatalf("Add private comment error: %v", err)
	}

	// 3. Query as user_1 (should see both public and own private)
	cmts1, _ := ls.GetVerseComments(ctx, "kjv", "John", 3, 16, "user_1")
	if len(cmts1) != 2 {
		t.Errorf("Expected user_1 to see 2 comments, got %d", len(cmts1))
	}

	// 4. Query as user_2 (should only see the public comment)
	cmts2, _ := ls.GetVerseComments(ctx, "kjv", "John", 3, 16, "user_2")
	if len(cmts2) != 1 || cmts2[0].ID != pubCmt.ID {
		t.Errorf("Expected user_2 to see only 1 public comment, got %d", len(cmts2))
	}

	// 5. Reaction
	if err := ls.ReactVerseComment(ctx, pubCmt.ID, "amen"); err != nil {
		t.Errorf("React error: %v", err)
	}
	cmtsAfterReact, _ := ls.GetVerseComments(ctx, "kjv", "John", 3, 16, "user_2")
	if cmtsAfterReact[0].Reactions["amen"] != 1 {
		t.Errorf("Expected amen reaction count 1, got %d", cmtsAfterReact[0].Reactions["amen"])
	}

	// 6. Flag comment
	if err := ls.FlagVerseComment(ctx, privCmt.ID); err != nil {
		t.Errorf("Flag error: %v", err)
	}
}

func TestDailyChallengeAndServerTimingVerification(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatal(err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_race_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// 1. Deterministic Verse of the Day
	ch1, err := getDailyChallengeVerse("2026-10-10", "kjv")
	if err != nil || ch1.Text == "" {
		t.Fatalf("Failed to get daily challenge: %v", err)
	}
	ch2, _ := getDailyChallengeVerse("2026-10-10", "kjv")
	if ch1.BookName != ch2.BookName || ch1.Chapter != ch2.Chapter || ch1.Verse != ch2.Verse {
		t.Errorf("Expected deterministic challenge for same date")
	}

	// 2. Anti-spoofing verification: client claiming 999 WPM / 0.05s for a 150-character verse
	startTime := time.Now().Add(-5 * time.Second)
	testText := "For God so loved the world, that he gave his only begotten Son, that whosoever believeth in him should not perish, but have everlasting life."
	verifiedDur, verifiedWPM, plausible := verifySubmissionTiming(startTime, 0.05, testText, 999)
	if plausible {
		t.Errorf("Expected 999 WPM to be marked as not plausible")
	}
	if verifiedWPM > 250 {
		t.Errorf("Expected WPM to be capped at 250, got %d", verifiedWPM)
	}
	if verifiedDur < 1.0 {
		t.Errorf("Expected duration to be adjusted above 1.0s, got %.2f", verifiedDur)
	}

	// 3. Save Race Results and verify ranking
	dateStr := "2026-10-10"
	_ = ls.SaveRaceResult(ctx, RaceResult{
		Date:        dateStr,
		UID:         "user_slow",
		DisplayName: "Slow Typist",
		WPM:         45,
		Duration:    12.0,
		Accuracy:    98,
	})
	_ = ls.SaveRaceResult(ctx, RaceResult{
		Date:        dateStr,
		UID:         "user_fast",
		DisplayName: "Fast Typist",
		WPM:         95,
		Duration:    5.5,
		Accuracy:    100,
	})

	results, err := ls.GetDailyRaceResults(ctx, dateStr)
	if err != nil || len(results) != 2 {
		t.Fatalf("Expected 2 race results, got %d", len(results))
	}
	if results[0].UID != "user_fast" || results[0].Rank != 1 {
		t.Errorf("Expected user_fast to be rank 1, got %+v", results[0])
	}
	if results[1].UID != "user_slow" || results[1].Rank != 2 {
		t.Errorf("Expected user_slow to be rank 2, got %+v", results[1])
	}
}

func TestHubPresenceAndGhostCursors(t *testing.T) {
	hub := newHub()
	go hub.run()

	conn1 := &safeConn{conn: nil}
	client1 := &Client{
		conn:        conn1,
		UID:         "c1",
		DisplayName: "Alice",
		Translation: "kjv",
		Book:        "Genesis",
		Chapter:     1,
		Verse:       1,
	}

	conn2 := &safeConn{conn: nil}
	client2 := &Client{
		conn:        conn2,
		UID:         "c2",
		DisplayName: "Bob",
		Translation: "kjv",
		Book:        "Genesis",
		Chapter:     1,
		Verse:       1,
	}

	hub.Register(client1)
	hub.Register(client2)

	time.Sleep(20 * time.Millisecond)

	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != 2 {
		t.Errorf("Expected 2 registered clients in hub, got %d", clientCount)
	}

	hub.Unregister(client1)
	hub.Unregister(client2)
}

func TestAnalyticsAndLeaderboards(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_analytics_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	uid := "analytics_user"

	// Record several sessions with various speeds
	_ = ls.RecordTypingSession(ctx, "other_1", "kjv", "Genesis", 50, 50, 40, 5.0)
	_ = ls.RecordTypingSession(ctx, "other_2", "kjv", "Genesis", 50, 50, 50, 5.0)
	_ = ls.RecordTypingSession(ctx, uid, "kjv", "Genesis", 100, 100, 75, 4.0)

	an, err := ls.GetUserAnalytics(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserAnalytics failed: %v", err)
	}
	if an.TopWPM != 75 || an.AverageWPM != 75 {
		t.Errorf("Expected top/avg WPM 75, got top=%d, avg=%d", an.TopWPM, an.AverageWPM)
	}
	if an.PercentileSpeed < 60 {
		t.Errorf("Expected percentile speed to be high (>60%%), got %d%%", an.PercentileSpeed)
	}

	lbs, err := ls.GetLeaderboards(ctx, "2026-10-10")
	if err != nil {
		t.Fatalf("GetLeaderboards failed: %v", err)
	}
	if len(lbs.TopWPM) == 0 {
		t.Errorf("Expected at least 1 entry in TopWPM leaderboard")
	}
}

func TestOfflineSyncQueue(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_sync_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	uid := "offline_user"

	// Simulate offline sync items
	items := []SyncVerseItem{
		{Translation: "kjv", BookName: "Romans", Chapter: 1, Verse: 1, WPM: 65, Duration: 4.0},
		{Translation: "kjv", BookName: "Romans", Chapter: 1, Verse: 2, WPM: 70, Duration: 3.5},
	}

	for _, it := range items {
		bp, _ := ls.GetBookProgress(ctx, uid, it.Translation, it.BookName, 100)
		if it.Verse > bp.CurrentVerse {
			bp.CurrentVerse = it.Verse
		}
		bp.CorrectEntries++
		_ = ls.UpdateBookProgress(ctx, uid, it.Translation, it.BookName, bp)
		_, _ = ls.RecordVerseCompleted(ctx, uid, 50, it.Duration)
	}

	bp, _ := ls.GetBookProgress(ctx, uid, "kjv", "Romans", 100)
	if bp.CurrentVerse != 2 {
		t.Errorf("Expected current verse 2 after offline sync, got %d", bp.CurrentVerse)
	}

	st, _ := ls.GetStreak(ctx, uid)
	if st.TotalVersesTyped != 2 {
		t.Errorf("Expected 2 verses recorded in streak, got %d", st.TotalVersesTyped)
	}
}

func TestVerseCompletionsTracking(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_completions_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	trans := "kjv"
	book := "John"
	ch := 3
	v := 16

	// Initially zero
	uComps, gComps, err := ls.GetVerseCompletions(ctx, "user_alpha", trans, book, ch, v)
	if err != nil {
		t.Fatalf("Failed to get completions: %v", err)
	}
	if uComps != 0 || gComps != 0 {
		t.Errorf("Expected 0 completions initially, got user=%d, global=%d", uComps, gComps)
	}

	// User Alpha completes John 3:16
	uComps, gComps, err = ls.RecordVerseCompletion(ctx, "user_alpha", trans, book, ch, v)
	if err != nil {
		t.Fatalf("Failed to record completion: %v", err)
	}
	if uComps != 1 || gComps != 1 {
		t.Errorf("Expected 1 and 1, got user=%d, global=%d", uComps, gComps)
	}

	// User Alpha completes John 3:16 again
	uComps, gComps, err = ls.RecordVerseCompletion(ctx, "user_alpha", trans, book, ch, v)
	if err != nil {
		t.Fatalf("Failed to record second completion: %v", err)
	}
	if uComps != 2 || gComps != 2 {
		t.Errorf("Expected 2 and 2, got user=%d, global=%d", uComps, gComps)
	}

	// User Beta completes John 3:16
	uComps, gComps, err = ls.RecordVerseCompletion(ctx, "user_beta", trans, book, ch, v)
	if err != nil {
		t.Fatalf("Failed to record completion for user beta: %v", err)
	}
	if uComps != 1 || gComps != 3 {
		t.Errorf("Expected beta=1 and global=3, got user=%d, global=%d", uComps, gComps)
	}

	// Verify User Alpha still has 2
	uComps, gComps, _ = ls.GetVerseCompletions(ctx, "user_alpha", trans, book, ch, v)
	if uComps != 2 || gComps != 3 {
		t.Errorf("Expected alpha=2 and global=3, got user=%d, global=%d", uComps, gComps)
	}

	// Test claiming / merging progress
	anonUid := "anon_123"
	_, _, _ = ls.RecordVerseCompletion(ctx, anonUid, trans, "Romans", 8, 28)
	err = ls.MergeUserProgress(ctx, "user_alpha", anonUid)
	if err != nil {
		t.Fatalf("Failed to merge progress: %v", err)
	}
	mergedU, _, _ := ls.GetVerseCompletions(ctx, "user_alpha", trans, "Romans", 8, 28)
	if mergedU != 1 {
		t.Errorf("Expected user_alpha to inherit completion of Romans 8:28, got %d", mergedU)
	}
}

func TestCustomizationsUnlockAndEquip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_customs_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	uid := "scribe_user"

	// 1. Verify default sanctuary settings
	def := DefaultCustomizations()
	if def.TextColor != "emerald" || def.FontFamily != "sans" || def.CaretAura != "default" {
		t.Errorf("Unexpected default customizations: %+v", def)
	}

	// 2. Initial unlocks: 60 WPM should have emerald, amber, sapphire, but NOT amethyst (80 WPM)
	unlocks60 := GetAllCustomizationUnlocks(60, 10, 1, 0)
	var amethystUnlocked bool
	for _, u := range unlocks60 {
		if u.ID == "color_amethyst" {
			amethystUnlocked = u.Unlocked
		}
	}
	if amethystUnlocked {
		t.Errorf("Expected Radiant Amethyst to be locked at 60 WPM")
	}

	// 3. User reaches 80 WPM: Radiant Amethyst must unlock
	unlocks80 := GetAllCustomizationUnlocks(80, 10, 1, 0)
	for _, u := range unlocks80 {
		if u.ID == "color_amethyst" {
			amethystUnlocked = u.Unlocked
		}
	}
	if !amethystUnlocked {
		t.Errorf("Expected Radiant Amethyst to unlock upon reaching 80 WPM milestone!")
	}

	// 4. Test newly unlocked detection
	newUnlocks := FindNewlyUnlockedCustomizations(79, 82, 10, 11, 1, 1, 0)
	var foundAmethyst bool
	for _, nu := range newUnlocks {
		if nu.ID == "color_amethyst" {
			foundAmethyst = true
			break
		}
	}
	if !foundAmethyst {
		t.Errorf("Expected FindNewlyUnlockedCustomizations to report color_amethyst when jumping from 79 to 82 WPM")
	}

	// 5. Test saving and retrieving equipped customizations in store
	equip := SanctuarySettings{
		TextColor:    "amethyst",
		FontFamily:   "serif",
		CaretAura:    "halo",
		AudioProfile: "chime",
		CelebrateFx:  "feathers",
	}
	if err := ls.SaveCustomizations(ctx, uid, equip); err != nil {
		t.Fatalf("Failed to save customizations: %v", err)
	}

	loaded, err := ls.GetCustomizations(ctx, uid)
	if err != nil {
		t.Fatalf("Failed to get customizations: %v", err)
	}
	if loaded.TextColor != "amethyst" || loaded.FontFamily != "serif" || loaded.CaretAura != "halo" || loaded.AudioProfile != "chime" || loaded.CelebrateFx != "feathers" {
		t.Errorf("Saved customizations do not match loaded: got %+v, want %+v", loaded, equip)
	}
}

func TestAngelBotEngine(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_angel_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}

	hub := newHub()
	mgr := newAngelManager(ls, hub)

	// 1. Verify 5 canonical angel templates (including personal guardian angel)
	templates := mgr.GetAngelTemplates()
	if len(templates) != 5 {
		t.Fatalf("Expected 5 angel templates, got %d", len(templates))
	}

	expectedAngels := map[string]int{
		"guardian": 40,
		"cherub":   20,
		"seraph":   60,
		"gabriel":  100,
		"michael":  160,
	}

	for _, a := range templates {
		expWpm, ok := expectedAngels[a.Rank]
		if !ok {
			t.Errorf("Unexpected angel rank: %s", a.Rank)
		}
		if a.WPM != expWpm {
			t.Errorf("Angel %s expected %d WPM, got %d", a.Rank, expWpm, a.WPM)
		}
	}

	// 2. Summon Michael companion (160 WPM)
	uid := "companion_test_user"
	ang := mgr.SummonCompanion(uid, "michael", "kjv", "Psalm", 23, 1, "The LORD is my shepherd; I shall not want.")
	if ang == nil {
		t.Fatalf("Expected companion to be summoned successfully")
	}
	if ang.Rank != "michael" || ang.Name != "Big Mike" || ang.WPM != 160 {
		t.Errorf("Summoned angel has unexpected stats: %+v", ang)
	}

	// Verify companion is tracked
	mgr.mu.RLock()
	comp, exists := mgr.companions[uid]
	mgr.mu.RUnlock()
	if !exists || comp == nil {
		t.Errorf("Companion not found in manager map")
	}

	// Update companion location
	mgr.UpdateCompanionLocation(uid, "kjv", "Psalm", 23, 2, "He maketh me to lie down in green pastures.")
	comp.mu.RLock()
	if comp.Verse != 2 {
		t.Errorf("Expected updated verse 2, got %d", comp.Verse)
	}
	comp.mu.RUnlock()

	// 3. Dismiss companion
	mgr.DismissCompanion(uid)
	mgr.mu.RLock()
	_, existsAfter := mgr.companions[uid]
	mgr.mu.RUnlock()
	if existsAfter {
		t.Errorf("Expected companion to be removed after dismissal")
	}
}

func TestWebSocketSanctuaryAndAngelIntegration(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatal(err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_sanctuary_ws_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	store = ls
	globalHub = newHub()
	go globalHub.run()
	globalAngelManager = newAngelManager(ls, globalHub)

	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?uid=sanctuary_tester&trans=kjv"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer wsConn.Close()

	readUntilType := func(targetType string) (Message, error) {
		_ = wsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var m Message
			if err := wsConn.ReadJSON(&m); err != nil {
				return m, err
			}
			if m.Type == targetType {
				return m, nil
			}
		}
	}

	// 1. Check sanctuary_state received on connection
	sancMsg, err := readUntilType("sanctuary_state")
	if err != nil {
		t.Fatalf("Failed to read sanctuary_state: %v", err)
	}
	if sancMsg.Sanctuary == nil || sancMsg.Sanctuary.TextColor != "emerald" {
		t.Errorf("Expected default sanctuary text color emerald, got %+v", sancMsg.Sanctuary)
	}
	if len(sancMsg.Unlocks) == 0 {
		t.Errorf("Expected unlocks catalog in sanctuary_state")
	}

	// 2. Check angels_list received on connection
	angelMsg, err := readUntilType("angels_list")
	if err != nil {
		t.Fatalf("Failed to read angels_list: %v", err)
	}
	if len(angelMsg.Angels) != 5 {
		t.Errorf("Expected 5 angels in angels_list, got %d", len(angelMsg.Angels))
	}

	// 3. Save sanctuary customization (with 1 Summon Token to test Big Mike summoning)
	newSettings := SanctuarySettings{
		TextColor:    "amethyst",
		FontFamily:   "serif",
		CaretAura:    "flame",
		AudioProfile: "chime",
		CelebrateFx:  "gold",
		SummonTokens: 1,
	}
	if err := wsConn.WriteJSON(Message{Type: "save_sanctuary", Sanctuary: &newSettings}); err != nil {
		t.Fatal(err)
	}
	savedMsg, err := readUntilType("sanctuary_saved")
	if err != nil {
		t.Fatalf("Failed to read sanctuary_saved: %v", err)
	}
	if savedMsg.Sanctuary == nil || savedMsg.Sanctuary.TextColor != "amethyst" {
		t.Errorf("Expected saved sanctuary to have amethyst, got %+v", savedMsg.Sanctuary)
	}

	// 4. Test angel companion summoning via WebSocket
	// First select a book so we have a target verse
	if err := wsConn.WriteJSON(Message{Type: "select_book", Content: "Romans"}); err != nil {
		t.Fatal(err)
	}
	// Intro or verse will be sent
	_, _ = readUntilType("intro")
	// Start typing intro to unlock Chapter 1
	guide := bookGuides["Romans"]
	if err := wsConn.WriteJSON(Message{Type: "content", Content: guide.Description, WPM: 85, Duration: 3.0}); err != nil {
		t.Fatal(err)
	}
	// Advance to verse 1
	verseMsg, err := readUntilType("verse")
	if err != nil {
		t.Fatalf("Failed to read verse: %v", err)
	}
	if verseMsg.UserCompletions != 0 {
		t.Errorf("Expected 0 user completions on new verse, got %d", verseMsg.UserCompletions)
	}

	// Now summon Michael
	if err := wsConn.WriteJSON(Message{Type: "summon_angel", AngelRank: "michael"}); err != nil {
		t.Fatal(err)
	}
	summonedMsg, err := readUntilType("angel_summoned")
	if err != nil {
		t.Fatalf("Failed to read angel_summoned: %v", err)
	}
	if summonedMsg.Angel == nil || summonedMsg.Angel.Rank != "michael" || summonedMsg.Angel.WPM != 160 {
		t.Errorf("Expected Michael companion with 160 WPM, got %+v", summonedMsg.Angel)
	}

	// 5. Dismiss angel companion via WebSocket
	if err := wsConn.WriteJSON(Message{Type: "dismiss_angel"}); err != nil {
		t.Fatal(err)
	}
	dismissedMsg, err := readUntilType("angel_dismissed")
	if err != nil {
		t.Fatalf("Failed to read angel_dismissed: %v", err)
	}
	if dismissedMsg.Type != "angel_dismissed" {
		t.Errorf("Expected angel_dismissed type")
	}

	// 6. Complete the verse and verify UserCompletions and GlobalCompletions increment
	if err := wsConn.WriteJSON(Message{Type: "content", Content: verseMsg.Content, WPM: 85, Duration: 2.0}); err != nil {
		t.Fatal(err)
	}
	correctMsg, err := readUntilType("correct")
	if err != nil {
		t.Fatalf("Failed to read correct: %v", err)
	}
	if correctMsg.UserCompletions != 1 || correctMsg.GlobalCompletions != 1 {
		t.Errorf("Expected UserCompletions=1, GlobalCompletions=1, got user=%d, global=%d",
			correctMsg.UserCompletions, correctMsg.GlobalCompletions)
	}
}

func TestPersonalGuardianAngelAdaptiveSpeed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_guardian_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}

	hub := newHub()
	mgr := newAngelManager(ls, hub)
	ctx := context.Background()

	uid := "guardian_test_user"

	// 1. Initial user with no stats: default baseline 40 + default 5% pace = 45 WPM
	templates := mgr.GetAngelTemplatesForUser(uid)
	var guardian *AngelInfo
	for i := range templates {
		if templates[i].Rank == "guardian" {
			guardian = &templates[i]
			break
		}
	}
	if guardian == nil {
		t.Fatalf("Guardian angel not found in templates")
	}
	if guardian.WPM != 40 {
		t.Errorf("Expected initial guardian WPM 40, got %d", guardian.WPM)
	}

	// 2. User establishes typing history at 80 WPM
	_ = ls.RecordTypingSession(ctx, uid, "kjv", "Genesis", 100, 100, 80, 5.0)
	_ = ls.RecordTypingSession(ctx, uid, "kjv", "Genesis", 100, 100, 80, 5.0)

	// User configures Guardian with Match (0%) pace and custom name
	_ = ls.SaveCustomizations(ctx, uid, SanctuarySettings{
		GuardianName: "Azrael My Guide",
		GuardianPace: 0,
	})

	templates = mgr.GetAngelTemplatesForUser(uid)
	for i := range templates {
		if templates[i].Rank == "guardian" {
			guardian = &templates[i]
			break
		}
	}
	if guardian.Name != "Azrael My Guide" {
		t.Errorf("Expected guardian name 'Azrael My Guide', got '%s'", guardian.Name)
	}
	// At 80 WPM average with 0% pace offset, guardian should match at 80 WPM
	if guardian.WPM != 80 {
		t.Errorf("Expected guardian WPM 80 matching user, got %d", guardian.WPM)
	}

	// 3. User configures Guardian with -10% gentle pace
	_ = ls.SaveCustomizations(ctx, uid, SanctuarySettings{
		GuardianName: "Azrael My Guide",
		GuardianPace: -10,
	})
	templates = mgr.GetAngelTemplatesForUser(uid)
	for i := range templates {
		if templates[i].Rank == "guardian" {
			guardian = &templates[i]
			break
		}
	}
	// 80 * (1.0 - 0.10) = 72 WPM (within -10%)
	if guardian.WPM != 72 {
		t.Errorf("Expected guardian WPM 72 (-10%%), got %d", guardian.WPM)
	}

	// 4. User configures Guardian with +10% challenge pace
	_ = ls.SaveCustomizations(ctx, uid, SanctuarySettings{
		GuardianName: "Azrael My Guide",
		GuardianPace: 10,
	})
	templates = mgr.GetAngelTemplatesForUser(uid)
	for i := range templates {
		if templates[i].Rank == "guardian" {
			guardian = &templates[i]
			break
		}
	}
	// 80 * (1.0 + 0.10) = 88 WPM (within +10%)
	if guardian.WPM != 88 {
		t.Errorf("Expected guardian WPM 88 (+10%%), got %d", guardian.WPM)
	}

	// 5. Summon companion and test dynamic WPM adjustment
	comp := mgr.SummonCompanion(uid, "guardian", "kjv", "Psalm", 23, 1, "The LORD is my shepherd; I shall not want.")
	if comp == nil || comp.Rank != "guardian" {
		t.Fatalf("Failed to summon guardian companion")
	}
	if comp.WPM != 88 {
		t.Errorf("Expected summoned companion WPM 88, got %d", comp.WPM)
	}

	// User speeds up to higher average WPM; UpdateCompanionWPM should update active companion
	_ = ls.RecordTypingSession(ctx, uid, "kjv", "Genesis", 100, 100, 140, 3.0)
	mgr.UpdateCompanionWPM(uid)
	activeComp := mgr.GetActiveCompanion(uid)
	if activeComp == nil {
		t.Fatalf("Expected active companion")
	}
	if activeComp.WPM <= 88 {
		t.Errorf("Expected active companion WPM to adapt upwards, got %d", activeComp.WPM)
	}
}

func TestBossRaidMechanics(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_boss_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("Failed to create local store: %v", err)
	}
	ctx := context.Background()

	// 1. Initial boss raid state
	raid, err := ls.GetBossRaid(ctx)
	if err != nil {
		t.Fatalf("Failed to get boss raid: %v", err)
	}
	if raid.BossName != "Big Mike" || raid.MaxHP != 5000 || raid.CurrentHP != 5000 {
		t.Fatalf("Unexpected initial boss raid state: %+v", raid)
	}
	if raid.BossWPM != 160 {
		t.Errorf("Expected boss WPM 160, got %d", raid.BossWPM)
	}

	// 2. Record first holy strike: 90 WPM (critical 1.5x = 135 damage)
	updatedRaid, err := ls.RecordBossStrike(ctx, "striker_1", "Faithful Scribe", "#6366f1", 90, 135)
	if err != nil {
		t.Fatalf("Failed to record boss strike: %v", err)
	}
	if updatedRaid.CurrentHP != 5000-135 {
		t.Errorf("Expected HP %d, got %d", 5000-135, updatedRaid.CurrentHP)
	}
	if len(updatedRaid.Conquerors) != 1 {
		t.Errorf("Expected 1 recent conqueror, got %d", len(updatedRaid.Conquerors))
	}

	// 3. Record massive SMITE strike defeating the boss: remaining HP + 100 overkill
	remainingHP := updatedRaid.CurrentHP
	defeatedRaid, err := ls.RecordBossStrike(ctx, "striker_2", "Champion Scribe", "#f59e0b", 160, remainingHP+100)
	if err != nil {
		t.Fatalf("Failed to record defeating strike: %v", err)
	}
	if defeatedRaid.CurrentHP != 0 {
		t.Errorf("Expected 0 HP when defeated, got %d", defeatedRaid.CurrentHP)
	}
	if !defeatedRaid.Conquered {
		t.Errorf("Expected Conquered = true")
	}

	// 4. Reset boss raid
	err = ls.ResetBossRaid(ctx, *defaultBossRaid())
	if err != nil {
		t.Fatalf("Failed to reset boss raid: %v", err)
	}
	resetRaid, err := ls.GetBossRaid(ctx)
	if err != nil {
		t.Fatalf("Failed to get reset boss raid: %v", err)
	}
	if resetRaid.CurrentHP != 5000 || resetRaid.Conquered {
		t.Errorf("Expected reset raid with 5000 HP and not defeated, got %+v", resetRaid)
	}
}

func TestAngelAndDemonLevels(t *testing.T) {
	// Angel Level WPM Tests
	// User requested: "cherub lv1 20wpm, cherub lv2 25 wpm"
	if wpm := GetAngelWPM("cherub", 1); wpm != 20 {
		t.Errorf("Expected Cherub Lv.1 = 20 WPM, got %d", wpm)
	}
	if wpm := GetAngelWPM("cherub", 2); wpm != 25 {
		t.Errorf("Expected Cherub Lv.2 = 25 WPM, got %d", wpm)
	}
	if wpm := GetAngelWPM("cherub", 3); wpm != 30 {
		t.Errorf("Expected Cherub Lv.3 = 30 WPM, got %d", wpm)
	}
	if wpm := GetAngelWPM("cherub", 4); wpm != 35 {
		t.Errorf("Expected Cherub Lv.4 = 35 WPM, got %d", wpm)
	}
	if wpm := GetAngelWPM("cherub", 5); wpm != 40 {
		t.Errorf("Expected Cherub Lv.5 = 40 WPM, got %d", wpm)
	}

	// Seraph levels
	if wpm := GetAngelWPM("seraph", 1); wpm != 50 {
		t.Errorf("Expected Seraph Lv.1 = 50 WPM, got %d", wpm)
	}
	if wpm := GetAngelWPM("seraph", 2); wpm != 60 {
		t.Errorf("Expected Seraph Lv.2 = 60 WPM, got %d", wpm)
	}

	// Gabriel levels
	if wpm := GetAngelWPM("gabriel", 1); wpm != 85 {
		t.Errorf("Expected Gabriel Lv.1 = 85 WPM, got %d", wpm)
	}
	if wpm := GetAngelWPM("gabriel", 2); wpm != 100 {
		t.Errorf("Expected Gabriel Lv.2 = 100 WPM, got %d", wpm)
	}

	// Michael levels
	if wpm := GetAngelWPM("michael", 1); wpm != 140 {
		t.Errorf("Expected Michael Lv.1 = 140 WPM, got %d", wpm)
	}
	if wpm := GetAngelWPM("michael", 2); wpm != 160 {
		t.Errorf("Expected Michael Lv.2 = 160 WPM, got %d", wpm)
	}

	// Demon Level WPM Tests
	if wpm := GetDemonWPM("imp", 1); wpm != 15 {
		t.Errorf("Expected Imp Lv.1 = 15 WPM, got %d", wpm)
	}
	if wpm := GetDemonWPM("imp", 2); wpm != 20 {
		t.Errorf("Expected Imp Lv.2 = 20 WPM, got %d", wpm)
	}
	if wpm := GetDemonWPM("imp", 3); wpm != 25 {
		t.Errorf("Expected Imp Lv.3 = 25 WPM, got %d", wpm)
	}

	if wpm := GetDemonWPM("fiend", 1); wpm != 35 {
		t.Errorf("Expected Fiend Lv.1 = 35 WPM, got %d", wpm)
	}
	if wpm := GetDemonWPM("fiend", 2); wpm != 45 {
		t.Errorf("Expected Fiend Lv.2 = 45 WPM, got %d", wpm)
	}

	if wpm := GetDemonWPM("tormentor", 1); wpm != 70 {
		t.Errorf("Expected Tormentor Lv.1 = 70 WPM, got %d", wpm)
	}
	if wpm := GetDemonWPM("tormentor", 2); wpm != 85 {
		t.Errorf("Expected Tormentor Lv.2 = 85 WPM, got %d", wpm)
	}

	if wpm := GetDemonWPM("apollyon", 1); wpm != 130 {
		t.Errorf("Expected Apollyon Lv.1 = 130 WPM, got %d", wpm)
	}
	if wpm := GetDemonWPM("apollyon", 2); wpm != 160 {
		t.Errorf("Expected Apollyon Lv.2 = 160 WPM, got %d", wpm)
	}
	if wpm := GetDemonWPM("apollyon", 4); wpm != 220 {
		t.Errorf("Expected Apollyon Lv.4 = 220 WPM, got %d", wpm)
	}
}

func TestDemonManager24_7AndAdversary(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_demon_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	h := newHub()

	dm := newDemonManager(ls, h)
	templates := dm.GetDemonTemplates()
	if len(templates) != 4 {
		t.Fatalf("Expected 4 demon templates, got %d", len(templates))
	}

	// Test 24/7 devil status
	vCount, loc, dWpm := dm.GetDevil24_7Status()
	if vCount <= 0 {
		t.Errorf("Expected positive 24/7 verse count, got %d", vCount)
	}
	if dWpm <= 0 || loc == "" {
		t.Errorf("Invalid 24/7 devil status: loc=%s, wpm=%d", loc, dWpm)
	}

	// Test adversary summoning with level
	adv := dm.SummonDemon("user_test_1", "imp", 2, "kjv", "Genesis", 1, 1, "In the beginning God created the heaven and the earth.")
	if adv == nil {
		t.Fatalf("Expected active demon, got nil")
	}
	if adv.Rank != "imp" || adv.Level != 2 || adv.WPM != 20 {
		t.Errorf("Unexpected demon info: %+v", adv)
	}

	active := dm.GetActiveDemon("user_test_1")
	if active == nil || active.Rank != "imp" {
		t.Errorf("Expected active demon in manager: %+v", active)
	}

	// Update location
	dm.UpdateDemonLocation("user_test_1", "kjv", "Genesis", 1, 2, "And the earth was without form, and void;")
	activeUpdated := dm.GetActiveDemon("user_test_1")
	if activeUpdated.Chapter != 1 || activeUpdated.Verse != 2 {
		t.Errorf("Failed to update demon location: %+v", activeUpdated)
	}

	// Dismiss demon
	dm.DismissDemon("user_test_1")
	if dm.GetActiveDemon("user_test_1") != nil {
		t.Errorf("Expected nil active demon after dismissal")
	}
}

func TestBossRaidLevelsAndDemonBoss(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_boss_lvl_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	ctx := context.Background()

	// Michael tiers
	michaelLv1, err := ls.GetBossRaidForLevel(ctx, 1, false)
	if err != nil || michaelLv1.BossWPM != 140 || michaelLv1.MaxHP != 3500 || michaelLv1.IsDemonBoss {
		t.Errorf("Invalid Michael Lv.1: %+v, err: %v", michaelLv1, err)
	}

	michaelLv4, err := ls.GetBossRaidForLevel(ctx, 4, false)
	if err != nil || michaelLv4.BossWPM != 200 || michaelLv4.MaxHP != 10000 || michaelLv4.IsDemonBoss {
		t.Errorf("Invalid Michael Lv.4: %+v, err: %v", michaelLv4, err)
	}

	// Apollyon (Demonic Boss) tiers
	apollyonLv1, err := ls.GetBossRaidForLevel(ctx, 1, true)
	if err != nil || apollyonLv1.BossWPM != 130 || apollyonLv1.MaxHP != 4000 || !apollyonLv1.IsDemonBoss {
		t.Errorf("Invalid Apollyon Lv.1: %+v, err: %v", apollyonLv1, err)
	}

	apollyonLv2, err := ls.GetBossRaidForLevel(ctx, 2, true)
	if err != nil || apollyonLv2.BossWPM != 160 || apollyonLv2.MaxHP != 6000 || !apollyonLv2.IsDemonBoss {
		t.Errorf("Invalid Apollyon Lv.2: %+v, err: %v", apollyonLv2, err)
	}

	apollyonLv4, err := ls.GetBossRaidForLevel(ctx, 4, true)
	if err != nil || apollyonLv4.BossWPM != 220 || apollyonLv4.MaxHP != 12000 || !apollyonLv4.IsDemonBoss {
		t.Errorf("Invalid Apollyon Lv.4: %+v, err: %v", apollyonLv4, err)
	}
}

func TestWildDemonsRoamingAndCombat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_wild_demon_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	h := newHub()

	dm := newDemonManager(ls, h)
	demons := dm.GetWildDemons()
	if len(demons) < 5 {
		t.Fatalf("Expected at least 5 wild demons, got %d", len(demons))
	}

	// Verify all wild demons type at slow methodical pace (10 - 24 WPM)
	for _, d := range demons {
		if d.WPM < 10 || d.WPM > 24 {
			t.Errorf("Wild demon %s has WPM %d outside slow methodical range (10-24)", d.Name, d.WPM)
		}
		if d.MaxHP <= 0 || d.CurrentHP <= 0 {
			t.Errorf("Wild demon %s has invalid HP: %d / %d", d.Name, d.CurrentHP, d.MaxHP)
		}
	}

	// Test locating a wild demon at a specific verse
	genesisDemon := dm.GetWildDemonAt("kjv", "Genesis", 3, 1)
	if genesisDemon == nil {
		t.Fatalf("Expected wild demon at Genesis 3:1")
	}
	if genesisDemon.Rank != "imp" || genesisDemon.Level != 1 {
		t.Errorf("Unexpected Genesis demon: %+v", genesisDemon)
	}

	// Test Holy Resonance Damage Strike
	initialHP := genesisDemon.CurrentHP
	updated, defeated := dm.ApplyWildDemonDamage(genesisDemon.ID, 25)
	if defeated {
		t.Errorf("Demon should not be defeated by 25 dmg from %d HP", initialHP)
	}
	if updated.CurrentHP != initialHP-25 {
		t.Errorf("Expected HP %d, got %d", initialHP-25, updated.CurrentHP)
	}

	// Test Mistake Backlash: Heals BacklashHealPct max HP, increases WPM by +2, enrage count increments
	baseWPM := updated.WPM
	enraged := dm.EnrageWildDemonOnMistake(genesisDemon.ID)
	if enraged.WPM != baseWPM+2 {
		t.Errorf("Expected WPM %d after enrage, got %d", baseWPM+2, enraged.WPM)
	}
	expectedHeal := int(float64(enraged.MaxHP) * DefaultDemonSyncBalance.BacklashHealPct)
	if enraged.CurrentHP != updated.CurrentHP+expectedHeal {
		t.Errorf("Expected enraged HP %d, got %d", updated.CurrentHP+expectedHeal, enraged.CurrentHP)
	}
	if enraged.EnrageCount != 1 {
		t.Errorf("Expected EnrageCount 1, got %d", enraged.EnrageCount)
	}

	// Test Defeating the demon
	updatedFinal, defeatedFinal := dm.ApplyWildDemonDamage(genesisDemon.ID, enraged.CurrentHP)
	if !defeatedFinal || updatedFinal.CurrentHP != 0 {
		t.Errorf("Expected demon to be defeated, got defeated=%v, hp=%d", defeatedFinal, updatedFinal.CurrentHP)
	}

	// Test Respawn resets HP, WPM, and enrage count
	respawned := dm.RespawnWildDemon(genesisDemon.ID)
	if respawned == nil || respawned.CurrentHP != respawned.MaxHP || respawned.EnrageCount != 0 {
		t.Errorf("Invalid respawn state: %+v", respawned)
	}
}

func TestGuardianAngelProgressionAndTrackerUnlock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_guardian_prog_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	ctx := context.Background()
	testUID := "user_hunter_test"

	// Initial customizations
	initial, err := ls.GetCustomizations(ctx, testUID)
	if err != nil {
		t.Fatal(err)
	}
	if initial.DemonsDefeated != 0 || initial.GuardianLevel != 1 || initial.GuardianWards != 0 || initial.TrackerUnlocked {
		t.Errorf("Unexpected initial settings: %+v", initial)
	}

	// Defeat 5 wild demons: tracker must remain locked
	for i := 1; i <= 5; i++ {
		settings, _, err := ls.RecordWildDemonDefeat(ctx, testUID, "imp", 1)
		if err != nil {
			t.Fatalf("Defeat %d failed: %v", i, err)
		}
		if settings.DemonsDefeated != i {
			t.Errorf("Expected %d demons defeated, got %d", i, settings.DemonsDefeated)
		}
		if settings.TrackerUnlocked {
			t.Errorf("Tracker should be locked at %d defeats", i)
		}
	}

	// Level and Holy Wards calculation:
	// Lv. 1-2: 0 wards, Lv. 3-5: 1 ward, Lv. 6-8: 2 wards
	midSettings, _ := ls.GetCustomizations(ctx, testUID)
	expectedWards := (midSettings.GuardianLevel - 1) / 3
	if midSettings.GuardianWards != expectedWards {
		t.Errorf("Expected %d Holy Wards for Level %d, got %d", expectedWards, midSettings.GuardianLevel, midSettings.GuardianWards)
	}

	// Defeat 6th demon: Tracker MUST unlock!
	finalSettings, _, err := ls.RecordWildDemonDefeat(ctx, testUID, "fiend", 2)
	if err != nil {
		t.Fatalf("6th defeat failed: %v", err)
	}
	if finalSettings.DemonsDefeated != 6 {
		t.Errorf("Expected 6 demons defeated, got %d", finalSettings.DemonsDefeated)
	}
	if !finalSettings.TrackerUnlocked {
		t.Errorf("Tracker MUST be unlocked after defeating 6 demons in the wild!")
	}
}

func TestWildDemonsWebSocketFlow(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatalf("Failed to load translations: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_ws_wild_demon_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	store = ls

	globalHub = newHub()
	go globalHub.run()
	globalDemonManager = newDemonManager(store, globalHub)
	globalAngelManager = newAngelManager(store, globalHub)

	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect websocket: %v", err)
	}
	defer wsConn.Close()

	readUntilType := func(msgType string) (*Message, error) {
		_ = wsConn.SetReadDeadline(time.Now().Add(4 * time.Second))
		for {
			var m Message
			err := wsConn.ReadJSON(&m)
			if err != nil {
				return nil, fmt.Errorf("error waiting for %s: %w", msgType, err)
			}
			if m.Type == msgType {
				return &m, nil
			}
		}
	}

	// 1. Initial Tracker check: should be locked with 0 defeats
	if err := wsConn.WriteJSON(Message{Type: "get_wild_demons"}); err != nil {
		t.Fatal(err)
	}
	listMsg, err := readUntilType("wild_demons_list")
	if err != nil {
		t.Fatalf("Failed to read wild_demons_list: %v", err)
	}
	if listMsg.TrackerUnlocked {
		t.Errorf("Expected TrackerUnlocked=false initially")
	}
	if listMsg.DemonsDefeated != 0 {
		t.Errorf("Expected DemonsDefeated=0, got %d", listMsg.DemonsDefeated)
	}

	// 2. Test Confront Wild Demon via confront_wild_demon message
	demons := globalDemonManager.GetWildDemons()
	if len(demons) == 0 {
		t.Fatalf("No wild demons roaming")
	}
	targetDemon := demons[0]

	if err := wsConn.WriteJSON(Message{Type: "confront_wild_demon", DemonID: targetDemon.ID}); err != nil {
		t.Fatal(err)
	}
	presentMsg, err := readUntilType("wild_demon_present")
	if err != nil {
		t.Fatalf("Failed to read wild_demon_present: %v", err)
	}
	if presentMsg.WildDemon == nil || presentMsg.WildDemon.ID != targetDemon.ID {
		t.Errorf("Expected wild demon present with ID %s, got %+v", targetDemon.ID, presentMsg.WildDemon)
	}

	// 4. Test Wild Demon Strike
	if err := wsConn.WriteJSON(Message{Type: "wild_demon_strike", DemonID: targetDemon.ID, Damage: 20}); err != nil {
		t.Fatal(err)
	}
	updateMsg, err := readUntilType("wild_demon_update")
	if err != nil {
		t.Fatalf("Failed to read wild_demon_update: %v", err)
	}
	if updateMsg.WildDemon.CurrentHP >= targetDemon.MaxHP {
		t.Errorf("Expected damage applied, got hp=%d", updateMsg.WildDemon.CurrentHP)
	}

	// 5. Test Mistake Backlash Enrage
	if err := wsConn.WriteJSON(Message{Type: "demon_mistake_backlash", DemonID: targetDemon.ID}); err != nil {
		t.Fatal(err)
	}
	enrageMsg, err := readUntilType("wild_demon_enraged")
	if err != nil {
		t.Fatalf("Failed to read wild_demon_enraged: %v", err)
	}
	if enrageMsg.WildDemon.WPM != targetDemon.WPM+2 {
		t.Errorf("Expected enraged WPM %d, got %d", targetDemon.WPM+2, enrageMsg.WildDemon.WPM)
	}
}

func TestWildDemonIntervalSpawningAndProgressEncounters(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatalf("Failed to load translations: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_spawn_encounter_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	hub := newHub()

	dm := newDemonManager(ls, hub)
	defer dm.Stop()

	// 1. Test Encounter Chance Scaling:
	// - Opening verses (0, 1): 0.0% chance
	c0 := CalculateEncounterChance(0, 100)
	c1 := CalculateEncounterChance(1, 100)
	if c0 != 0.0 || c1 != 0.0 {
		t.Errorf("Expected 0.0 encounter chance for opening verses, got c0=%.2f, c1=%.2f", c0, c1)
	}

	// - Monotonic increase with progress (the more verses completed, the higher the chance)
	c10 := CalculateEncounterChance(10, 100)
	c30 := CalculateEncounterChance(30, 100)
	c70 := CalculateEncounterChance(70, 100)
	c99 := CalculateEncounterChance(99, 100)

	if c10 <= 0.05 {
		t.Errorf("Expected c10 > 0.05, got %.3f", c10)
	}
	if c30 <= c10 {
		t.Errorf("Expected c30 (%.3f) > c10 (%.3f)", c30, c10)
	}
	if c70 <= c30 {
		t.Errorf("Expected c70 (%.3f) > c30 (%.3f)", c70, c30)
	}
	if c99 <= c70 {
		t.Errorf("Expected c99 (%.3f) > c70 (%.3f)", c99, c70)
	}
	if c99 > 0.50 {
		t.Errorf("Expected chance capped at 0.50, got %.3f", c99)
	}

	// 2. Test Periodic Interval Spawning:
	dm.SetMaxWildDemons(15)
	initialCount := len(dm.GetWildDemons())
	newDemon := dm.SpawnPeriodicWildDemon()
	if newDemon == nil {
		t.Fatalf("Expected SpawnPeriodicWildDemon to spawn a demon")
	}
	if len(dm.GetWildDemons()) != initialCount+1 {
		t.Errorf("Expected wild demons count to increase by 1, got %d", len(dm.GetWildDemons()))
	}
	if newDemon.WPM < 10 || newDemon.WPM > 24 {
		t.Errorf("Expected slow methodical WPM (10-24), got %d", newDemon.WPM)
	}

	// 3. Test Demon "Lives Until Defeated":
	// The demon stays alive at its verse until its HP is reduced to 0
	found := dm.GetWildDemon(newDemon.ID)
	if found == nil || found.CurrentHP <= 0 {
		t.Fatalf("Demon should be alive and roaming: %+v", found)
	}

	// Deal partial damage: demon remains alive in the world
	updated, defeated := dm.ApplyWildDemonDamage(newDemon.ID, 30)
	if defeated || updated.CurrentHP != newDemon.MaxHP-30 {
		t.Errorf("Demon should not be defeated from partial damage: hp=%d", updated.CurrentHP)
	}
	if dm.GetWildDemon(newDemon.ID) == nil {
		t.Errorf("Demon should still reside in active demons after partial damage")
	}

	// Deal lethal damage: demon is defeated and removed from active world!
	updatedLethal, defeatedLethal := dm.ApplyWildDemonDamage(newDemon.ID, updated.CurrentHP)
	if !defeatedLethal || updatedLethal.CurrentHP != 0 {
		t.Errorf("Expected demon to be defeated on lethal damage")
	}
	if dm.GetWildDemon(newDemon.ID) != nil {
		t.Errorf("Defeated demon must be removed from active wild demons (lives until defeated)")
	}

	// 4. Test CheckOrSpawnWildDemonForUser on a verse:
	testVerse := Verse{
		BookName: "Ephesians",
		Chapter:  6,
		Verse:    12,
		Text:     "For we wrestle not against flesh and blood, but against principalities, against powers...",
	}
	// At verse 0 in book, encounter chance is 0
	dEarly, chanceEarly, _ := dm.CheckOrSpawnWildDemonForUser("kjv", testVerse, 0, 155)
	if dEarly != nil || chanceEarly != 0.0 {
		t.Errorf("Expected nil demon and 0 chance at verse 0, got demon=%v, chance=%.2f", dEarly, chanceEarly)
	}

	// When an existing demon is at a verse, it is consistently returned (lives until defeated)
	freshDemon := dm.SpawnPeriodicWildDemon()
	if freshDemon != nil {
		existingVerse := Verse{
			BookName: freshDemon.BookName,
			Chapter:  freshDemon.Chapter,
			Verse:    freshDemon.Verse,
			Text:     freshDemon.Text,
		}
		foundExisting, chanceExisting, isAmbush := dm.CheckOrSpawnWildDemonForUser("kjv", existingVerse, 50, 100)
		if foundExisting == nil || foundExisting.ID != freshDemon.ID {
			t.Errorf("Expected to encounter existing live demon %s", freshDemon.ID)
		}
		if chanceExisting != 1.0 || isAmbush {
			t.Errorf("Existing demon should have 1.0 encounter rate and isAmbush=false")
		}
	}

	// Verify dynamic ambush spawn when globalDemonManager is set and hub broadcasts wild_demons_list_update (regression test for mutex deadlock)
	setGlobalDemonManager(dm)
	dm.SetMaxWildDemons(100)
	ambushVerse := Verse{
		BookName: "Leviticus",
		Chapter:  19,
		Verse:    18,
		Text:     "Thou shalt not avenge, nor bear any grudge against the children of thy people, but thou shalt love thy neighbour as thyself: I am the LORD.",
	}
	var spawnedAmbush *WildDemon
	for attempt := 0; attempt < 100; attempt++ {
		dAmbush, chAmbush, isAmb := dm.CheckOrSpawnWildDemonForUser("kjv", ambushVerse, 80, 100)
		if chAmbush <= 0 {
			t.Fatalf("Expected positive encounter chance at verse 80/100, got %.2f", chAmbush)
		}
		if dAmbush != nil && isAmb {
			spawnedAmbush = dAmbush
			break
		}
	}
	if spawnedAmbush == nil {
		t.Fatalf("Expected dynamic ambush demon to spawn within 100 attempts at 50%% encounter rate")
	}
}

func TestDemonBalancingSwarmsAndEvolution(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatalf("Failed to load translations: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_swarm_evolve_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	hub := newHub()
	dm := newDemonManager(ls, hub)
	defer dm.Stop()

	// 1. Dynamic Inverse Pressure Spawn Interval Verification
	// Initial demons: 7 (<66), interval should be 45s
	if interval := dm.GetDynamicSpawnInterval(); interval != 45*time.Second {
		t.Errorf("Expected 45s interval for <66 demons, got %v", interval)
	}

	// Manually set demons slice to test interval thresholds
	dm.mu.Lock()
	originalDemons := dm.wildDemons

	// 66 to 199 -> 90s
	dm.wildDemons = make([]*WildDemon, 100)
	for i := range dm.wildDemons {
		dm.wildDemons[i] = &WildDemon{ID: fmt.Sprintf("test-%d", i), CurrentHP: 100, MaxHP: 100}
	}
	dm.mu.Unlock()
	if interval := dm.GetDynamicSpawnInterval(); interval != 90*time.Second {
		t.Errorf("Expected 90s interval for 100 demons, got %v", interval)
	}

	// 200 to 499 -> 150s
	dm.mu.Lock()
	dm.wildDemons = make([]*WildDemon, 350)
	for i := range dm.wildDemons {
		dm.wildDemons[i] = &WildDemon{ID: fmt.Sprintf("test-%d", i), CurrentHP: 100, MaxHP: 100}
	}
	dm.mu.Unlock()
	if interval := dm.GetDynamicSpawnInterval(); interval != 150*time.Second {
		t.Errorf("Expected 150s interval for 350 demons, got %v", interval)
	}

	// 500 to 599 -> 240s
	dm.mu.Lock()
	dm.wildDemons = make([]*WildDemon, 550)
	for i := range dm.wildDemons {
		dm.wildDemons[i] = &WildDemon{ID: fmt.Sprintf("test-%d", i), CurrentHP: 100, MaxHP: 100}
	}
	dm.mu.Unlock()
	if interval := dm.GetDynamicSpawnInterval(); interval != 240*time.Second {
		t.Errorf("Expected 240s interval for 550 demons, got %v", interval)
	}

	// >= 600 -> 360s
	dm.mu.Lock()
	dm.wildDemons = make([]*WildDemon, 620)
	for i := range dm.wildDemons {
		dm.wildDemons[i] = &WildDemon{ID: fmt.Sprintf("test-%d", i), CurrentHP: 100, MaxHP: 100}
	}
	dm.mu.Unlock()
	if interval := dm.GetDynamicSpawnInterval(); interval != 360*time.Second {
		t.Errorf("Expected 360s interval for 620 demons, got %v", interval)
	}

	// 2. World Cap of 666 Enforcement
	dm.mu.Lock()
	dm.wildDemons = make([]*WildDemon, 666)
	for i := range dm.wildDemons {
		dm.wildDemons[i] = &WildDemon{ID: fmt.Sprintf("cap-%d", i), CurrentHP: 100, MaxHP: 100}
	}
	dm.mu.Unlock()
	// At 666 cap, SpawnPeriodicWildDemon must return nil and not exceed 666
	overCapDemon := dm.SpawnPeriodicWildDemon()
	if overCapDemon != nil {
		t.Errorf("SpawnPeriodicWildDemon must not exceed 666 cap, got spawned demon")
	}
	if len(dm.GetWildDemons()) != 666 {
		t.Errorf("Expected exactly 666 demons at cap, got %d", len(dm.GetWildDemons()))
	}

	// Swarm triggers should also honor 666 cap
	miniSpawned := dm.TriggerMiniSwarm("Genesis")
	if miniSpawned != 0 {
		t.Errorf("TriggerMiniSwarm should spawn 0 demons when at 666 cap, got %d", miniSpawned)
	}
	majorSpawned := dm.TriggerMajorSwarm()
	if majorSpawned != 0 {
		t.Errorf("TriggerMajorSwarm should spawn 0 demons when at 666 cap, got %d", majorSpawned)
	}

	// CheckOrSpawnWildDemonForUser should also honor 666 cap
	vTest := Verse{BookName: "Romans", Chapter: 8, Verse: 38, Text: "For I am persuaded, that neither death, nor life..."}
	dCap, _, _ := dm.CheckOrSpawnWildDemonForUser("kjv", vTest, 90, 100)
	if dCap != nil {
		t.Errorf("CheckOrSpawnWildDemonForUser should not spawn new demon when at 666 cap")
	}

	// Restore manageable demons slice
	dm.mu.Lock()
	dm.wildDemons = originalDemons
	dm.mu.Unlock()

	// 3. Demon Aging and Evolution Test
	// Create an uncontested demon with past LastEvolve
	testDemon := &WildDemon{
		ID:         "evolve-test-1",
		Rank:       "fiend",
		Level:      1,
		MaxLevel:   5,
		Name:       "Test Roaming Fiend",
		Title:      "Stumbling Stone",
		WPM:        15,
		MaxHP:      150,
		CurrentHP:  150,
		SpawnedAt:  time.Now().UTC().Add(-45 * time.Minute),
		LastEvolve: time.Now().UTC().Add(-20 * time.Minute), // older than evolveInterval (15m)
	}
	dm.mu.Lock()
	dm.wildDemons = append(dm.wildDemons, testDemon)
	dm.mu.Unlock()

	evolvedList := dm.EvolveLivingDemons()
	foundEvolved := false
	for _, ed := range evolvedList {
		if ed.ID == testDemon.ID {
			foundEvolved = true
			if ed.Level != 2 {
				t.Errorf("Expected evolved level 2, got %d", ed.Level)
			}
			if ed.MaxHP != 200 || ed.CurrentHP != 200 {
				t.Errorf("Expected MaxHP & CurrentHP 200 (+50), got max=%d cur=%d", ed.MaxHP, ed.CurrentHP)
			}
			if ed.WPM != 16 {
				t.Errorf("Expected WPM 16 (+1), got %d", ed.WPM)
			}
			if !strings.HasPrefix(ed.Title, "Awakened ") {
				t.Errorf("Expected title prefix 'Awakened ', got %s", ed.Title)
			}
		}
	}
	if !foundEvolved {
		t.Errorf("Expected testDemon to be included in evolved list")
	}

	// Test reaching MaxLevel cap (5)
	dm.mu.Lock()
	testDemon.Level = 5
	testDemon.MaxLevel = 5
	testDemon.LastEvolve = time.Now().UTC().Add(-20 * time.Minute)
	dm.mu.Unlock()

	evolvedAgain := dm.EvolveLivingDemons()
	for _, ed := range evolvedAgain {
		if ed.ID == testDemon.ID {
			t.Errorf("Demon at MaxLevel (5) should not evolve further, got level %d", ed.Level)
		}
	}

	// 4. Swarm Incursions with capacity available
	dm.mu.Lock()
	dm.wildDemons = dm.wildDemons[:5]
	dm.maxWildDemons = 666
	dm.mu.Unlock()

	preMiniCount := len(dm.GetWildDemons())
	miniCount := dm.TriggerMiniSwarm("Prophecy")
	if miniCount < 12 || miniCount > 18 {
		t.Errorf("TriggerMiniSwarm should spawn between 12 and 18 demons, got %d", miniCount)
	}
	postMiniCount := len(dm.GetWildDemons())
	if postMiniCount != preMiniCount+miniCount {
		t.Errorf("Expected demon count %d, got %d", preMiniCount+miniCount, postMiniCount)
	}
}

func TestCanonicalVerseAdvancementAndMichaelSlay(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatalf("Failed to load translations: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_canonical_michael_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	hub := newHub()
	dm := newDemonManager(ls, hub)
	defer dm.Stop()

	// 1. Test getNextCanonicalVerse
	nextGen, ok := getNextCanonicalVerse("kjv", "Genesis", 3, 1)
	if !ok || nextGen.BookName != "Genesis" || nextGen.Chapter != 3 || nextGen.Verse != 2 {
		t.Errorf("Expected Genesis 3:2 after Genesis 3:1, got %+v", nextGen)
	}

	// Test wrap-around from Revelation 22:21 to Genesis 1:1
	nextWrap, ok := getNextCanonicalVerse("kjv", "Revelation", 22, 21)
	if !ok || nextWrap.BookName != "Genesis" || nextWrap.Chapter != 1 || nextWrap.Verse != 1 {
		t.Errorf("Expected wrap-around to Genesis 1:1 after Revelation 22:21, got %+v", nextWrap)
	}

	// 2. Test AdvanceWildDemonToNextVerse when a wild demon completes a verse
	advancedDemon, nextV, moved := dm.AdvanceWildDemonToNextVerse("wild-imp-1")
	if !moved || advancedDemon == nil {
		t.Fatalf("Expected wild-imp-1 to advance to next canonical verse")
	}
	if advancedDemon.BookName != "Genesis" || advancedDemon.Chapter != 3 || advancedDemon.Verse != 2 || nextV.Verse != 2 {
		t.Errorf("Expected wild-imp-1 to advance to Genesis 3:2, got %s %d:%d", advancedDemon.BookName, advancedDemon.Chapter, advancedDemon.Verse)
	}

	// 3. Test SlayWildDemonByMichael when Michael completes the verse where the demon lurks
	slain := dm.SlayWildDemonByMichael("kjv", advancedDemon.BookName, advancedDemon.Chapter, advancedDemon.Verse)
	if slain == nil || slain.ID != "wild-imp-1" {
		t.Fatalf("Expected Michael to slay wild-imp-1 at %s %d:%d, got %+v", advancedDemon.BookName, advancedDemon.Chapter, advancedDemon.Verse, slain)
	}
	if dm.GetWildDemon("wild-imp-1") != nil {
		t.Errorf("Expected wild-imp-1 to be removed from active wild demons after being slain by Michael")
	}
}

func TestSpanishTranslationGuidesAndLenientMatching(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatalf("Failed to load translations: %v", err)
	}

	rvr, ok := translations["rvr1909"]
	if !ok {
		t.Fatalf("Expected rvr1909 translation to be loaded")
	}
	if len(rvr.Verses) != 31084 {
		t.Errorf("Expected 31084 verses in rvr1909, got %d", len(rvr.Verses))
	}
	if rvr.Verses[0].Text != "En el principio crió Dios los cielos y la tierra." {
		t.Errorf("Unexpected Genesis 1:1 in rvr1909: %q", rvr.Verses[0].Text)
	}

	// Verify lenient matching rules for Spanish accents, ñ, ü, and inverted punctuation
	target := "¿Adónde huiré de tu espíritu? ¡Bendito el Señor, que oyó mi oración y mi vergüenza quitó!"
	exactInput := "¿Adónde huiré de tu espíritu? ¡Bendito el Señor, que oyó mi oración y mi vergüenza quitó!"
	plainNoInverted := "Adonde huire de tu espiritu? Bendito el Senor, que oyo mi oracion y mi verguenza quito!"
	wrongWords := "Adonde huire de tu espiritu? Bendito el Rey, que oyo mi oracion y mi verguenza quito!"

	if !matchesPassageText(exactInput, target) {
		t.Errorf("Expected exact Spanish input to match target")
	}
	if !matchesPassageText(plainNoInverted, target) {
		t.Errorf("Expected plain unaccented Spanish input without ¿/¡ to match target")
	}
	if matchesPassageText(wrongWords, target) {
		t.Errorf("Expected wrong words not to match target")
	}
}

func TestBigMikeRenameAndDemonSyncStacks(t *testing.T) {
	// 1. Verify Big Mike rename in Angel templates
	tpl, ok := angelTemplates["michael"]
	if !ok || tpl.Name != "Big Mike" {
		t.Fatalf("Expected michael template to be named 'Big Mike', got %+v", tpl)
	}

	// 2. Verify Sync Stack Damage Scaling (every 5 stacks increases damage)
	// Base damage 1 (Level 1 Guardian)
	d0 := CalculateSyncStackDamage(1, 0)
	d4 := CalculateSyncStackDamage(1, 4)
	d5 := CalculateSyncStackDamage(1, 5)
	d9 := CalculateSyncStackDamage(1, 9)
	d10 := CalculateSyncStackDamage(1, 10)
	d15 := CalculateSyncStackDamage(1, 15)

	if d0 != 1 || d4 != 1 {
		t.Errorf("Expected 0-4 stacks to deal base damage 1, got d0=%d, d4=%d", d0, d4)
	}
	if d5 <= d4 || d9 != d5 {
		t.Errorf("Expected 5-9 stacks to increase damage over 0-4 stacks, got d4=%d, d5=%d, d9=%d", d4, d5, d9)
	}
	if d10 <= d9 {
		t.Errorf("Expected 10 stacks to increase damage over 5-9 stacks, got d9=%d, d10=%d", d9, d10)
	}
	if d15 <= d10 {
		t.Errorf("Expected 15 stacks to increase damage over 10 stacks, got d10=%d, d15=%d", d10, d15)
	}

	// Base damage 10 (Higher power)
	h0 := CalculateSyncStackDamage(10, 0)
	h5 := CalculateSyncStackDamage(10, 5)
	h10 := CalculateSyncStackDamage(10, 10)
	if h0 != 10 || h5 <= h0 || h10 <= h5 {
		t.Errorf("Expected monotonic damage scaling every 5 stacks for base 10: h0=%d, h5=%d, h10=%d", h0, h5, h10)
	}

	// 3. Verify custom DemonSyncBalanceConfig for future balancing
	customCfg := DemonSyncBalanceConfig{
		StacksPerWord:       1,
		StacksPerDamageTier: 5,
		FlatDamagePerTier:   3,
		DamageMultPerTier:   0.50,
		MaxStacks:           10,
		CharSyncTolerance:   5,
	}
	if got := CalculateSyncStackDamageWithConfig(2, 5, customCfg); got != 5 {
		t.Errorf("Expected custom config at 5 stacks (base 2 + 3 flat) = 5, got %d", got)
	}
	// MaxStacks cap at 10 (2 tiers) even if 20 stacks passed
	if got := CalculateSyncStackDamageWithConfig(2, 20, customCfg); got != 8 {
		t.Errorf("Expected custom config capped at 10 stacks (base 2 + 2*3 flat) = 8, got %d", got)
	}

	// 4. Verify Widened Radiance Window (±5 chars) & Dynamic Demon WPM Scaling
	if DefaultDemonSyncBalance.CharSyncTolerance != 5 {
		t.Errorf("Expected widened radiance window CharSyncTolerance=5, got %d", DefaultDemonSyncBalance.CharSyncTolerance)
	}

	baseWPM := 14
	// At equal position and pace -> stays at baseWPM
	if w := CalculateDynamicDemonWPM(baseWPM, 20, 20, 14); w != baseWPM {
		t.Errorf("Expected base WPM %d when tied, got %d", baseWPM, w)
	}
	// User typing 5 chars ahead -> demon WPM increases!
	wAhead5 := CalculateDynamicDemonWPM(baseWPM, 25, 20, 22)
	if wAhead5 <= baseWPM {
		t.Errorf("Expected demon WPM to increase when user is 5 chars ahead, got %d (base %d)", wAhead5, baseWPM)
	}
	// User typing 11 chars ahead -> demon WPM increases even further!
	wAhead11 := CalculateDynamicDemonWPM(baseWPM, 31, 20, 30)
	if wAhead11 <= wAhead5 {
		t.Errorf("Expected demon WPM at +11 chars (%d) > at +5 chars (%d)", wAhead11, wAhead5)
	}
	// User typing slower / 5 chars behind -> demon WPM decreases!
	wBehind5 := CalculateDynamicDemonWPM(baseWPM, 15, 20, 10)
	if wBehind5 >= baseWPM {
		t.Errorf("Expected demon WPM to decrease when user is 5 chars behind, got %d (base %d)", wBehind5, baseWPM)
	}
	// User typing slower inside window -> demon WPM also eases down
	wSlowerInWin := CalculateDynamicDemonWPM(baseWPM, 18, 20, 8)
	if wSlowerInWin >= baseWPM {
		t.Errorf("Expected demon WPM to ease down when user types slower behind demon, got %d (base %d)", wSlowerInWin, baseWPM)
	}

	// 5. Verify Punctuation Checkpoints
	sampleVerse := "Be sober, be vigilant; because your adversary the devil, as a roaring lion, walketh about:"
	// Before 2nd punctuation mark (only 1st comma at index 8 crossed): checkpoint still 0
	cp1, pCount1 := CalculatePunctuationCheckpoint(sampleVerse, 15)
	if pCount1 != 1 || cp1 != 0 {
		t.Errorf("Expected 1 punctuation mark and checkpoint 0 at len 15, got punct=%d, cp=%d", pCount1, cp1)
	}
	// After 2nd punctuation mark (semicolon at index 21 crossed): checkpoint locks in after '; '!
	cp2, pCount2 := CalculatePunctuationCheckpoint(sampleVerse, 25)
	if pCount2 != 2 || cp2 != 23 {
		t.Errorf("Expected 2 punctuation marks and checkpoint 23 ('because...'), got punct=%d, cp=%d", pCount2, cp2)
	}
	// After 4th punctuation mark (comma after 'lion' at index 74 crossed): checkpoint advances again!
	cp4, pCount4 := CalculatePunctuationCheckpoint(sampleVerse, 80)
	if pCount4 != 4 || cp4 <= cp2 {
		t.Errorf("Expected 4 punctuation marks and advanced checkpoint > %d, got punct=%d, cp=%d", cp2, pCount4, cp4)
	}
}

func TestVoiceModeSpokenMatchingAndWebSocketSubmission(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatal(err)
	}

	// 1. Unit test normalizeSpokenForMatch and matchesSpokenPassage
	target1 := "In the beginning God created the heaven and the earth."
	spoken1 := "in the beginning god created the heaven and the earth"
	if !matchesSpokenPassage(spoken1, target1) {
		t.Errorf("Expected spoken passage %q to match target %q", spoken1, target1)
	}

	target2 := "\"Come,\" he said, 'follow me!' - and they went."
	spoken2 := "come he said follow me and they went"
	if !matchesSpokenPassage(spoken2, target2) {
		t.Errorf("Expected spoken passage %q to match punctuated target %q", spoken2, target2)
	}

	targetES := "En el principio creó Dios los cielos y la tierra."
	spokenES := "en el principio creo dios los cielos y la tierra"
	if !matchesSpokenPassage(spokenES, targetES) {
		t.Errorf("Expected Spanish spoken passage %q to match target %q", spokenES, targetES)
	}

	if matchesSpokenPassage("in the beginning god created", target1) {
		t.Errorf("Expected incomplete spoken passage not to match full target")
	}

	// 2. Verify frontend Voice Mode elements and engine in embedded index.html
	htmlBytes, err := appFS.ReadFile("index.html")
	if err != nil {
		t.Fatalf("Failed to read index.html: %v", err)
	}
	htmlStr := string(htmlBytes)
	requiredSnippets := []string{
		`id="voiceModeBar"`,
		`id="btnToggleVoiceMode"`,
		`id="btnVoiceMic"`,
		`id="voiceNextWordPill"`,
		`id="voiceTranscriptPreview"`,
		`id="btnSpeakVerseTTS"`,
		`id="btnVoiceSkipWord"`,
		`id="voiceAutoSubmitCheck"`,
		`function processSpokenTranscript(`,
		`function voiceWordsMatch(`,
		`voice_herald`,
	}
	for _, snip := range requiredSnippets {
		if !strings.Contains(htmlStr, snip) {
			t.Errorf("Expected index.html to contain Voice Mode snippet %q", snip)
		}
	}

	// 3. WebSocket flow test with VoiceMode: true
	tmpDir, err := os.MkdirTemp("", "elespe_voice_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	store = ls

	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?uid=test_voice_user&trans=kjv"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer wsConn.Close()

	readUntilType := func(targetType string) (Message, error) {
		for {
			var m Message
			if err := wsConn.ReadJSON(&m); err != nil {
				return m, err
			}
			if m.Type == targetType {
				return m, nil
			}
		}
	}

	if _, err := readUntilType("books"); err != nil {
		t.Fatal(err)
	}

	if err := wsConn.WriteJSON(Message{Type: "select_book", Content: "Genesis"}); err != nil {
		t.Fatal(err)
	}

	introMsg, err := readUntilType("intro")
	if err != nil {
		t.Fatal(err)
	}

	// Complete intro using VoiceMode
	if err := wsConn.WriteJSON(Message{
		Type:      "content",
		Content:   introMsg.Content,
		WPM:       60,
		Duration:  4.0,
		VoiceMode: true,
	}); err != nil {
		t.Fatal(err)
	}

	verseMsg, err := readUntilType("verse")
	if err != nil {
		t.Fatal(err)
	}
	if verseMsg.Number != 1 {
		t.Fatalf("Expected verse 1, got %d", verseMsg.Number)
	}

	// Submit verse 1 using unpunctuated, lowercase spoken text with VoiceMode: true
	if err := wsConn.WriteJSON(Message{
		Type:      "content",
		Content:   "in the beginning god created the heaven and the earth",
		WPM:       75,
		Duration:  3.5,
		VoiceMode: true,
	}); err != nil {
		t.Fatal(err)
	}

	correctMsg, err := readUntilType("correct")
	if err != nil {
		t.Fatalf("Expected correct message for spoken verse submission, got err: %v", err)
	}
	if correctMsg.Content != "correct" {
		t.Errorf("Expected correct content, got %q", correctMsg.Content)
	}

	verse2Msg, err := readUntilType("verse")
	if err != nil {
		t.Fatal(err)
	}
	if verse2Msg.Number != 2 {
		t.Errorf("Expected progression to verse 2 after Voice Mode submission, got %d", verse2Msg.Number)
	}

	// Verify voice_herald badge was awarded in store
	badges, err := ls.GetBadges(context.Background(), "test_voice_user")
	if err != nil {
		t.Fatal(err)
	}
	foundVoiceBadge := false
	for _, b := range badges {
		if b.ID == "voice_herald" {
			foundVoiceBadge = true
			break
		}
	}
	if !foundVoiceBadge {
		t.Errorf("Expected voice_herald badge to be awarded after Voice Mode completion, got badges: %+v", badges)
	}
}

func TestPassageFinishBurstDamageAndDemonTransition(t *testing.T) {
	if len(translations) == 0 {
		if err := loadTranslationsAndGuides(); err != nil {
			t.Fatalf("Failed to load translations: %v", err)
		}
	}

	// 1. Unit test CalculatePassageFinishBurstDamage (default & adjustable)
	// Default baseBurst = 30, baseStrike = 1, syncStacks = 0, charsAhead = 0 => 30
	dmgDefault := CalculatePassageFinishBurstDamage(1, 0, 0, 0)
	if dmgDefault != 30 {
		t.Errorf("Expected default passage burst damage 30, got %d", dmgDefault)
	}

	// Custom adjustable baseBurst = 50, baseStrike = 1, syncStacks = 5 (tier 1: +25%), charsAhead = 10 => (50 + 10 + 5*2) * 1.25 = 70 * 1.25 = 88
	dmgAdjusted := CalculatePassageFinishBurstDamage(1, 5, 10, 50)
	if dmgAdjusted != 88 {
		t.Errorf("Expected adjusted passage burst damage 88, got %d", dmgAdjusted)
	}

	// Clamping to MaxPassageBurstDamage (300)
	dmgMax := CalculatePassageFinishBurstDamage(50, 50, 100, 300)
	if dmgMax != DefaultDemonSyncBalance.MaxPassageBurstDamage {
		t.Errorf("Expected max clamped burst damage %d, got %d", DefaultDemonSyncBalance.MaxPassageBurstDamage, dmgMax)
	}

	// 2. Test AdvanceWildDemonToNextVerse resets CharIndex to 0 and restores BaseWPM (+ EnrageCount*2)
	hub := newHub()
	go hub.run()
	dm := newDemonManager(nil, hub)
	defer dm.Stop()

	// Find wild-imp-1 on Genesis 3:1
	impBefore := dm.GetWildDemon("wild-imp-1")
	if impBefore == nil {
		t.Fatalf("Expected wild-imp-1 to exist")
	}
	baseWPM := impBefore.WPM

	// Simulate dynamic WPM surge during combat on Genesis 3:1
	dm.AdjustWildDemonPaceForPlayer("kjv", "Genesis", 3, 1, 45, 70)
	impSurged := dm.GetWildDemon("wild-imp-1")
	if impSurged.WPM <= baseWPM {
		t.Errorf("Expected demon WPM to surge above base %d when player is ahead, got %d", baseWPM, impSurged.WPM)
	}

	// Advance demon to next passage (Genesis 3:2)
	advDemon, nextV, ok := dm.AdvanceWildDemonToNextVerse("wild-imp-1")
	if !ok || advDemon == nil {
		t.Fatalf("Expected AdvanceWildDemonToNextVerse to succeed")
	}
	if nextV.BookName != "Genesis" || nextV.Chapter != 3 || nextV.Verse != 2 {
		t.Errorf("Expected demon to advance to Genesis 3:2, got %s %d:%d", nextV.BookName, nextV.Chapter, nextV.Verse)
	}
	if advDemon.CharIndex != 0 {
		t.Errorf("Expected advanced demon CharIndex to reset to 0, got %d", advDemon.CharIndex)
	}
	if advDemon.WPM != baseWPM {
		t.Errorf("Expected advanced demon WPM to reset to baseWPM %d, got %d", baseWPM, advDemon.WPM)
	}
	if advDemon.BaseWPM != baseWPM {
		t.Errorf("Expected advanced demon BaseWPM to be %d, got %d", baseWPM, advDemon.BaseWPM)
	}

	// 3. WebSocket integration test: User finishes passage before demon -> burst damage applied & demon driven to next verse!
	tmpDir, err := os.MkdirTemp("", "elespe_burst_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	store = ls
	globalHub = hub
	globalDemonManager = dm

	// Place wild-imp-1 on Genesis 1:1 with 120 HP so we can fight it immediately after intro
	gen1v1Text := translations["kjv"].VersesByBook["Genesis"][0].Text
	dm.mu.Lock()
	for _, d := range dm.wildDemons {
		if d.ID == "wild-imp-1" {
			d.BookName = "Genesis"
			d.Chapter = 1
			d.Verse = 1
			d.Text = cleanVerseText(gen1v1Text)
			d.CurrentHP = 120
			d.MaxHP = 120
			d.CharIndex = 10
			d.verseEpoch++
			break
		}
	}
	dm.mu.Unlock()

	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?uid=test_burst_user&trans=kjv"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer wsConn.Close()

	readUntilType := func(targetType string) (Message, error) {
		_ = wsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var m Message
			if err := wsConn.ReadJSON(&m); err != nil {
				return m, err
			}
			if m.Type == targetType {
				return m, nil
			}
		}
	}

	if _, err := readUntilType("books"); err != nil {
		t.Fatal(err)
	}
	if err := wsConn.WriteJSON(Message{Type: "select_book", Content: "Genesis"}); err != nil {
		t.Fatal(err)
	}
	introMsg, err := readUntilType("intro")
	if err != nil {
		t.Fatal(err)
	}
	if err := wsConn.WriteJSON(Message{
		Type:     "content",
		Content:  introMsg.Content,
		WPM:      60,
		Duration: 3.0,
	}); err != nil {
		t.Fatal(err)
	}

	verse1Msg, err := readUntilType("verse")
	if err != nil {
		t.Fatal(err)
	}
	if verse1Msg.WildDemon == nil || verse1Msg.WildDemon.ID != "wild-imp-1" {
		t.Fatalf("Expected wild-imp-1 on Genesis 1:1, got %+v", verse1Msg.WildDemon)
	}

	// Finish Genesis 1:1 before the demon with adjusted BurstDamage: 45 and SyncStacks: 3
	if err := wsConn.WriteJSON(Message{
		Type:        "content",
		Content:     verse1Msg.Content,
		WPM:         70,
		Duration:    4.0,
		SyncStacks:  3,
		DemonID:     "wild-imp-1",
		BurstDamage: 45,
	}); err != nil {
		t.Fatal(err)
	}

	burstMsg, err := readUntilType("wild_demon_burst")
	if err != nil {
		t.Fatalf("Expected wild_demon_burst message when finishing passage before demon, got err: %v", err)
	}
	if burstMsg.BurstDamage <= 45 {
		t.Errorf("Expected total burst damage > 45 (base 45 + strike + sync stacks + char lead), got %d", burstMsg.BurstDamage)
	}
	if burstMsg.WildDemon == nil || burstMsg.WildDemon.CurrentHP >= 120 {
		t.Errorf("Expected demon HP to be reduced from 120 by burst damage, got %+v", burstMsg.WildDemon)
	}

	// Verify next verse (Genesis 1:2) has the same wild demon waiting at CharIndex 0 with reduced HP!
	verse2Msg, err := readUntilType("verse")
	if err != nil {
		t.Fatal(err)
	}
	if verse2Msg.Number != 2 {
		t.Errorf("Expected verse 2, got %d", verse2Msg.Number)
	}
	if verse2Msg.WildDemon == nil || verse2Msg.WildDemon.ID != "wild-imp-1" {
		t.Fatalf("Expected wild-imp-1 to have moved onto verse 2 (Genesis 1:2) with the player, got %+v", verse2Msg.WildDemon)
	}
	if verse2Msg.WildDemon.CharIndex != 0 {
		t.Errorf("Expected demon on verse 2 to start at CharIndex 0, got %d", verse2Msg.WildDemon.CharIndex)
	}
	if verse2Msg.WildDemon.CurrentHP != 120-burstMsg.BurstDamage {
		t.Errorf("Expected demon HP on verse 2 to be %d, got %d", 120-burstMsg.BurstDamage, verse2Msg.WildDemon.CurrentHP)
	}

	// Verify custom PassageBurstDamage (45) was persisted in user customizations
	savedCustoms, err := ls.GetCustomizations(context.Background(), "test_burst_user")
	if err != nil {
		t.Fatal(err)
	}
	if savedCustoms.PassageBurstDamage != 45 {
		t.Errorf("Expected saved PassageBurstDamage to be 45, got %d", savedCustoms.PassageBurstDamage)
	}
}

func TestAdminConsoleSecurityAndLiveOperations(t *testing.T) {
	if len(translations) == 0 {
		if err := loadTranslationsAndGuides(); err != nil {
			t.Fatalf("Failed to load translations: %v", err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "elespe_admin_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	store = ls
	globalHub = newHub()
	go globalHub.run()
	globalDemonManager = newDemonManager(store, globalHub)
	defer globalDemonManager.Stop()

	t.Setenv("ADMIN_USERNAME", "sanctuary_admin")
	t.Setenv("ADMIN_PASSWORD", "super-secret-pass-123")
	initAdminSystem(tmpDir)

	mux := http.NewServeMux()
	registerAdminRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Verify /admin serves HTML with strict security headers
	respPage, err := http.Get(server.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer respPage.Body.Close()
	if respPage.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK for /admin, got %d", respPage.StatusCode)
	}
	if respPage.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("Expected X-Frame-Options: DENY, got %q", respPage.Header.Get("X-Frame-Options"))
	}

	// 2. Verify unauthenticated API requests are rejected with 401
	respUnauth, err := http.Get(server.URL + "/api/admin/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer respUnauth.Body.Close()
	if respUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Expected 401 Unauthorized for unauthenticated dashboard request, got %d", respUnauth.StatusCode)
	}

	// 3. Verify wrong password fails and brute-force rate limit locks out after 5 attempts
	for i := 1; i <= 5; i++ {
		badReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/login", strings.NewReader(`{"username":"sanctuary_admin","password":"wrong-password"}`))
		badReq.Header.Set("Content-Type", "application/json")
		badReq.Header.Set("X-Forwarded-For", "198.51.100.77")
		badResp, err := http.DefaultClient.Do(badReq)
		if err != nil {
			t.Fatal(err)
		}
		badResp.Body.Close()
		if i < 5 && badResp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Attempt %d: expected 401, got %d", i, badResp.StatusCode)
		}
		if i == 5 && badResp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("Attempt 5: expected 429 lockout, got %d", badResp.StatusCode)
		}
	}

	// 4. Authenticate with valid credentials from another IP
	loginReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/login", strings.NewReader(`{"username":"sanctuary_admin","password":"super-secret-pass-123"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("X-Forwarded-For", "192.0.2.10")
	loginResp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK on valid admin login, got %d", loginResp.StatusCode)
	}

	var loginData struct {
		Authenticated bool   `json:"authenticated"`
		Token         string `json:"token"`
		CSRFToken     string `json:"csrf_token"`
	}
	if err := json.NewDecoder(loginResp.Body).Decode(&loginData); err != nil {
		t.Fatal(err)
	}
	if !loginData.Authenticated || loginData.Token == "" || loginData.CSRFToken == "" {
		t.Fatalf("Expected authenticated token and csrf_token, got %+v", loginData)
	}

	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == adminCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly {
		t.Fatalf("Expected HttpOnly %s cookie to be set", adminCookieName)
	}

	// 5. Verify CSRF protection when using cookie auth on state-mutating POST
	noCsrfReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/config/reset", strings.NewReader(`{}`))
	noCsrfReq.AddCookie(sessionCookie)
	noCsrfResp, err := http.DefaultClient.Do(noCsrfReq)
	if err != nil {
		t.Fatal(err)
	}
	noCsrfResp.Body.Close()
	if noCsrfResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 when X-CSRF-Token is missing on cookie-authenticated POST, got %d", noCsrfResp.StatusCode)
	}

	// 6. Update runtime config & burst damage parameters via /api/admin/config
	opts := globalAdmin.GetOptions()
	opts.DemonBalance.PassageBurstBaseDamage = 55
	opts.DemonBalance.CharSyncTolerance = 7
	opts.MaxWildDemons = 500
	optsBytes, _ := json.Marshal(opts)

	cfgReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/config", strings.NewReader(string(optsBytes)))
	cfgReq.Header.Set("Content-Type", "application/json")
	cfgReq.AddCookie(sessionCookie)
	cfgReq.Header.Set("X-CSRF-Token", loginData.CSRFToken)
	cfgResp, err := http.DefaultClient.Do(cfgReq)
	if err != nil {
		t.Fatal(err)
	}
	cfgResp.Body.Close()
	if cfgResp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK updating config, got %d", cfgResp.StatusCode)
	}
	if DefaultDemonSyncBalance.PassageBurstBaseDamage != 55 || DefaultDemonSyncBalance.CharSyncTolerance != 7 {
		t.Errorf("Expected live DefaultDemonSyncBalance to update (55, 7), got (%d, %d)", DefaultDemonSyncBalance.PassageBurstBaseDamage, DefaultDemonSyncBalance.CharSyncTolerance)
	}

	// Reset back to factory defaults
	rstReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/config/reset", strings.NewReader(`{}`))
	rstReq.AddCookie(sessionCookie)
	rstReq.Header.Set("X-CSRF-Token", loginData.CSRFToken)
	rstResp, err := http.DefaultClient.Do(rstReq)
	if err != nil {
		t.Fatal(err)
	}
	rstResp.Body.Close()
	if DefaultDemonSyncBalance.PassageBurstBaseDamage != 30 || DefaultDemonSyncBalance.CharSyncTolerance != 5 {
		t.Errorf("Expected reset DefaultDemonSyncBalance to restore (30, 5), got (%d, %d)", DefaultDemonSyncBalance.PassageBurstBaseDamage, DefaultDemonSyncBalance.CharSyncTolerance)
	}

	// 7. Verify /api/admin/dashboard returns analytics and debug info
	dashReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/admin/dashboard", nil)
	dashReq.Header.Set("Authorization", "Bearer "+loginData.Token)
	dashResp, err := http.DefaultClient.Do(dashReq)
	if err != nil {
		t.Fatal(err)
	}
	defer dashResp.Body.Close()
	if dashResp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/admin/dashboard, got %d", dashResp.StatusCode)
	}
	var dashPayload struct {
		Analytics AdminAnalyticsSnapshot `json:"analytics"`
		Debug     AdminDebugSnapshot     `json:"debug"`
	}
	if err := json.NewDecoder(dashResp.Body).Decode(&dashPayload); err != nil {
		t.Fatal(err)
	}
	if dashPayload.Debug.NumGoroutine <= 0 || dashPayload.Debug.GoVersion == "" {
		t.Errorf("Expected populated runtime debug metrics, got %+v", dashPayload.Debug)
	}
	if len(dashPayload.Debug.AuditLog) == 0 {
		t.Errorf("Expected non-empty security audit log")
	}

	// 8. Verify live admin action (spawn_demon & slay_demon)
	actReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/action", strings.NewReader(`{"action":"spawn_demon","book_name":"Genesis","chapter":1,"verse":3,"rank":"tormentor","level":3,"wpm":18,"hp":250,"name":"Admin Test Demon"}`))
	actReq.Header.Set("Content-Type", "application/json")
	actReq.Header.Set("Authorization", "Bearer "+loginData.Token)
	actResp, err := http.DefaultClient.Do(actReq)
	if err != nil {
		t.Fatal(err)
	}
	var actData struct {
		OK        bool       `json:"ok"`
		WildDemon *WildDemon `json:"wild_demon"`
	}
	_ = json.NewDecoder(actResp.Body).Decode(&actData)
	actResp.Body.Close()
	if !actData.OK || actData.WildDemon == nil || actData.WildDemon.Name != "Admin Test Demon" {
		t.Fatalf("Expected spawn_demon action to succeed, got %+v", actData)
	}

	slayReq2, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/action", strings.NewReader(fmt.Sprintf(`{"action":"slay_demon","demon_id":%q}`, actData.WildDemon.ID)))
	slayReq2.Header.Set("Content-Type", "application/json")
	slayReq2.Header.Set("Authorization", "Bearer "+loginData.Token)
	slayResp, err := http.DefaultClient.Do(slayReq2)
	if err != nil {
		t.Fatal(err)
	}
	slayResp.Body.Close()
	if slayResp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK slaying admin-spawned demon, got %d", slayResp.StatusCode)
	}

	// 9. Verify creating a new custom demon type and adjusting existing demon type parameters
	upsertPayload := `{
		"action": "upsert_demon_type",
		"apply_to_active_wild": true,
		"demon_type": {
			"rank": "archwraith",
			"name": "Abyssal Archwraith",
			"title": "Harvester of Doubt",
			"description": "A custom demon type created in the Admin Console.",
			"icon": "👻",
			"color": "#9333ea",
			"level": 1,
			"max_level": 5,
			"base_level_wpm": 50,
			"wpm_per_level": 15,
			"wild_min_wpm": 14,
			"wild_max_wpm": 20,
			"wild_base_hp": 180,
			"wild_hp_per_level": 75,
			"spawn_weight": 3,
			"min_progress_ratio": 0.2,
			"max_progress_ratio": 0.9,
			"block_duration": 3.2,
			"ability_interval_chars": 8,
			"shroud_chance": 0.75,
			"base_shroud_range": 3
		}
	}`
	upsertReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/action", strings.NewReader(upsertPayload))
	upsertReq.Header.Set("Content-Type", "application/json")
	upsertReq.Header.Set("Authorization", "Bearer "+loginData.Token)
	upsertResp, err := http.DefaultClient.Do(upsertReq)
	if err != nil {
		t.Fatal(err)
	}
	var upsertData struct {
		OK         bool        `json:"ok"`
		DemonType  DemonInfo   `json:"demon_type"`
		DemonTypes []DemonInfo `json:"demon_types"`
	}
	_ = json.NewDecoder(upsertResp.Body).Decode(&upsertData)
	upsertResp.Body.Close()
	if !upsertData.OK || upsertData.DemonType.Rank != "archwraith" || !upsertData.DemonType.IsCustom {
		t.Fatalf("Expected custom demon type archwraith to be created, got %+v", upsertData)
	}
	// Level 3 WPM should be 50 + (3-1)*15 = 80
	if wpm3 := GetDemonWPM("archwraith", 3); wpm3 != 80 {
		t.Errorf("Expected archwraith Lv.3 WPM = 80, got %d", wpm3)
	}

	// Spawn a wild demon of the new custom type using its default parameters
	spawnCustomReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/action", strings.NewReader(`{"action":"spawn_demon","book_name":"Genesis","chapter":1,"verse":4,"rank":"archwraith","level":2}`))
	spawnCustomReq.Header.Set("Content-Type", "application/json")
	spawnCustomReq.Header.Set("Authorization", "Bearer "+loginData.Token)
	spawnCustomResp, err := http.DefaultClient.Do(spawnCustomReq)
	if err != nil {
		t.Fatal(err)
	}
	var spawnCustomData struct {
		OK        bool       `json:"ok"`
		WildDemon *WildDemon `json:"wild_demon"`
	}
	_ = json.NewDecoder(spawnCustomResp.Body).Decode(&spawnCustomData)
	spawnCustomResp.Body.Close()
	if !spawnCustomData.OK || spawnCustomData.WildDemon == nil {
		t.Fatalf("Expected custom demon type spawn to succeed")
	}
	// Expected HP for level 2: 180 + 2*75 = 330
	if spawnCustomData.WildDemon.MaxHP != 330 || spawnCustomData.WildDemon.Icon != "👻" {
		t.Errorf("Expected custom wild demon MaxHP=330 and Icon=👻, got MaxHP=%d Icon=%q", spawnCustomData.WildDemon.MaxHP, spawnCustomData.WildDemon.Icon)
	}

	// Delete the custom demon type and reset demon types
	delReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/admin/action", strings.NewReader(`{"action":"delete_demon_type","rank":"archwraith"}`))
	delReq.Header.Set("Content-Type", "application/json")
	delReq.Header.Set("Authorization", "Bearer "+loginData.Token)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK deleting custom demon type, got %d", delResp.StatusCode)
	}
	if _, exists := GetDemonType("archwraith"); exists {
		t.Errorf("Expected archwraith to be deleted from demonTemplates")
	}
}

func TestPersistentGuardianGhostSyncLevelingAndMichaelCurrency(t *testing.T) {
	if err := loadTranslationsAndGuides(); err != nil {
		t.Fatal(err)
	}

	tmpDir, err := os.MkdirTemp("", "elespe_guardian_ghost_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	store = ls
	globalHub = newHub()
	go globalHub.run()
	globalAngelManager = newAngelManager(ls, globalHub)
	defer globalAngelManager.Stop()

	ctx := context.Background()
	uidA := "player_alpha"
	uidB := "player_bravo"
	_ = ls.EnsureUser(ctx, uidA)
	_ = ls.EnsureUser(ctx, uidB)

	// 1. Test Guardian Sync Leveling from Letters, Words, Verses, and Books + Summon Token reward
	// Default XP: 1/letter, 5/word, 25/verse, 250/book. Level 1 -> 2 requires 100 XP.
	sanct1, xpGained1, leveledUp1, err := ls.RecordGuardianSyncProgress(ctx, uidA, 30, 10, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("RecordGuardianSyncProgress failed: %v", err)
	}
	// 30*1 + 10*5 + 1*25 = 105 XP -> should level up from Lv.1 to Lv.2 (with 5 XP remainder)!
	if xpGained1 != 105 || !leveledUp1 || sanct1.GuardianLevel != 2 || sanct1.GuardianXP != 5 {
		t.Errorf("Expected 105 XP, leveledUp=true, Level=2, XP=5; got xpGained=%d leveledUp=%v Level=%d XP=%d",
			xpGained1, leveledUp1, sanct1.GuardianLevel, sanct1.GuardianXP)
	}
	if sanct1.SyncedLetters != 30 || sanct1.SyncedWords != 10 || sanct1.SyncedVerses != 1 {
		t.Errorf("Unexpected synced counters: %+v", sanct1)
	}

	// Book completion awards +250 XP and +1 Summon Token
	sanct2, xpGained2, leveledUp2, err := ls.RecordGuardianSyncProgress(ctx, uidA, 0, 0, 0, 1, 0, 1)
	if err != nil {
		t.Fatalf("RecordGuardianSyncProgress book failed: %v", err)
	}
	if xpGained2 != 250 || !leveledUp2 || sanct2.SyncedBooks != 1 || sanct2.SummonTokens != 1 {
		t.Errorf("Expected book sync XP=250, SyncedBooks=1, SummonTokens=1; got xp=%d books=%d tokens=%d",
			xpGained2, sanct2.SyncedBooks, sanct2.SummonTokens)
	}

	// 2. Test Persistent Player Guardian Ghost continues mashing out verses when player disconnects WITHOUT granting user progress
	shortVerse := "Jesus wept."
	gInfo := globalAngelManager.EnsurePlayerGuardian(uidB, "Bravo Scribe", "#10b981", "kjv", "John", 11, 35, shortVerse)
	if gInfo == nil || gInfo.Rank != "guardian" || gInfo.OwnerUID != uidB {
		t.Fatalf("Expected persistent guardian for %s, got %+v", uidB, gInfo)
	}

	// Speed up Bravo's guardian bot so it finishes the verse quickly while Bravo is offline
	globalAngelManager.mu.RLock()
	bravoBot := globalAngelManager.guardians[uidB]
	globalAngelManager.mu.RUnlock()
	if bravoBot == nil {
		t.Fatalf("Expected Bravo guardian bot in guardians map")
	}
	bravoBot.mu.Lock()
	bravoBot.Info.WPM = 600
	bravoBot.mu.Unlock()

	// Simulate Bravo disconnecting
	globalAngelManager.OnUserDisconnect(uidB)

	// Verify Bravo's guardian is still in RoamingGuardians even while Bravo is offline
	roaming := globalAngelManager.GetRoamingGuardians()
	foundBravo := false
	for _, rg := range roaming {
		if rg.OwnerUID == uidB {
			foundBravo = true
			if rg.PlayerOnline {
				t.Errorf("Expected PlayerOnline=false for disconnected user %s", uidB)
			}
		}
	}
	if !foundBravo {
		t.Errorf("Expected disconnected player's guardian to remain in GetRoamingGuardians()")
	}

	// Wait for Bravo's autonomous guardian ghost to finish John 11:35 and advance linearly to John 11:36
	deadline := time.Now().Add(3 * time.Second)
	advancedLinearly := false
	for time.Now().Before(deadline) {
		curG := globalAngelManager.GetPlayerGuardian(uidB)
		if curG != nil && curG.BookName == "John" && curG.Chapter == 11 && curG.Verse >= 36 {
			advancedLinearly = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !advancedLinearly {
		curG := globalAngelManager.GetPlayerGuardian(uidB)
		t.Errorf("Expected disconnected player's guardian ghost to advance linearly past John 11:35, got %+v", curG)
	}

	// Verify Bravo's personal BookProgress and personal UserCompletions were NOT incremented by the autonomous ghost!
	uComps, gComps, _ := ls.GetVerseCompletions(ctx, uidB, "kjv", "John", 11, 35)
	if uComps != 0 {
		t.Errorf("Autonomous guardian ghost must NOT grant personal verse completions to offline player, got uComps=%d", uComps)
	}
	if gComps < 1 {
		t.Errorf("Expected global completions to record autonomous angel inscription, got gComps=%d", gComps)
	}
	bpBravo, _ := ls.GetBookProgress(ctx, uidB, "kjv", "John", 879)
	if bpBravo != nil && bpBravo.CurrentVerse != 0 {
		t.Errorf("Autonomous guardian ghost must NOT advance user BookProgress, got CurrentVerse=%d", bpBravo.CurrentVerse)
	}

	// 3. Test Big Mike linear canonical progression & Summon Currency enforcement over WebSocket
	globalAngelManager.StartGlobalAngel("michael")
	mInfo := globalAngelManager.SummonGlobalMichaelToVerse("kjv", "Genesis", 1, 1, "In the beginning God created the heaven and the earth.")
	if mInfo == nil || mInfo.BookName != "Genesis" || mInfo.Chapter != 1 || mInfo.Verse != 1 {
		t.Fatalf("Expected global Michael at Genesis 1:1, got %+v", mInfo)
	}

	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?uid=token_tester&trans=kjv"
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer wsConn.Close()

	readUntilType := func(targetTypes ...string) (Message, error) {
		_ = wsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var m Message
			if err := wsConn.ReadJSON(&m); err != nil {
				return m, err
			}
			for _, tt := range targetTypes {
				if m.Type == tt {
					return m, nil
				}
			}
		}
	}

	if _, err := readUntilType("books"); err != nil {
		t.Fatalf("Failed to read books on connect: %v", err)
	}

	// Attempt to summon Big Mike with 0 Summon Tokens -> should be denied!
	if err := wsConn.WriteJSON(Message{Type: "summon_angel", AngelRank: "michael"}); err != nil {
		t.Fatal(err)
	}
	denyMsg, err := readUntilType("angel_summon_denied")
	if err != nil {
		t.Fatalf("Expected angel_summon_denied when summoning Big Mike with 0 tokens, got err: %v", err)
	}
	if !strings.Contains(denyMsg.Content, "Token") {
		t.Errorf("Expected token denial message, got %q", denyMsg.Content)
	}

	// Test interacting with Bravo's Guardian Angel -> should grant AngelSyncBuff and +1 Holy Ward
	if err := wsConn.WriteJSON(Message{Type: "interact_angel", PeerAngelUID: uidB}); err != nil {
		t.Fatal(err)
	}
	buffMsg, err := readUntilType("angel_interaction_buff")
	if err != nil {
		t.Fatalf("Expected angel_interaction_buff, got err: %v", err)
	}
	if buffMsg.AngelBuff == nil || !buffMsg.AngelBuff.Active || buffMsg.HolyWards < 1 {
		t.Errorf("Expected active AngelSyncBuff and HolyWards>=1, got %+v", buffMsg)
	}
}

func TestDemonTaintRighteousnessDebuffAndGloriousMichaelTrail(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "elespe_taint_trail_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ls, err := newLocalStore(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	store = ls
	globalHub = newHub()
	go globalHub.run()
	globalAngelManager = newAngelManager(ls, globalHub)
	defer globalAngelManager.Stop()
	globalDemonManager = newDemonManager(ls, globalHub)
	defer globalDemonManager.Stop()

	// 1. Test Big Mike Glorious Golden Trail recording & inspection
	globalAngelManager.RecordMichaelTrail("kjv", "Genesis", 1, 1)
	if !globalAngelManager.IsVerseOnMichaelTrail("kjv", "Genesis", 1, 1) {
		t.Errorf("Expected Genesis 1:1 to be on Big Mike's Glorious Golden Trail")
	}
	if globalAngelManager.IsVerseOnMichaelTrail("kjv", "Genesis", 1, 2) {
		t.Errorf("Did not expect Genesis 1:2 to be on Big Mike's Glorious Golden Trail yet")
	}

	// 2. Grab an active Wild Demon and test Autonomous Guardian Skirmish + Righteousness Debuff
	demons := globalDemonManager.GetWildDemons()
	if len(demons) == 0 {
		t.Fatalf("Expected roaming wild demons on startup")
	}
	wildD := demons[0]
	initialHP := wildD.CurrentHP
	initialWPM := wildD.WPM

	skirmished := globalDemonManager.SkirmishWildDemonByGuardian(wildD.Translation, wildD.BookName, wildD.Chapter, wildD.Verse, 10)
	if skirmished == nil {
		t.Fatalf("Expected SkirmishWildDemonByGuardian to hit wild demon on %s %d:%d", wildD.BookName, wildD.Chapter, wildD.Verse)
	}
	if !skirmished.AngelicBeacon {
		t.Errorf("Expected AngelicBeacon=true after autonomous guardian skirmish")
	}
	if skirmished.CurrentHP >= initialHP {
		t.Errorf("Expected demon HP to decrease from %d after guardian skirmish, got %d", initialHP, skirmished.CurrentHP)
	}

	// Apply Righteousness Sanctified Rebuke
	rebuked, defeated := globalDemonManager.DebuffWildDemonByRighteousness(wildD.ID, 15)
	if defeated || rebuked == nil {
		t.Fatalf("Expected demon to survive rebuke and be debuffed, got defeated=%v, rebuked=%+v", defeated, rebuked)
	}
	if !rebuked.Debuffed || rebuked.DebuffStacks != 1 {
		t.Errorf("Expected Debuffed=true and DebuffStacks=1, got %+v", rebuked)
	}
	if rebuked.WPM >= initialWPM && initialWPM > 5 {
		t.Errorf("Expected debuffed demon WPM (%d) to be slower than initial WPM (%d)", rebuked.WPM, initialWPM)
	}

	// Verify debuffed demon takes +25% bonus damage on ApplyWildDemonDamage
	hpBeforeStrike := rebuked.CurrentHP
	struck, _ := globalDemonManager.ApplyWildDemonDamage(wildD.ID, 20)
	if struck == nil {
		t.Fatalf("Expected ApplyWildDemonDamage to return updated demon")
	}
	actualDmg := hpBeforeStrike - struck.CurrentHP
	if actualDmg != 25 { // 20 * 1.25 = 25
		t.Errorf("Expected 25 damage (20 + 25%% debuff vulnerability), got %d", actualDmg)
	}

	// 3. Test Offline Guardian Report accumulation & consumption
	globalAngelManager.RecordOfflineGuardianBlessing("offline_scribe", 25)
	rep := globalAngelManager.ConsumeOfflineReport("offline_scribe")
	if rep == nil || rep.BlessingsReceived != 1 || rep.BonusXPEarned != 25 {
		t.Errorf("Expected offline report with 1 blessing received and 25 bonus XP, got %+v", rep)
	}
	if repAgain := globalAngelManager.ConsumeOfflineReport("offline_scribe"); repAgain != nil {
		t.Errorf("Expected second ConsumeOfflineReport to return nil, got %+v", repAgain)
	}

	// 4. Test Capitalized Letters helper & bonus XP / damage
	if got := countUppercaseRunes("In the Beginning GOD created."); got != 5 {
		t.Errorf("Expected 5 uppercase runes in 'In the Beginning GOD created.', got %d", got)
	}

	// 5. Test Salted Password Hashing, Legacy Upgrade, and Auth Token Verification
	preHash := hashPassword("MySacredSecret99")
	salted1 := saltPasswordHash(preHash)
	salted2 := saltPasswordHash(preHash)
	if salted1 == salted2 {
		t.Errorf("Expected distinct random salts for identical password pre-hashes")
	}
	if !verifyStoredPassword(salted1, preHash) || !verifyStoredPassword(salted2, preHash) {
		t.Errorf("Expected salted password hashes to verify against pre-hash")
	}
	if verifyStoredPassword(salted1, hashPassword("WrongPass")) {
		t.Errorf("Expected wrong password to fail verification")
	}
	if !verifyStoredPassword(preHash, preHash) || !needsPasswordUpgrade(preHash) {
		t.Errorf("Expected legacy v1 hash to verify and flag for v2 upgrade")
	}
	tok := signAuthToken("acc_12345")
	if !verifyAuthToken("acc_12345", tok) {
		t.Errorf("Expected valid auth token to verify for acc_12345")
	}
	if verifyAuthToken("acc_99999", tok) || verifyAuthToken("acc_12345", "invalid") {
		t.Errorf("Expected forged or mismatched auth token to be rejected")
	}

	// 6. Verify UI Layout: Typing Stage above Companion Dock, Smart Buff Pills, Collapsible Guardian Tabs, and Zen Mode
	htmlBytes, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("Failed to read index.html: %v", err)
	}
	htmlStr := string(htmlBytes)
	verseIdx := strings.Index(htmlStr, `id="verse"`)
	dockIdx := strings.Index(htmlStr, `id="companionDock"`)
	if verseIdx == -1 || dockIdx == -1 || verseIdx > dockIdx {
		t.Errorf("Expected #verse (pos %d) to appear above #companionDock (pos %d)", verseIdx, dockIdx)
	}
	for _, marker := range []string{
		`id="stageBuffsBar"`,
		`class="smart-buff-pill"`,
		`id="guardianTabsShell"`,
		`id="btnToggleGuardianDock"`,
		`id="btnToggleZenMode"`,
		`zen-typing`,
		`revealSmartPillOnce`,
	} {
		if !strings.Contains(htmlStr, marker) {
			t.Errorf("Expected index.html to contain %q", marker)
		}
	}
}



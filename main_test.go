package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	if len(translations) != 7 {
		t.Errorf("Expected 7 translations, got %d", len(translations))
	}

	allTrans := []string{"kjv", "esv", "niv", "nlt", "hcsb", "asv", "bbe"}
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

	genGuide := bookGuides["Genesis"]
	if genGuide.Order != 1 || genGuide.Description == "" || genGuide.Review == "" {
		t.Errorf("Invalid Genesis guide: %+v", genGuide)
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

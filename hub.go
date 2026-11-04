package main

import (
	"fmt"
	"sync"
	"time"
)

// Client represents an active WebSocket connection with presence metadata.
type Client struct {
	conn           *safeConn
	UID            string
	DisplayName    string
	AvatarColor    string
	StreakDays     int
	Translation    string
	Book           string
	Chapter        int
	Verse          int
	CharIndex      int
	WPM            int
	IsSpectator    bool
	VerseStartTime time.Time
}

// Hub maintains active client connections, broadcasts presence, ghost cursors, and chat messages.
type Hub struct {
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

var globalHub *Hub

func newHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (h *Hub) run() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			h.broadcastCrowdMetrics()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
			}
			h.mu.Unlock()
			h.broadcastCrowdMetrics()

		case <-ticker.C:
			h.broadcastCrowdMetrics()
		}
	}
}

func (h *Hub) Register(c *Client) {
	h.register <- c
}

func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

func (h *Hub) UpdateClientIdentity(c *Client, displayName, color string, streakDays int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.DisplayName = displayName
	c.AvatarColor = color
	c.StreakDays = streakDays
}

func (h *Hub) UpdateClientLocation(c *Client, trans, book string, chapter, verse int) {
	h.mu.Lock()
	c.Translation = trans
	c.Book = book
	c.Chapter = chapter
	c.Verse = verse
	c.CharIndex = 0
	h.mu.Unlock()

	h.sendVerseTypersCount(trans, book, chapter, verse)
}

func (h *Hub) UpdateCursor(c *Client, charIndex, wpm int) {
	h.mu.Lock()
	c.CharIndex = charIndex
	c.WPM = wpm
	trans := c.Translation
	book := c.Book
	chapter := c.Chapter
	verse := c.Verse
	uid := c.UID
	displayName := c.DisplayName
	color := c.AvatarColor

	// Find peers on same passage to send ghost cursor
	var peers []*safeConn
	for peer := range h.clients {
		if peer != c && peer.Translation == trans && peer.Book == book && peer.Chapter == chapter && peer.Verse == verse {
			peers = append(peers, peer.conn)
		}
	}
	h.mu.Unlock()

	ghostMsg := Message{
		Type: "ghost_cursor",
		Ghost: &GhostCursor{
			UID:         uid,
			DisplayName: displayName,
			AvatarColor: color,
			CharIndex:   charIndex,
			WPM:         wpm,
		},
	}

	for _, conn := range peers {
		_ = conn.WriteJSON(ghostMsg)
	}
}

func (h *Hub) broadcastAll(out Message) {
	h.mu.RLock()
	conns := make([]*safeConn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c.conn)
	}
	h.mu.RUnlock()

	for _, conn := range conns {
		_ = conn.WriteJSON(out)
	}
}

func (h *Hub) broadcastToVerse(trans, book string, chapter, verse int, out Message) {
	h.mu.RLock()
	var peers []*safeConn
	for peer := range h.clients {
		if peer.Translation == trans && peer.Book == book && peer.Chapter == chapter && peer.Verse == verse {
			peers = append(peers, peer.conn)
		}
	}
	h.mu.RUnlock()

	for _, conn := range peers {
		_ = conn.WriteJSON(out)
	}
}

func (h *Hub) BroadcastAngelCursor(trans, book string, chapter, verse int, ghost GhostCursor) {
	h.broadcastToVerse(trans, book, chapter, verse, Message{
		Type:  "ghost_cursor",
		Ghost: &ghost,
	})
}

func (h *Hub) BroadcastDemonCursor(trans, book string, chapter, verse int, ghost GhostCursor) {
	h.broadcastToVerse(trans, book, chapter, verse, Message{
		Type:  "ghost_cursor",
		Ghost: &ghost,
	})
}

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) IsUserConnected(uid string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.UID == uid {
			return true
		}
	}
	return false
}

func (h *Hub) SendToUID(uid string, msg Message) {
	h.mu.RLock()
	var targets []*safeConn
	for c := range h.clients {
		if c.UID == uid {
			targets = append(targets, c.conn)
		}
	}
	h.mu.RUnlock()

	for _, conn := range targets {
		_ = conn.WriteJSON(msg)
	}
}

func (h *Hub) sendVerseTypersCount(trans, book string, chapter, verse int) {
	h.mu.RLock()
	var peers []*safeConn
	count := 0
	for peer := range h.clients {
		if peer.Translation == trans && peer.Book == book && peer.Chapter == chapter && peer.Verse == verse {
			count++
			peers = append(peers, peer.conn)
		}
	}
	h.mu.RUnlock()

	msg := Message{
		Type:        "typers_on_verse",
		TypersCount: count,
		Book:        book,
	}
	for _, conn := range peers {
		_ = conn.WriteJSON(msg)
	}
}

func (h *Hub) broadcastCrowdMetrics() {
	h.mu.RLock()
	totalOnline := len(h.clients)
	typersByBook := make(map[string]int)
	for c := range h.clients {
		if c.Book != "" {
			typersByBook[c.Book]++
		}
	}
	// Copy client conns
	conns := make([]*safeConn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c.conn)
	}
	h.mu.RUnlock()

	// Get trending book
	trendingBook := "Genesis"
	maxCount := 0
	for b, count := range typersByBook {
		if count > maxCount {
			maxCount = count
			trendingBook = b
		}
	}

	metrics := &CrowdMetrics{
		ActiveTypers: totalOnline,
		TypersByBook: typersByBook,
		TrendingBook: trendingBook,
	}

	msg := Message{
		Type:    "crowd_metrics",
		Metrics: metrics,
	}

	for _, conn := range conns {
		_ = conn.WriteJSON(msg)
	}
}

func (h *Hub) BroadcastChatMessage(msg ChatMessage) {
	h.mu.RLock()
	conns := make([]*safeConn, 0, len(h.clients))
	for c := range h.clients {
		if msg.Channel == "global" || c.Book == msg.Channel {
			conns = append(conns, c.conn)
		}
	}
	h.mu.RUnlock()

	out := Message{
		Type:        "chat_message",
		ChatMessage: &msg,
	}
	for _, conn := range conns {
		_ = conn.WriteJSON(out)
	}
}

func (h *Hub) BroadcastCommentAdded(comment VerseComment) {
	h.broadcastToVerse(comment.Translation, comment.BookName, comment.Chapter, comment.Verse, Message{
		Type:    "comment_added",
		Comment: &comment,
	})
}

func (h *Hub) BroadcastBossRaidUpdate(raid BossRaid, hit *BossConqueror) {
	h.broadcastAll(Message{
		Type:     "boss_raid_update",
		BossRaid: &raid,
		BossHit:  hit,
	})
}

func (h *Hub) HasClientsOnVerse(trans, book string, chapter, verse int) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for peer := range h.clients {
		if peer.Translation == trans && peer.Book == book && peer.Chapter == chapter && peer.Verse == verse {
			return true
		}
	}
	return false
}

func (h *Hub) GetActivePlayerOnVerse(trans, book string, chapter, verse int) (charIndex int, wpm int, ok bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for peer := range h.clients {
		if !peer.IsSpectator && peer.Translation == trans && peer.Book == book && peer.Chapter == chapter && peer.Verse == verse {
			return peer.CharIndex, peer.WPM, true
		}
	}
	return 0, 0, false
}

func (h *Hub) BroadcastWildDemonsList(demons []WildDemon) {
	maxDemons := 666
	if dm := getGlobalDemonManager(); dm != nil {
		maxDemons = dm.GetMaxWildDemons()
	}
	activeCount := len(demons)
	sanctPct := 100
	if activeCount > 0 {
		sanctPct = int((1.0 - float64(activeCount)/float64(max(1, maxDemons))) * 100)
		if sanctPct < 0 {
			sanctPct = 0
		}
	}

	h.broadcastAll(Message{
		Type:           "wild_demons_list_update",
		WildDemons:     demons,
		ActiveDemons:   activeCount,
		MaxDemons:      maxDemons,
		Sanctification: sanctPct,
	})
}

func (h *Hub) BroadcastSwarmEvent(eventText string, activeDemons, maxDemons, sanctification int) {
	h.broadcastAll(Message{
		Type:           "swarm_event",
		SwarmEvent:     eventText,
		ActiveDemons:   activeDemons,
		MaxDemons:      maxDemons,
		Sanctification: sanctification,
	})
}

func (h *Hub) BroadcastMichaelSlay(trans, book string, chapter, verse int, demon WildDemon, content string) {
	h.broadcastAll(Message{
		Type:        "michael_slay",
		Content:     content,
		WildDemon:   &demon,
		Book:        book,
		Translation: trans,
	})
}

func (h *Hub) BroadcastDemonEscaped(trans, book string, chapter, verse int, demon WildDemon, nextVerse Verse) {
	content := fmt.Sprintf("%s %s completed %s %d:%d before being slain and prowled onto %s %d:%d!", demon.Icon, demon.Name, book, chapter, verse, nextVerse.BookName, nextVerse.Chapter, nextVerse.Verse)
	h.broadcastAll(Message{
		Type:        "wild_demon_escaped",
		Content:     content,
		WildDemon:   &demon,
		Verse:       &nextVerse,
		Book:        book,
		Translation: trans,
	})
}


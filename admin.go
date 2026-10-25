package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	adminCookieName      = "elespe_admin_session"
	adminSessionDuration = 12 * time.Hour
	adminMaxFailedLogins = 5
	adminLockoutWindow   = 5 * time.Minute
	adminMaxAuditEntries = 250
)

var (
	serverStartTime = time.Now().UTC()
	globalAdmin     *AdminManager
)

// AdminRuntimeOptions holds live-configurable server options, game parameters, and feature flags.
type AdminRuntimeOptions struct {
	DemonBalance                 DemonSyncBalanceConfig `json:"demon_balance"`
	MaxWildDemons                int                    `json:"max_wild_demons"`
	SpawnIntervalOverrideSec     int                    `json:"spawn_interval_override_sec"`
	EvolveIntervalSec            int                    `json:"evolve_interval_sec"`
	EncounterBaseChance          float64                `json:"encounter_base_chance"`
	EncounterMaxChance           float64                `json:"encounter_max_chance"`
	EncounterPeacefulIntroVerses int                    `json:"encounter_peaceful_intro_verses"`
	GlobalAngelRank              string                 `json:"global_angel_rank"`
	GlobalAngelWPM               int                    `json:"global_angel_wpm"`
	Devil24_7Rank                string                 `json:"devil_24_7_rank"`
	Devil24_7Level               int                    `json:"devil_24_7_level"`
	MandatoryBookIntro           bool                   `json:"mandatory_book_intro"`
	VoiceModeEnabled             bool                   `json:"voice_mode_enabled"`
	ChatEnabled                  bool                   `json:"chat_enabled"`
	CommentsEnabled              bool                   `json:"comments_enabled"`
	MaintenanceBanner            string                 `json:"maintenance_banner"`
}

type adminPersistedState struct {
	Username     string              `json:"username"`
	PasswordHash string              `json:"password_hash"`
	Options      AdminRuntimeOptions `json:"options"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

type adminSession struct {
	Token     string    `json:"token"`
	CSRFToken string    `json:"csrf_token"`
	Username  string    `json:"username"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type loginAttemptTracker struct {
	Failures    int
	FirstFailed time.Time
	LockedUntil time.Time
}

type AdminAuditEntry struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`
	IP        string    `json:"ip"`
	Action    string    `json:"action"`
	Details   string    `json:"details"`
}

// AdminManager manages secure admin authentication, live parameter tuning, usage analytics, and debug telemetry.
type AdminManager struct {
	mu             sync.RWMutex
	configPath     string
	username       string
	passwordHash   string
	options        AdminRuntimeOptions
	defaultBalance DemonSyncBalanceConfig
	sessions       map[string]adminSession
	failedAttempts map[string]*loginAttemptTracker
	auditLog       []AdminAuditEntry
	nextAuditID    int
}

func hashAdminPassword(password string) string {
	salt := "elespe-admin-sanctuary-v1-salt:"
	sum := sha256.Sum256([]byte(salt + password))
	return hex.EncodeToString(sum[:])
}

func generateSecureToken(numBytes int) string {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(sum[:numBytes])
	}
	return hex.EncodeToString(b)
}

func defaultAdminRuntimeOptions(baseBalance DemonSyncBalanceConfig) AdminRuntimeOptions {
	return AdminRuntimeOptions{
		DemonBalance:                 baseBalance,
		MaxWildDemons:                666,
		SpawnIntervalOverrideSec:     0,
		EvolveIntervalSec:            600,
		EncounterBaseChance:          0.04,
		EncounterMaxChance:           0.50,
		EncounterPeacefulIntroVerses: 2,
		GlobalAngelRank:              "michael",
		GlobalAngelWPM:               160,
		Devil24_7Rank:                "imp",
		Devil24_7Level:               1,
		MandatoryBookIntro:           true,
		VoiceModeEnabled:             true,
		ChatEnabled:                  true,
		CommentsEnabled:              true,
		MaintenanceBanner:            "",
	}
}

func initAdminSystem(configDir string) *AdminManager {
	if configDir == "" {
		configDir = ".elespe"
	}
	_ = os.MkdirAll(configDir, 0755)
	cfgPath := filepath.Join(configDir, "admin_config.json")

	baseBalance := DefaultDemonSyncBalance
	mgr := &AdminManager{
		configPath:     cfgPath,
		username:       "admin",
		options:        defaultAdminRuntimeOptions(baseBalance),
		defaultBalance: baseBalance,
		sessions:       make(map[string]adminSession),
		failedAttempts: make(map[string]*loginAttemptTracker),
		auditLog:       make([]AdminAuditEntry, 0, 64),
		nextAuditID:    1,
	}

	// Load persisted config if it exists
	if raw, err := os.ReadFile(cfgPath); err == nil {
		var saved adminPersistedState
		if err := json.Unmarshal(raw, &saved); err == nil {
			if saved.Username != "" {
				mgr.username = saved.Username
			}
			if saved.PasswordHash != "" {
				mgr.passwordHash = saved.PasswordHash
			}
			if saved.Options.MaxWildDemons > 0 {
				mgr.options = saved.Options
			}
		}
	}

	// Environment variable overrides take highest priority for credentials
	if envUser := strings.TrimSpace(os.Getenv("ADMIN_USERNAME")); envUser != "" {
		mgr.username = envUser
	}
	envPass := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD"))
	if envPass == "" {
		envPass = strings.TrimSpace(os.Getenv("ADMIN_TOKEN"))
	}
	if envPass != "" {
		mgr.passwordHash = hashAdminPassword(envPass)
	} else if mgr.passwordHash == "" {
		defaultPass := "elespe-admin"
		mgr.passwordHash = hashAdminPassword(defaultPass)
		passFile := filepath.Join(configDir, "admin_password.txt")
		_ = os.WriteFile(passFile, []byte(defaultPass+"\n"), 0600)
		log.Printf("[Admin] Initialized admin console at /admin (username: %q, default password saved to %s; set ADMIN_PASSWORD env var or change in Admin Console -> Security)", mgr.username, passFile)
	}

	mgr.applyOptionsToSubsystemsLocked()
	mgr.recordAuditLocked("system", "127.0.0.1", "startup", "Admin console and security subsystem initialized")
	globalAdmin = mgr
	return mgr
}

func (m *AdminManager) saveStateLocked() error {
	if m.configPath == "" {
		return nil
	}
	state := adminPersistedState{
		Username:     m.username,
		PasswordHash: m.passwordHash,
		Options:      m.options,
		UpdatedAt:    time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(m.configPath), 0755)
	return os.WriteFile(m.configPath, raw, 0600)
}

func (m *AdminManager) recordAuditLocked(actor, ip, action, details string) {
	entry := AdminAuditEntry{
		ID:        m.nextAuditID,
		Timestamp: time.Now().UTC(),
		Actor:     actor,
		IP:        ip,
		Action:    action,
		Details:   details,
	}
	m.nextAuditID++
	m.auditLog = append([]AdminAuditEntry{entry}, m.auditLog...)
	if len(m.auditLog) > adminMaxAuditEntries {
		m.auditLog = m.auditLog[:adminMaxAuditEntries]
	}
}

func (m *AdminManager) GetOptions() AdminRuntimeOptions {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.options
}

func (m *AdminManager) applyOptionsToSubsystemsLocked() {
	DefaultDemonSyncBalance = m.options.DemonBalance
	if globalDemonManager != nil {
		if m.options.MaxWildDemons > 0 {
			globalDemonManager.SetMaxWildDemons(m.options.MaxWildDemons)
		}
		if m.options.SpawnIntervalOverrideSec > 0 {
			globalDemonManager.SetSpawnInterval(time.Duration(m.options.SpawnIntervalOverrideSec) * time.Second)
		} else {
			globalDemonManager.SetSpawnInterval(0)
		}
		if m.options.EvolveIntervalSec > 0 {
			globalDemonManager.SetEvolveInterval(time.Duration(m.options.EvolveIntervalSec) * time.Second)
		}
	}
	if globalAngelManager != nil && m.options.GlobalAngelWPM > 0 {
		globalAngelManager.mu.RLock()
		ga := globalAngelManager.globalAngel
		globalAngelManager.mu.RUnlock()
		if ga != nil {
			ga.mu.Lock()
			ga.Info.WPM = m.options.GlobalAngelWPM
			ga.mu.Unlock()
		}
	}
}

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func setAdminSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Content-Security-Policy", "default-src 'self' 'unsafe-inline' data:; frame-ancestors 'none'")
}

func writeAdminJSON(w http.ResponseWriter, status int, payload any) {
	setAdminSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (m *AdminManager) authenticateRequest(r *http.Request, requireCSRF bool) (*adminSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	for tok, sess := range m.sessions {
		if now.After(sess.ExpiresAt) {
			delete(m.sessions, tok)
		}
	}

	// 1. Check Authorization: Bearer <token> or X-Admin-Token header
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		tok := strings.TrimSpace(authHeader[7:])
		if sess, ok := m.sessions[tok]; ok && now.Before(sess.ExpiresAt) {
			return &sess, true
		}
	}
	if headerTok := strings.TrimSpace(r.Header.Get("X-Admin-Token")); headerTok != "" {
		if sess, ok := m.sessions[headerTok]; ok && now.Before(sess.ExpiresAt) {
			return &sess, true
		}
	}

	// 2. Check HttpOnly session cookie
	cookie, err := r.Cookie(adminCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	sess, ok := m.sessions[cookie.Value]
	if !ok || now.After(sess.ExpiresAt) {
		return nil, false
	}
	if requireCSRF {
		csrf := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
		if subtle.ConstantTimeCompare([]byte(csrf), []byte(sess.CSRFToken)) != 1 {
			return nil, false
		}
	}
	return &sess, true
}

func (m *AdminManager) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	setAdminSecurityHeaders(w)
	raw, err := appFS.ReadFile("admin.html")
	if err != nil {
		http.Error(w, "Admin console asset not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(raw)
}

func (m *AdminManager) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authenticateRequest(r, false)
	if !ok {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
		})
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      sess.Username,
		"csrf_token":    sess.CSRFToken,
		"expires_at":    sess.ExpiresAt,
	})
}

func (m *AdminManager) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}

	ip := getClientIP(r)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	tracker, exists := m.failedAttempts[ip]
	if exists {
		if now.Before(tracker.LockedUntil) {
			remaining := int(math.Ceil(tracker.LockedUntil.Sub(now).Seconds()))
			m.recordAuditLocked(req.Username, ip, "login_locked", fmt.Sprintf("Blocked login attempt during lockout (%ds remaining)", remaining))
			writeAdminJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               fmt.Sprintf("Too many failed login attempts. Try again in %d seconds.", remaining),
				"retry_after_seconds": remaining,
			})
			return
		}
		if now.Sub(tracker.FirstFailed) > adminLockoutWindow {
			delete(m.failedAttempts, ip)
			tracker = nil
		}
	}

	inputUser := strings.TrimSpace(req.Username)
	if inputUser == "" {
		inputUser = "admin"
	}
	inputPassHash := hashAdminPassword(req.Password)

	userMatch := subtle.ConstantTimeCompare([]byte(inputUser), []byte(m.username)) == 1
	passMatch := subtle.ConstantTimeCompare([]byte(inputPassHash), []byte(m.passwordHash)) == 1

	if !userMatch || !passMatch {
		if tracker == nil {
			tracker = &loginAttemptTracker{
				Failures:    1,
				FirstFailed: now,
			}
			m.failedAttempts[ip] = tracker
		} else {
			tracker.Failures++
		}
		if tracker.Failures >= adminMaxFailedLogins {
			tracker.LockedUntil = now.Add(adminLockoutWindow)
			m.recordAuditLocked(inputUser, ip, "login_lockout", fmt.Sprintf("IP locked out after %d failed attempts", tracker.Failures))
			writeAdminJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": "Too many failed login attempts. Locked out for 5 minutes.",
			})
			return
		}
		m.recordAuditLocked(inputUser, ip, "login_failed", fmt.Sprintf("Invalid credentials (attempt %d/%d)", tracker.Failures, adminMaxFailedLogins))
		writeAdminJSON(w, http.StatusUnauthorized, map[string]any{
			"error":              "Invalid admin username or password",
			"remaining_attempts": adminMaxFailedLogins - tracker.Failures,
		})
		return
	}

	delete(m.failedAttempts, ip)

	token := generateSecureToken(32)
	csrf := generateSecureToken(16)
	sess := adminSession{
		Token:     token,
		CSRFToken: csrf,
		Username:  m.username,
		IP:        ip,
		UserAgent: r.UserAgent(),
		CreatedAt: now,
		ExpiresAt: now.Add(adminSessionDuration),
	}
	m.sessions[token] = sess
	m.recordAuditLocked(m.username, ip, "login_success", "Admin session established")

	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		Expires:  sess.ExpiresAt,
	})

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      sess.Username,
		"token":         sess.Token,
		"csrf_token":    sess.CSRFToken,
		"expires_at":    sess.ExpiresAt,
	})
}

func (m *AdminManager) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authenticateRequest(r, false)
	if ok && sess != nil {
		m.mu.Lock()
		delete(m.sessions, sess.Token)
		m.recordAuditLocked(sess.Username, getClientIP(r), "logout", "Admin session terminated")
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (m *AdminManager) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}
	sess, ok := m.authenticateRequest(r, true)
	if !ok {
		writeAdminJSON(w, http.StatusUnauthorized, map[string]any{"error": "Unauthorized"})
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewUsername     string `json:"new_username"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}

	if len(req.NewPassword) < 8 {
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "New password must be at least 8 characters"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	curHash := hashAdminPassword(req.CurrentPassword)
	if subtle.ConstantTimeCompare([]byte(curHash), []byte(m.passwordHash)) != 1 {
		m.recordAuditLocked(sess.Username, getClientIP(r), "password_change_failed", "Current password verification failed")
		writeAdminJSON(w, http.StatusForbidden, map[string]any{"error": "Current password is incorrect"})
		return
	}

	if newU := strings.TrimSpace(req.NewUsername); newU != "" {
		m.username = newU
	}
	m.passwordHash = hashAdminPassword(req.NewPassword)

	// Revoke all other sessions except current
	for tok, s := range m.sessions {
		if tok != sess.Token {
			delete(m.sessions, tok)
		} else {
			s.Username = m.username
			m.sessions[tok] = s
		}
	}

	_ = m.saveStateLocked()
	m.recordAuditLocked(m.username, getClientIP(r), "password_changed", "Admin credentials updated and other sessions revoked")

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": m.username,
		"message":  "Admin credentials updated successfully.",
	})
}

func (m *AdminManager) handleConfig(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authenticateRequest(r, r.Method != http.MethodGet)
	if !ok {
		writeAdminJSON(w, http.StatusUnauthorized, map[string]any{"error": "Unauthorized"})
		return
	}

	if r.Method == http.MethodGet {
		m.mu.RLock()
		opts := m.options
		defBal := m.defaultBalance
		m.mu.RUnlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"options":         opts,
			"default_balance": defBal,
		})
		return
	}

	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeAdminJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}

	var incoming AdminRuntimeOptions
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON payload: " + err.Error()})
		return
	}

	// Sanitize bounds
	if incoming.MaxWildDemons < 1 {
		incoming.MaxWildDemons = 1
	}
	if incoming.MaxWildDemons > 2000 {
		incoming.MaxWildDemons = 2000
	}
	if incoming.EvolveIntervalSec < 10 {
		incoming.EvolveIntervalSec = 10
	}
	if incoming.GlobalAngelWPM < 10 {
		incoming.GlobalAngelWPM = 10
	}
	if incoming.GlobalAngelWPM > 500 {
		incoming.GlobalAngelWPM = 500
	}
	if incoming.DemonBalance.StacksPerDamageTier < 1 {
		incoming.DemonBalance.StacksPerDamageTier = 5
	}
	if incoming.DemonBalance.CharSyncTolerance < 1 {
		incoming.DemonBalance.CharSyncTolerance = 5
	}
	if incoming.DemonBalance.PunctuationPerCheckpoint < 1 {
		incoming.DemonBalance.PunctuationPerCheckpoint = 2
	}
	if incoming.DemonBalance.MinDemonWPM < 1 {
		incoming.DemonBalance.MinDemonWPM = 6
	}
	if incoming.DemonBalance.PassageBurstBaseDamage < 1 {
		incoming.DemonBalance.PassageBurstBaseDamage = 30
	}
	if incoming.DemonBalance.MinPassageBurstDamage < 1 {
		incoming.DemonBalance.MinPassageBurstDamage = 5
	}
	if incoming.DemonBalance.MaxPassageBurstDamage < incoming.DemonBalance.MinPassageBurstDamage {
		incoming.DemonBalance.MaxPassageBurstDamage = 300
	}
	if incoming.DemonBalance.PassageBurstMultiplier <= 0 {
		incoming.DemonBalance.PassageBurstMultiplier = 1.0
	}

	m.mu.Lock()
	m.options = incoming
	m.applyOptionsToSubsystemsLocked()
	_ = m.saveStateLocked()
	m.recordAuditLocked(sess.Username, getClientIP(r), "update_config", fmt.Sprintf("Updated runtime options & demon balance (BaseBurst=%d, Tolerance=±%d, MaxDemons=%d)", incoming.DemonBalance.PassageBurstBaseDamage, incoming.DemonBalance.CharSyncTolerance, incoming.MaxWildDemons))
	optsCopy := m.options
	m.mu.Unlock()

	if globalHub != nil {
		globalHub.BroadcastAdminConfigUpdate(optsCopy)
	}

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"options": optsCopy,
		"message": "Configuration applied live and broadcast to connected clients.",
	})
}

func (m *AdminManager) handleResetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}
	sess, ok := m.authenticateRequest(r, true)
	if !ok {
		writeAdminJSON(w, http.StatusUnauthorized, map[string]any{"error": "Unauthorized"})
		return
	}

	m.mu.Lock()
	m.options = defaultAdminRuntimeOptions(m.defaultBalance)
	m.applyOptionsToSubsystemsLocked()
	_ = m.saveStateLocked()
	m.recordAuditLocked(sess.Username, getClientIP(r), "reset_config", "Reset all runtime options and demon balance parameters to factory defaults")
	optsCopy := m.options
	m.mu.Unlock()

	if globalHub != nil {
		globalHub.BroadcastAdminConfigUpdate(optsCopy)
	}

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"options": optsCopy,
		"message": "All parameters reset to defaults.",
	})
}

// ---- Analytics, Usage & Store Telemetry ----

type AdminUserRow struct {
	UID                string    `json:"uid"`
	Username           string    `json:"username"`
	DisplayName        string    `json:"display_name"`
	Email              string    `json:"email"`
	IsRegistered       bool      `json:"is_registered"`
	CurrentStreak      int       `json:"current_streak"`
	LongestStreak      int       `json:"longest_streak"`
	TotalVersesTyped   int       `json:"total_verses_typed"`
	TotalCharsTyped    int       `json:"total_chars_typed"`
	TotalSeconds       int       `json:"total_seconds"`
	TopWPM             int       `json:"top_wpm"`
	AvgWPM             int       `json:"avg_wpm"`
	GuardianLevel      int       `json:"guardian_level"`
	GuardianPower      int       `json:"guardian_power"`
	GuardianWards      int       `json:"guardian_wards"`
	DemonsDefeated     int       `json:"demons_defeated"`
	PassageBurstDamage int       `json:"passage_burst_damage"`
	TrackerUnlocked    bool      `json:"tracker_unlocked"`
	BadgesCount        int       `json:"badges_count"`
	FavoritesCount     int       `json:"favorites_count"`
	LastActive         string    `json:"last_active"`
	IsOnline           bool      `json:"is_online"`
}

type AdminBookUsage struct {
	BookName string `json:"book_name"`
	Sessions int    `json:"sessions"`
	Chars    int    `json:"chars"`
	AvgWPM   int    `json:"avg_wpm"`
}

type AdminVerseUsage struct {
	Key         string `json:"key"`
	Completions int    `json:"completions"`
}

type AdminDailyActivity struct {
	Date     string `json:"date"`
	Sessions int    `json:"sessions"`
	Chars    int    `json:"chars"`
	AvgWPM   int    `json:"avg_wpm"`
}

type AdminAnalyticsSnapshot struct {
	TotalRegisteredAccounts int                  `json:"total_registered_accounts"`
	TotalUniqueUsers        int                  `json:"total_unique_users"`
	ActiveUsersToday        int                  `json:"active_users_today"`
	ActiveUsers7d           int                  `json:"active_users_7d"`
	OnlineConnections       int                  `json:"online_connections"`
	TotalSessions           int                  `json:"total_sessions"`
	SessionsToday           int                  `json:"sessions_today"`
	SessionsLastHour        int                  `json:"sessions_last_hour"`
	TotalVersesCompleted    int                  `json:"total_verses_completed"`
	TotalCharsTyped         int                  `json:"total_chars_typed"`
	TotalTypingHours        float64              `json:"total_typing_hours"`
	GlobalAvgWPM            int                  `json:"global_avg_wpm"`
	GlobalTopWPM            int                  `json:"global_top_wpm"`
	TotalFavorites          int                  `json:"total_favorites"`
	TotalBadgesAwarded      int                  `json:"total_badges_awarded"`
	TotalComments           int                  `json:"total_comments"`
	FlaggedCommentsCount    int                  `json:"flagged_comments_count"`
	TotalChatMessages       int                  `json:"total_chat_messages"`
	TotalDailyRaces         int                  `json:"total_daily_races"`
	TotalDemonsDefeated     int                  `json:"total_demons_defeated"`
	TranslationsBreakdown   map[string]int       `json:"translations_breakdown"`
	WPMDistribution         map[string]int       `json:"wpm_distribution"`
	TopBooks                []AdminBookUsage     `json:"top_books"`
	TopVerses               []AdminVerseUsage    `json:"top_verses"`
	DailyTrend              []AdminDailyActivity `json:"daily_trend"`
	Users                   []AdminUserRow       `json:"users"`
	RecentComments          []VerseComment       `json:"recent_comments"`
	RecentChat              []ChatMessage        `json:"recent_chat"`
	ActiveBossRaid          *BossRaid            `json:"active_boss_raid,omitempty"`
}

func collectAdminAnalytics(ctx context.Context) AdminAnalyticsSnapshot {
	snap := AdminAnalyticsSnapshot{
		TranslationsBreakdown: make(map[string]int),
		WPMDistribution: map[string]int{
			"0-39 WPM":    0,
			"40-59 WPM":   0,
			"60-79 WPM":   0,
			"80-99 WPM":   0,
			"100-119 WPM": 0,
			"120+ WPM":    0,
		},
		TopBooks:       make([]AdminBookUsage, 0),
		TopVerses:      make([]AdminVerseUsage, 0),
		DailyTrend:     make([]AdminDailyActivity, 0),
		Users:          make([]AdminUserRow, 0),
		RecentComments: make([]VerseComment, 0),
		RecentChat:     make([]ChatMessage, 0),
	}

	onlineUIDs := make(map[string]bool)
	if globalHub != nil {
		globalHub.mu.RLock()
		snap.OnlineConnections = len(globalHub.clients)
		for c := range globalHub.clients {
			onlineUIDs[c.UID] = true
		}
		globalHub.mu.RUnlock()
	}

	todayStr := time.Now().UTC().Format("2006-01-02")
	sevenDaysAgo := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	oneHourAgo := time.Now().UTC().Add(-1 * time.Hour)

	if ls, ok := store.(*localStore); ok && ls != nil {
		ls.mu.Lock()
		defer ls.mu.Unlock()

		snap.TotalRegisteredAccounts = len(ls.data.Accounts)
		uniqueUIDs := make(map[string]bool)
		for uid := range ls.data.Accounts {
			uniqueUIDs[uid] = true
		}
		for uid := range ls.data.Streaks {
			uniqueUIDs[uid] = true
		}
		for uid := range ls.data.Progress {
			uniqueUIDs[uid] = true
		}
		for uid := range ls.data.SanctuarySettings {
			uniqueUIDs[uid] = true
		}

		userTopWPM := make(map[string]int)
		userWpmSum := make(map[string]int)
		userWpmCount := make(map[string]int)

		bookStats := make(map[string]*struct {
			sessions int
			chars    int
			wpmSum   int
		})
		dailyStats := make(map[string]*struct {
			sessions int
			chars    int
			wpmSum   int
		})
		for i := 13; i >= 0; i-- {
			d := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
			dailyStats[d] = &struct {
				sessions int
				chars    int
				wpmSum   int
			}{}
		}

		totalWpmSum := 0
		totalWpmCount := 0
		for _, sess := range ls.data.Sessions {
			if sess == nil {
				continue
			}
			uniqueUIDs[sess.UID] = true
			snap.TotalSessions++
			dStr := sess.CreatedAt.UTC().Format("2006-01-02")
			if dStr == todayStr {
				snap.SessionsToday++
			}
			if sess.CreatedAt.After(oneHourAgo) {
				snap.SessionsLastHour++
			}
			tr := sess.Translation
			if tr == "" {
				tr = "kjv"
			}
			snap.TranslationsBreakdown[tr]++

			wpm := sess.Stats.WPM
			chars := sess.Stats.CharsTyped
			if wpm > 0 {
				totalWpmSum += wpm
				totalWpmCount++
				if wpm > snap.GlobalTopWPM {
					snap.GlobalTopWPM = wpm
				}
				if wpm > userTopWPM[sess.UID] {
					userTopWPM[sess.UID] = wpm
				}
				userWpmSum[sess.UID] += wpm
				userWpmCount[sess.UID]++

				switch {
				case wpm < 40:
					snap.WPMDistribution["0-39 WPM"]++
				case wpm < 60:
					snap.WPMDistribution["40-59 WPM"]++
				case wpm < 80:
					snap.WPMDistribution["60-79 WPM"]++
				case wpm < 100:
					snap.WPMDistribution["80-99 WPM"]++
				case wpm < 120:
					snap.WPMDistribution["100-119 WPM"]++
				default:
					snap.WPMDistribution["120+ WPM"]++
				}
			}

			if sess.BookName != "" {
				bs, ok := bookStats[sess.BookName]
				if !ok {
					bs = &struct {
						sessions int
						chars    int
						wpmSum   int
					}{}
					bookStats[sess.BookName] = bs
				}
				bs.sessions++
				bs.chars += chars
				bs.wpmSum += wpm
			}

			if ds, ok := dailyStats[dStr]; ok {
				ds.sessions++
				ds.chars += chars
				ds.wpmSum += wpm
			}
		}

		if totalWpmCount > 0 {
			snap.GlobalAvgWPM = totalWpmSum / totalWpmCount
		}

		for bName, bs := range bookStats {
			avg := 0
			if bs.sessions > 0 {
				avg = bs.wpmSum / bs.sessions
			}
			snap.TopBooks = append(snap.TopBooks, AdminBookUsage{
				BookName: bName,
				Sessions: bs.sessions,
				Chars:    bs.chars,
				AvgWPM:   avg,
			})
		}
		sort.Slice(snap.TopBooks, func(i, j int) bool {
			return snap.TopBooks[i].Sessions > snap.TopBooks[j].Sessions
		})
		if len(snap.TopBooks) > 15 {
			snap.TopBooks = snap.TopBooks[:15]
		}

		for i := 13; i >= 0; i-- {
			d := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
			ds := dailyStats[d]
			avg := 0
			if ds.sessions > 0 {
				avg = ds.wpmSum / ds.sessions
			}
			snap.DailyTrend = append(snap.DailyTrend, AdminDailyActivity{
				Date:     d,
				Sessions: ds.sessions,
				Chars:    ds.chars,
				AvgWPM:   avg,
			})
		}

		for k, count := range ls.data.GlobalVerseCompletions {
			snap.TotalVersesCompleted += count
			snap.TopVerses = append(snap.TopVerses, AdminVerseUsage{
				Key:         k,
				Completions: count,
			})
		}
		sort.Slice(snap.TopVerses, func(i, j int) bool {
			return snap.TopVerses[i].Completions > snap.TopVerses[j].Completions
		})
		if len(snap.TopVerses) > 15 {
			snap.TopVerses = snap.TopVerses[:15]
		}

		totalSecs := 0
		for _, st := range ls.data.Streaks {
			if st == nil {
				continue
			}
			snap.TotalCharsTyped += st.TotalCharsTyped
			totalSecs += st.TotalSeconds
			if st.LastActiveDate == todayStr {
				snap.ActiveUsersToday++
			}
			if st.LastActiveDate >= sevenDaysAgo {
				snap.ActiveUsers7d++
			}
		}
		snap.TotalTypingHours = math.Round((float64(totalSecs)/3600.0)*100) / 100

		for _, favs := range ls.data.Favorites {
			snap.TotalFavorites += len(favs)
		}
		for _, bgs := range ls.data.Badges {
			snap.TotalBadgesAwarded += len(bgs)
		}
		for _, sc := range ls.data.SanctuarySettings {
			snap.TotalDemonsDefeated += sc.DemonsDefeated
		}

		snap.TotalComments = len(ls.data.Comments)
		for i := len(ls.data.Comments) - 1; i >= 0; i-- {
			c := ls.data.Comments[i]
			if c.Flagged {
				snap.FlaggedCommentsCount++
			}
			if len(snap.RecentComments) < 30 {
				snap.RecentComments = append(snap.RecentComments, c)
			}
		}

		snap.TotalChatMessages = len(ls.data.Chat)
		for i := len(ls.data.Chat) - 1; i >= 0 && len(snap.RecentChat) < 30; i-- {
			snap.RecentChat = append(snap.RecentChat, ls.data.Chat[i])
		}

		snap.TotalDailyRaces = len(ls.data.Races)
		if ls.data.BossRaid != nil {
			brCopy := *ls.data.BossRaid
			snap.ActiveBossRaid = &brCopy
		}

		snap.TotalUniqueUsers = len(uniqueUIDs)

		for uid := range uniqueUIDs {
			if strings.HasPrefix(uid, "angel-") || strings.HasPrefix(uid, "demon-") {
				continue
			}
			row := AdminUserRow{
				UID:                uid,
				DisplayName:        "Guest (" + uid[:min(8, len(uid))] + ")",
				GuardianLevel:      1,
				GuardianPower:      10,
				PassageBurstDamage: DefaultDemonSyncBalance.PassageBurstBaseDamage,
				IsOnline:           onlineUIDs[uid],
			}
			if acc, ok := ls.data.Accounts[uid]; ok && acc != nil {
				row.Username = acc.Username
				row.DisplayName = acc.DisplayName
				row.Email = acc.Email
				row.IsRegistered = true
				row.LastActive = acc.LastActive.UTC().Format("2006-01-02 15:04")
			}
			if st, ok := ls.data.Streaks[uid]; ok && st != nil {
				row.CurrentStreak = st.CurrentStreak
				row.LongestStreak = st.LongestStreak
				row.TotalVersesTyped = st.TotalVersesTyped
				row.TotalCharsTyped = st.TotalCharsTyped
				row.TotalSeconds = st.TotalSeconds
				if row.LastActive == "" && st.LastActiveDate != "" {
					row.LastActive = st.LastActiveDate
				}
			}
			if sc, ok := ls.data.SanctuarySettings[uid]; ok {
				if sc.GuardianLevel > 0 {
					row.GuardianLevel = sc.GuardianLevel
				}
				row.GuardianWards = (row.GuardianLevel - 1) / 3
				row.GuardianPower = 10 + (row.GuardianLevel-1)*2
				if sc.GuardianPower > row.GuardianPower {
					row.GuardianPower = sc.GuardianPower
				}
				row.DemonsDefeated = sc.DemonsDefeated
				if sc.PassageBurstDamage > 0 {
					row.PassageBurstDamage = sc.PassageBurstDamage
				}
				row.TrackerUnlocked = sc.DemonsDefeated >= 6 || sc.TrackerUnlocked
			}
			row.TopWPM = userTopWPM[uid]
			if cnt := userWpmCount[uid]; cnt > 0 {
				row.AvgWPM = userWpmSum[uid] / cnt
			}
			row.BadgesCount = len(ls.data.Badges[uid])
			row.FavoritesCount = len(ls.data.Favorites[uid])
			snap.Users = append(snap.Users, row)
		}

		sort.Slice(snap.Users, func(i, j int) bool {
			if snap.Users[i].IsOnline != snap.Users[j].IsOnline {
				return snap.Users[i].IsOnline
			}
			if snap.Users[i].TotalVersesTyped != snap.Users[j].TotalVersesTyped {
				return snap.Users[i].TotalVersesTyped > snap.Users[j].TotalVersesTyped
			}
			return snap.Users[i].UID < snap.Users[j].UID
		})
		if len(snap.Users) > 100 {
			snap.Users = snap.Users[:100]
		}
		return snap
	}

	// PostgreSQL store fallback
	if ps, ok := store.(*postgresStore); ok && ps != nil {
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&snap.TotalRegisteredAccounts)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&snap.TotalUniqueUsers)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_streaks WHERE last_active_date = $1`, todayStr).Scan(&snap.ActiveUsersToday)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_streaks WHERE last_active_date >= $1`, sevenDaysAgo).Scan(&snap.ActiveUsers7d)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(AVG(wpm),0)::int, COALESCE(MAX(wpm),0) FROM typing_sessions WHERE wpm > 0`).Scan(&snap.TotalSessions, &snap.GlobalAvgWPM, &snap.GlobalTopWPM)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM typing_sessions WHERE started_at::date = CURRENT_DATE`).Scan(&snap.SessionsToday)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM typing_sessions WHERE started_at >= CURRENT_TIMESTAMP - interval '1 hour'`).Scan(&snap.SessionsLastHour)
		var totalSecs int
		_ = ps.pool.QueryRow(ctx, `SELECT COALESCE(SUM(total_verses_typed),0), COALESCE(SUM(total_chars_typed),0), COALESCE(SUM(total_seconds),0) FROM user_streaks`).Scan(&snap.TotalVersesCompleted, &snap.TotalCharsTyped, &totalSecs)
		snap.TotalTypingHours = math.Round((float64(totalSecs)/3600.0)*100) / 100
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM favorites`).Scan(&snap.TotalFavorites)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_badges`).Scan(&snap.TotalBadgesAwarded)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE flagged = TRUE) FROM verse_comments`).Scan(&snap.TotalComments, &snap.FlaggedCommentsCount)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages`).Scan(&snap.TotalChatMessages)
		_ = ps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM daily_races`).Scan(&snap.TotalDailyRaces)
		_ = ps.pool.QueryRow(ctx, `SELECT COALESCE(SUM(demons_defeated),0) FROM sanctuary_settings`).Scan(&snap.TotalDemonsDefeated)
		if br, err := ps.GetBossRaid(ctx); err == nil {
			snap.ActiveBossRaid = br
		}
	}

	return snap
}

// ---- Live Debug & System Diagnostics Collector ----

type AdminConnectedClient struct {
	UID            string    `json:"uid"`
	DisplayName    string    `json:"display_name"`
	AvatarColor    string    `json:"avatar_color"`
	StreakDays     int       `json:"streak_days"`
	Translation    string    `json:"translation"`
	Book           string    `json:"book"`
	Chapter        int       `json:"chapter"`
	Verse          int       `json:"verse"`
	CharIndex      int       `json:"char_index"`
	WPM            int       `json:"wpm"`
	IsSpectator    bool      `json:"is_spectator"`
	VerseStartTime time.Time `json:"verse_start_time"`
}

type AdminDebugSnapshot struct {
	ServerStartTime      time.Time              `json:"server_start_time"`
	UptimeSeconds        int64                  `json:"uptime_seconds"`
	UptimeHuman          string                 `json:"uptime_human"`
	GoVersion            string                 `json:"go_version"`
	GOOS                 string                 `json:"goos"`
	GOARCH               string                 `json:"goarch"`
	NumCPU               int                    `json:"num_cpu"`
	NumGoroutine         int                    `json:"num_goroutine"`
	MemAllocMB           float64                `json:"mem_alloc_mb"`
	MemTotalAllocMB      float64                `json:"mem_total_alloc_mb"`
	MemSysMB             float64                `json:"mem_sys_mb"`
	MemHeapInuseMB       float64                `json:"mem_heap_inuse_mb"`
	MemHeapObjects       uint64                 `json:"mem_heap_objects"`
	MemStackInuseMB      float64                `json:"mem_stack_inuse_mb"`
	NumGC                uint32                 `json:"num_gc"`
	LastGCTime           string                 `json:"last_gc_time"`
	PauseTotalMs         float64                `json:"pause_total_ms"`
	StoreType            string                 `json:"store_type"`
	StoreFilePath        string                 `json:"store_file_path"`
	StoreFileSizeBytes   int64                  `json:"store_file_size_bytes"`
	LoadedTranslations   map[string]int         `json:"loaded_translations"`
	BookGuidesENCount    int                    `json:"book_guides_en_count"`
	BookGuidesESCount    int                    `json:"book_guides_es_count"`
	ConnectedClients     []AdminConnectedClient `json:"connected_clients"`
	WildDemons           []WildDemon            `json:"wild_demons"`
	SlainDemonsPoolCount int                    `json:"slain_demons_pool_count"`
	DynamicSpawnSec      int                    `json:"dynamic_spawn_sec"`
	GlobalAngel          *AngelInfo             `json:"global_angel,omitempty"`
	Devil24_7VersesCount int                    `json:"devil_24_7_verses_count"`
	Devil24_7Location    string                 `json:"devil_24_7_location"`
	ActiveAdminSessions  []adminSession         `json:"active_admin_sessions"`
	AuditLog             []AdminAuditEntry      `json:"audit_log"`
}

func (m *AdminManager) collectAdminDebugInfo() AdminDebugSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	uptime := time.Since(serverStartTime)
	lastGC := "Never"
	if mem.LastGC > 0 {
		lastGC = time.Unix(0, int64(mem.LastGC)).UTC().Format("2006-01-02 15:04:05 UTC")
	}

	storeType := "postgres"
	storePath := "DATABASE_URL"
	var storeSize int64
	if ls, ok := store.(*localStore); ok && ls != nil {
		storeType = "local"
		storePath = ls.path
		if fi, err := os.Stat(ls.path); err == nil {
			storeSize = fi.Size()
		}
	}

	transCounts := make(map[string]int, len(translations))
	for id, td := range translations {
		if td != nil {
			transCounts[id] = len(td.Verses)
		}
	}

	clients := make([]AdminConnectedClient, 0)
	if globalHub != nil {
		globalHub.mu.RLock()
		for c := range globalHub.clients {
			clients = append(clients, AdminConnectedClient{
				UID:            c.UID,
				DisplayName:    c.DisplayName,
				AvatarColor:    c.AvatarColor,
				StreakDays:     c.StreakDays,
				Translation:    c.Translation,
				Book:           c.Book,
				Chapter:        c.Chapter,
				Verse:          c.Verse,
				CharIndex:      c.CharIndex,
				WPM:            c.WPM,
				IsSpectator:    c.IsSpectator,
				VerseStartTime: c.VerseStartTime,
			})
		}
		globalHub.mu.RUnlock()
	}

	var wildDemons []WildDemon
	slainCount := 0
	dynSpawnSec := 0
	devil24Count := 0
	devil24Loc := ""
	if globalDemonManager != nil {
		wildDemons = globalDemonManager.GetWildDemons()
		dynSpawnSec = int(globalDemonManager.GetDynamicSpawnInterval().Seconds())
		devil24Count, devil24Loc, _ = globalDemonManager.GetDevil24_7Status()
		globalDemonManager.mu.RLock()
		slainCount = len(globalDemonManager.slainDemons)
		globalDemonManager.mu.RUnlock()
	}

	var globalAngel *AngelInfo
	if globalAngelManager != nil {
		globalAngel = globalAngelManager.GetGlobalAngelStatus()
	}

	m.mu.RLock()
	activeSessions := make([]adminSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		masked := s
		if len(masked.Token) > 10 {
			masked.Token = masked.Token[:8] + "..."
		}
		masked.CSRFToken = "***"
		activeSessions = append(activeSessions, masked)
	}
	auditCopy := make([]AdminAuditEntry, len(m.auditLog))
	copy(auditCopy, m.auditLog)
	m.mu.RUnlock()

	return AdminDebugSnapshot{
		ServerStartTime:      serverStartTime,
		UptimeSeconds:        int64(uptime.Seconds()),
		UptimeHuman:          uptime.Truncate(time.Second).String(),
		GoVersion:            runtime.Version(),
		GOOS:                 runtime.GOOS,
		GOARCH:               runtime.GOARCH,
		NumCPU:               runtime.NumCPU(),
		NumGoroutine:         runtime.NumGoroutine(),
		MemAllocMB:           math.Round(float64(mem.Alloc)/1024/1024*100) / 100,
		MemTotalAllocMB:      math.Round(float64(mem.TotalAlloc)/1024/1024*100) / 100,
		MemSysMB:             math.Round(float64(mem.Sys)/1024/1024*100) / 100,
		MemHeapInuseMB:       math.Round(float64(mem.HeapInuse)/1024/1024*100) / 100,
		MemHeapObjects:       mem.HeapObjects,
		MemStackInuseMB:      math.Round(float64(mem.StackInuse)/1024/1024*100) / 100,
		NumGC:                mem.NumGC,
		LastGCTime:           lastGC,
		PauseTotalMs:         math.Round(float64(mem.PauseTotalNs)/1e6*100) / 100,
		StoreType:            storeType,
		StoreFilePath:        storePath,
		StoreFileSizeBytes:   storeSize,
		LoadedTranslations:   transCounts,
		BookGuidesENCount:    len(bookGuides),
		BookGuidesESCount:    len(bookGuidesES),
		ConnectedClients:     clients,
		WildDemons:           wildDemons,
		SlainDemonsPoolCount: slainCount,
		DynamicSpawnSec:      dynSpawnSec,
		GlobalAngel:          globalAngel,
		Devil24_7VersesCount: devil24Count,
		Devil24_7Location:    devil24Loc,
		ActiveAdminSessions:  activeSessions,
		AuditLog:             auditCopy,
	}
}

func (m *AdminManager) handleDashboard(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.authenticateRequest(r, false)
	if !ok {
		writeAdminJSON(w, http.StatusUnauthorized, map[string]any{"error": "Unauthorized"})
		return
	}

	m.mu.RLock()
	opts := m.options
	defBal := m.defaultBalance
	m.mu.RUnlock()

	analytics := collectAdminAnalytics(r.Context())
	debugInfo := m.collectAdminDebugInfo()

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"username":        sess.Username,
		"csrf_token":      sess.CSRFToken,
		"options":         opts,
		"default_balance": defBal,
		"analytics":       analytics,
		"debug":           debugInfo,
	})
}

// ---- Live Admin Actions Handler ----

func (m *AdminManager) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
		return
	}
	sess, ok := m.authenticateRequest(r, true)
	if !ok {
		writeAdminJSON(w, http.StatusUnauthorized, map[string]any{"error": "Unauthorized"})
		return
	}

	var req struct {
		Action             string `json:"action"`
		DemonID            string `json:"demon_id"`
		Rank               string `json:"rank"`
		Level              int    `json:"level"`
		WPM                int    `json:"wpm"`
		HP                 int    `json:"hp"`
		Name               string `json:"name"`
		BookName           string `json:"book_name"`
		Chapter            int    `json:"chapter"`
		Verse              int    `json:"verse"`
		Theme              string `json:"theme"`
		Message            string `json:"message"`
		IsDemonBoss        bool   `json:"is_demon_boss"`
		UID                string `json:"uid"`
		GuardianLevel      int    `json:"guardian_level"`
		GuardianPower      int    `json:"guardian_power"`
		DemonsDefeated     int    `json:"demons_defeated"`
		PassageBurstDamage int    `json:"passage_burst_damage"`
		CommentID          int    `json:"comment_id"`
		ChatID             int    `json:"chat_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}

	ip := getClientIP(r)
	ctx := r.Context()

	switch req.Action {
	case "trigger_mini_swarm":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		spawned := globalDemonManager.TriggerMiniSwarm(req.Theme)
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "trigger_mini_swarm", fmt.Sprintf("Spawned %d demons in mini-swarm (theme=%q)", spawned, req.Theme))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Triggered Mini-Swarm: spawned %d wild demons!", spawned),
		})

	case "trigger_major_swarm":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		spawned := globalDemonManager.TriggerMajorSwarm()
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "trigger_major_swarm", fmt.Sprintf("Spawned %d demons in Sunday Major Swarm", spawned))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Triggered Major Swarm: spawned %d wild demons!", spawned),
		})

	case "evolve_demons":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		evolved := globalDemonManager.ForceEvolveAllDemons()
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "evolve_demons", fmt.Sprintf("Force-evolved %d living wild demons", len(evolved)))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Evolved %d living wild demons to the next level!", len(evolved)),
		})

	case "spawn_demon":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		var spawned *WildDemon
		if req.BookName != "" && req.Chapter > 0 && req.Verse > 0 {
			spawned = globalDemonManager.SpawnAdminCustomWildDemon(req.BookName, req.Chapter, req.Verse, req.Rank, req.Level, req.WPM, req.HP, req.Name)
		} else {
			spawned = globalDemonManager.SpawnPeriodicWildDemon()
		}
		if spawned == nil {
			writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "Could not spawn demon (pool full or invalid scripture reference)"})
			return
		}
		if globalHub != nil {
			globalHub.BroadcastWildDemonsList(globalDemonManager.GetWildDemons())
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "spawn_demon", fmt.Sprintf("Spawned %s (%s Lv.%d) at %s %d:%d", spawned.Name, spawned.Rank, spawned.Level, spawned.BookName, spawned.Chapter, spawned.Verse))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"wild_demon": spawned,
			"message":    fmt.Sprintf("Spawned %s %s at %s %d:%d!", spawned.Icon, spawned.Name, spawned.BookName, spawned.Chapter, spawned.Verse),
		})

	case "slay_demon":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		slain := globalDemonManager.AdminSlayWildDemonByID(req.DemonID)
		if slain == nil {
			writeAdminJSON(w, http.StatusNotFound, map[string]any{"error": "Demon not found"})
			return
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "slay_demon", fmt.Sprintf("Smote wild demon %s (%s) at %s %d:%d", slain.Name, slain.ID, slain.BookName, slain.Chapter, slain.Verse))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Exorcised %s from %s %d:%d!", slain.Name, slain.BookName, slain.Chapter, slain.Verse),
		})

	case "advance_demon":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		adv, nextV, ok := globalDemonManager.AdvanceWildDemonToNextVerse(req.DemonID)
		if !ok || adv == nil {
			writeAdminJSON(w, http.StatusNotFound, map[string]any{"error": "Could not advance demon"})
			return
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "advance_demon", fmt.Sprintf("Advanced %s (%s) to %s %d:%d", adv.Name, adv.ID, nextV.BookName, nextV.Chapter, nextV.Verse))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Advanced %s to %s %d:%d!", adv.Name, nextV.BookName, nextV.Chapter, nextV.Verse),
		})

	case "respawn_demon":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		resp := globalDemonManager.RespawnWildDemon(req.DemonID)
		if resp == nil {
			writeAdminJSON(w, http.StatusNotFound, map[string]any{"error": "Demon not found"})
			return
		}
		if globalHub != nil {
			globalHub.BroadcastWildDemonsList(globalDemonManager.GetWildDemons())
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "respawn_demon", fmt.Sprintf("Respawned %s at %s %d:%d", resp.Name, resp.BookName, resp.Chapter, resp.Verse))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Respawned %s at %s %d:%d!", resp.Name, resp.BookName, resp.Chapter, resp.Verse),
		})

	case "slay_all_demons":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		count := globalDemonManager.AdminSlayAllWildDemons()
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "slay_all_demons", fmt.Sprintf("Purged all %d active wild demons", count))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Purged all %d active wild demons! Scripture is 100%% sanctified.", count),
		})

	case "reset_demons":
		if globalDemonManager == nil {
			writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "DemonManager not initialized"})
			return
		}
		count := globalDemonManager.AdminResetDefaultWildDemons()
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "reset_demons", fmt.Sprintf("Reset wild demon pool to %d default adversaries", count))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Reset wild demon pool to %d canonical adversaries.", count),
		})

	case "reset_boss_raid":
		lvl := req.Level
		if lvl < 1 || lvl > 4 {
			lvl = 2
		}
		raid := defaultBossRaidForLevel(lvl, req.IsDemonBoss)
		if req.HP > 0 {
			raid.MaxHP = req.HP
			raid.CurrentHP = req.HP
		}
		if req.WPM > 0 {
			raid.BossWPM = req.WPM
		}
		_ = store.ResetBossRaid(ctx, *raid)
		if globalHub != nil {
			globalHub.BroadcastBossRaidUpdate(*raid, nil)
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "reset_boss_raid", fmt.Sprintf("Reset Boss Raid to %s Lv.%d (%d HP, %d WPM)", raid.BossName, raid.Level, raid.MaxHP, raid.BossWPM))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"boss_raid": raid,
			"message":   fmt.Sprintf("Reset Boss Raid: %s (Lv. %d, %d HP, %d WPM)!", raid.BossName, raid.Level, raid.MaxHP, raid.BossWPM),
		})

	case "broadcast_announcement":
		msgText := strings.TrimSpace(req.Message)
		if msgText == "" {
			writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "Announcement message cannot be empty"})
			return
		}
		if globalHub != nil {
			activeCount := 0
			if globalDemonManager != nil {
				activeCount = len(globalDemonManager.GetWildDemons())
			}
			sanct := max(0, int((1.0-float64(activeCount)/666.0)*100))
			globalHub.BroadcastSwarmEvent("📣 ADMIN DECREE: "+msgText, activeCount, 666, sanct)
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "broadcast_announcement", msgText)
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": "Broadcast announcement sent to all connected clients.",
		})

	case "update_user":
		if req.UID == "" {
			writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "UID is required"})
			return
		}
		customs, err := store.GetCustomizations(ctx, req.UID)
		if err != nil || customs == nil {
			customs = &SanctuarySettings{TextColor: "emerald", FontFamily: "sans", CaretAura: "default", AudioProfile: "classic", CelebrateFx: "confetti"}
		}
		if req.GuardianLevel > 0 {
			customs.GuardianLevel = req.GuardianLevel
		}
		if req.GuardianPower > 0 {
			customs.GuardianPower = req.GuardianPower
		}
		if req.DemonsDefeated >= 0 {
			customs.DemonsDefeated = req.DemonsDefeated
			customs.TrackerUnlocked = customs.DemonsDefeated >= 6
		}
		if req.PassageBurstDamage > 0 {
			customs.PassageBurstDamage = req.PassageBurstDamage
		}
		_ = store.SaveCustomizations(ctx, req.UID, *customs)
		if globalHub != nil {
			globalHub.SendToUID(req.UID, Message{
				Type:            "sanctuary_data",
				Sanctuary:       customs,
				TrackerUnlocked: customs.TrackerUnlocked,
				DemonsDefeated:  customs.DemonsDefeated,
			})
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "update_user", fmt.Sprintf("Updated user %s (GuardianLv=%d, DemonsDefeated=%d, Burst=%d)", req.UID, customs.GuardianLevel, customs.DemonsDefeated, customs.PassageBurstDamage))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Updated user %s settings.", req.UID),
		})

	case "delete_comment":
		if ls, ok := store.(*localStore); ok && ls != nil {
			ls.mu.Lock()
			for i, c := range ls.data.Comments {
				if c.ID == req.CommentID {
					ls.data.Comments = append(ls.data.Comments[:i], ls.data.Comments[i+1:]...)
					break
				}
			}
			_ = ls.save()
			ls.mu.Unlock()
		} else if ps, ok := store.(*postgresStore); ok && ps != nil {
			_, _ = ps.pool.Exec(ctx, `DELETE FROM verse_comments WHERE id = $1`, req.CommentID)
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "delete_comment", fmt.Sprintf("Deleted comment ID %d", req.CommentID))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Comment deleted."})

	case "unflag_comment":
		if ls, ok := store.(*localStore); ok && ls != nil {
			ls.mu.Lock()
			for i := range ls.data.Comments {
				if ls.data.Comments[i].ID == req.CommentID {
					ls.data.Comments[i].Flagged = false
					break
				}
			}
			_ = ls.save()
			ls.mu.Unlock()
		} else if ps, ok := store.(*postgresStore); ok && ps != nil {
			_, _ = ps.pool.Exec(ctx, `UPDATE verse_comments SET flagged = FALSE WHERE id = $1`, req.CommentID)
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "unflag_comment", fmt.Sprintf("Unflagged comment ID %d", req.CommentID))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Comment unflagged."})

	case "delete_chat":
		if ls, ok := store.(*localStore); ok && ls != nil {
			ls.mu.Lock()
			for i, c := range ls.data.Chat {
				if c.ID == req.ChatID {
					ls.data.Chat = append(ls.data.Chat[:i], ls.data.Chat[i+1:]...)
					break
				}
			}
			_ = ls.save()
			ls.mu.Unlock()
		} else if ps, ok := store.(*postgresStore); ok && ps != nil {
			_, _ = ps.pool.Exec(ctx, `DELETE FROM chat_messages WHERE id = $1`, req.ChatID)
		}
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "delete_chat", fmt.Sprintf("Deleted chat message ID %d", req.ChatID))
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Chat message deleted."})

	case "force_gc":
		runtime.GC()
		debug.FreeOSMemory()
		m.mu.Lock()
		m.recordAuditLocked(sess.Username, ip, "force_gc", "Triggered manual garbage collection & OS memory release")
		m.mu.Unlock()
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Garbage collection completed."})

	default:
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{"error": "Unknown admin action: " + req.Action})
	}
}

// ---- Helper Methods on Hub & DemonManager for Admin Operations ----

func (h *Hub) BroadcastAdminConfigUpdate(opts AdminRuntimeOptions) {
	h.mu.RLock()
	conns := make([]*safeConn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c.conn)
	}
	h.mu.RUnlock()

	bal := opts.DemonBalance
	out := Message{
		Type:          "balance_config_update",
		BalanceConfig: &bal,
	}
	for _, conn := range conns {
		_ = conn.WriteJSON(out)
	}
}

func (m *DemonManager) ForceEvolveAllDemons() []WildDemon {
	m.mu.Lock()
	var evolved []WildDemon
	now := time.Now().UTC()
	for _, d := range m.wildDemons {
		if d.CurrentHP <= 0 {
			continue
		}
		if d.MaxLevel <= 0 {
			d.MaxLevel = 5
		}
		if d.Level < d.MaxLevel {
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
			evolved = append(evolved, *d)
		}
	}
	allDemons := m.getWildDemonsLocked()
	m.mu.Unlock()

	if len(evolved) > 0 && m.hub != nil {
		m.hub.BroadcastWildDemonsList(allDemons)
	}
	return evolved
}

func (m *DemonManager) SpawnAdminCustomWildDemon(book string, ch, v int, rank string, level, wpm, hp int, customName string) *WildDemon {
	m.mu.Lock()
	defer m.mu.Unlock()

	td, ok := translations["kjv"]
	if !ok {
		return nil
	}
	vList, ok := td.VersesByBook[book]
	if !ok || len(vList) == 0 {
		return nil
	}
	text := ""
	for _, item := range vList {
		if item.Chapter == ch && item.Verse == v {
			text = cleanVerseText(item.Text)
			break
		}
	}
	if text == "" {
		text = cleanVerseText(vList[0].Text)
		ch = vList[0].Chapter
		v = vList[0].Verse
	}

	if rank == "" {
		rank = "fiend"
	}
	if level < 1 {
		level = 1
	}
	if level > 5 {
		level = 5
	}
	if wpm < 6 {
		wpm = 14
	}
	if hp < 20 {
		hp = 120 + level*60
	}

	icon := "👹"
	color := "#dc2626"
	title := "Admin Summoned Adversary"
	switch rank {
	case "imp":
		icon = "😈"
		color = "#ef4444"
		title = "Tempter of the Void"
	case "tormentor":
		icon = "👺"
		color = "#b91c1c"
		title = "Accuser of the Brethren"
	case "apollyon":
		icon = "👑🔥"
		color = "#991b1b"
		title = "Harbinger of Abaddon"
	}
	if customName == "" {
		customName = fmt.Sprintf("Summoned %s of %s", strings.Title(rank), book)
	}

	now := time.Now().UTC()
	demon := &WildDemon{
		ID:          fmt.Sprintf("wild-admin-%d", now.UnixNano()%1000000),
		Rank:        rank,
		Level:       level,
		MaxLevel:    5,
		Name:        customName,
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
		CharIndex:   0,
		SpawnedAt:   now,
		LastEvolve:  now,
	}
	m.wildDemons = append(m.wildDemons, demon)
	m.wildDemonBases[demon.ID] = wpm
	go m.runWildDemonLoop(demon)
	res := *demon
	return &res
}

func (m *DemonManager) AdminSlayWildDemonByID(demonID string) *WildDemon {
	m.mu.Lock()
	var slain *WildDemon
	for idx, d := range m.wildDemons {
		if d.ID == demonID {
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
		content := fmt.Sprintf("⚡ Admin Decree smote %s %s (Lv. %d) at %s %d:%d!", slain.Icon, slain.Name, slain.Level, slain.BookName, slain.Chapter, slain.Verse)
		m.hub.BroadcastMichaelSlay(slain.Translation, slain.BookName, slain.Chapter, slain.Verse, *slain, content)
		m.hub.BroadcastWildDemonsList(allDemons)
	}
	return slain
}

func (m *DemonManager) AdminSlayAllWildDemons() int {
	m.mu.Lock()
	count := len(m.wildDemons)
	for _, d := range m.wildDemons {
		d.CurrentHP = 0
		m.slainDemons[d.ID] = d
	}
	m.wildDemons = nil
	m.mu.Unlock()

	if m.hub != nil {
		m.hub.BroadcastWildDemonsList([]WildDemon{})
	}
	return count
}

func (m *DemonManager) AdminResetDefaultWildDemons() int {
	m.mu.Lock()
	for _, d := range m.wildDemons {
		d.CurrentHP = 0
	}
	m.wildDemons = initWildDemons()
	m.wildDemonBases = make(map[string]int)
	for _, d := range m.wildDemons {
		m.wildDemonBases[d.ID] = d.WPM
		go m.runWildDemonLoop(d)
	}
	allDemons := m.getWildDemonsLocked()
	m.mu.Unlock()

	if m.hub != nil {
		m.hub.BroadcastWildDemonsList(allDemons)
	}
	return len(allDemons)
}

func registerAdminRoutes(mux *http.ServeMux) {
	if globalAdmin == nil {
		initAdminSystem(".elespe")
	}
	mux.HandleFunc("/admin", globalAdmin.handleAdminPage)
	mux.HandleFunc("/admin/", globalAdmin.handleAdminPage)
	mux.HandleFunc("/admin.html", globalAdmin.handleAdminPage)
	mux.HandleFunc("/api/admin/session", globalAdmin.handleSessionStatus)
	mux.HandleFunc("/api/admin/login", globalAdmin.handleLogin)
	mux.HandleFunc("/api/admin/logout", globalAdmin.handleLogout)
	mux.HandleFunc("/api/admin/security/password", globalAdmin.handleChangePassword)
	mux.HandleFunc("/api/admin/dashboard", globalAdmin.handleDashboard)
	mux.HandleFunc("/api/admin/config", globalAdmin.handleConfig)
	mux.HandleFunc("/api/admin/config/reset", globalAdmin.handleResetConfig)
	mux.HandleFunc("/api/admin/action", globalAdmin.handleAction)
}

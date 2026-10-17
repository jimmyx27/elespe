# elespe — Future Features

## Guiding principles

- Free and usable with zero account. Anonymous by default.
- Accounts are optional; logging in must NEVER lose progress (merge, never replace).
- The server should only own state a single client cannot: shared, cross-device,
  or real-time collaborative state.

## 1. Identity: anonymous-first, optional accounts

Anonymous users already get a `uid` in `localStorage` (index.html) that the server
trusts. Enhancements:

- OAuth / email / passkey login that LINKS to the existing `uid` instead of
  creating a blank profile.
- "Claim your progress" merge flow: on first login, the server migrates the
  anonymous uid's progress, favorites, streaks, and badges into the account.
- Conflict resolution when both the anonymous uid and the account have progress
  (e.g. take max `currentVerse`, union badges/favorites, max streak).
- Cross-device sync once logged in; anonymous still works fully offline.
- Optional display name / avatar (needed for presence and races).

## 2. Live presence (see other typists)

- Colored "ghost" cursors showing where other users are on the same verse.
- "X typists on this verse right now" indicator.
- Opt-in privacy toggle; presence off by default or per-book.
- Spectator mode to watch a book progress live.
- Requires a shared connection hub keyed by {translation, book, verse}.

## 3. Daily challenge race (Verse of the Day)

- The Verse of the Day IS the challenge run: everyone who opts in races the
  exact same passage at the same time; first correct submit wins.
- Rotating featured passages: challenge runs may also use the top 5
  most-favorited or most-commented verses, so the community's favorite
  scripture becomes the day's race.
- One race format only — no separate ghost races, matchmaking queues, or
  tournament brackets.
- Server-side timing and verification so the winner can't be spoofed
  (see section 9).
- Results feed live presence and crowd counters ("N racing this verse now").
- Per-challenge leaderboard (daily / weekly) plus personal bests.

## 4. Live crowd metrics

- Book dropdown shows "N typing now" per book.
- Global ticker: verses typed in the last hour / all-time counters.
- Popular verse heatmap; "trending passages".

## 5. Verse comments & reflection (community, friendship, faith)

- Leave comments / reflections on any verse, keyed by
  {translation, book, chapter, verse}.
- Inline comments panel in the typing view, so people pausing on a verse can
  read what it has meant to others and add their own.
- Wall for the app is to foster community, friendship, and faith — comments
  should encourage testimony, encouragement, and shared reflection, not debate.
- Faith-oriented prompts (e.g. "What does this verse mean to you today?",
  "Share a prayer or encouragement") to guide thoughtful participation.
- Threaded replies, plus lightweight reactions (e.g. 🙏, ❤️, "amen").
- Private vs public: let users keep a personal reflection journal that stays
  private, or share publicly with the community.
- "Follow this verse" to get notified when others add a reflection.
- Surface popular/shared reflections at book review time as a collective voice.
- Community trust & safety:
  - Reporting/flagging, rate limiting, and spam/profanity filtering.
  - Comments require a display name (optional account); anonymous users can
    still read and type, so the free/no-account path is never blocked.
  - Clear community guidelines / code of conduct.
  - Comment author shows their streak/level badge to reward positive members.

## 6. Social

- Friends / follow list.
- Share streak and achievement cards.
- Team/guild aggregate progress on shared goals.
- Notifications when a friend beats your time.
- Live chat sidebar: a real-time chat panel bound to the current book/verse
  and to daily challenge races, so community and friendship happen alongside
  typing.
  - Reuses the comment trust & safety rules (reporting/flagging, rate limits,
    display-name-to-post, guidelines/conduct).

## 7. Personal analytics & leaderboards

- History charts from the existing `typing_sessions` table.
- Percentile rankings ("faster than X% of typists").
- Streak and WPM leaderboards (global / friends / per book).

## 8. Offline / PWA

- Service worker + IndexedDB so the app works with no connection.
- Local write queue that syncs progress when back online.

## 9. Server-side integrity

- Today WPM is client-reported and spoofable (main.go `content` handler).
- For races/leaderboards, verify timing server-side per submission.
- Replay/audit of suspicious sessions.

## 10. Infrastructure implications

- Introduce a connection hub/registry: {book, verse} -> active connections,
  extending the current per-connection loop.
- Broadcast fan-out for cursors/counts; keepalive already handled via ping/pong.
- Multi-instance requires a broker (Redis pub/sub) for cross-node broadcast.
- Bigger binary/memory: datasets are already fully in RAM (main.go).
- Consider demoting the WS game loop to client-side and keeping WS for
  presence/races/sync if you want offline-first behavior.

## 11. Ideas / backlog

Loose collection of further improvements, roughly grouped. Not sequenced; pull
from here as capacity allows.

### A. Trust, legality & correctness

- Copyright: ESV, NIV, NLT, and HCSB are copyrighted; only KJV, ASV, and BBE are
  reliably public domain. Resolve licensing before public release.
- Server-signed anonymous tokens: replace trusting the `?uid=` query param so
  social features (comments, chat, races) can't be hijacked.
- Data export and delete-account (GDPR-friendly).
- Streak grace / Sabbath rest: a user can request a grace period by submitting
  a reason; it is capped (limited number per period and/or a maximum duration)
  so it stays intentional and can't be farmed for infinite streaks.

### B. Faith formation & retention

- Reading plans / devotionals, including topical plans (anxiety, gratitude, grief).
- Memorization mode: words fade out with spaced repetition so it trains recall,
  not just speed.
- Prayer wall: sibling to verse comments, for requests and thanksgiving.
- Groups / small groups: shared plans, discussion, and group goals.
- Co-typing: two users type the same verse together in sync (collaborative).
- Mentor / accountability partner who can see progress and encourage.

### C. Typing craft

- Adaptive drills generated from the characters a user mistypes most.
- Difficulty modes: case sensitivity, strict punctuation, and "blind" (no
  mistake feedback).
- Audio: public-domain KJV narration or TTS while typing.
- Mobile mode: the current UX assumes a physical keyboard.

### D. Accessibility & reach

- i18n / localized UI and non-English scripture sets.
- Screen-reader semantics and focus management; dyslexia-friendly font,
  high-contrast theme, larger text.
- Theme and font personalization beyond the automatic atmosphere backgrounds.

### E. Architecture & operations

- Lazy-load translations instead of holding all datasets in RAM at once.
- Observability: metrics, structured logs, richer health checks.
- Content pipeline for adding translations/guides without editing `main.go`.
- CI plus expanded test coverage.

## Suggested sequencing

1. Optional-account linking + non-destructive merge (unlocks everything else).
2. Shared hub + per-book "N typing now" (cheapest live win).
3. Live cursors on the same verse.
4. Verse comments + live chat sidebar.
5. Daily challenge race (Verse of the Day) + leaderboards.
6. Social + personal analytics.
7. PWA/offline.

# elespe

Praise the Sun! \\[T]/ A keyboard-typing trainer for the Holy Bible. Type each verse accurately to advance through books, keep daily streaks, favorite verses, earn completion badges, and type required book intros and reviews with dynamic atmospheric book backgrounds.

## Features

- **7 Bible Translations**:
  - **KJV** — King James Version (1769 Authorized Version)
  - **ESV** — English Standard Version
  - **NIV** — New International Version
  - **NLT** — New Living Translation
  - **HCSB** — Holman Christian Standard Bible (CSB)
  - **ASV** — American Standard Version (1901)
  - **BBE** — Bible in Basic English (1965)
  - Switch translations seamlessly at any time; progress and stats are tracked per translation.
- **Mandatory Typable Intros & Locked Reviews**:
  - **Mandatory Book Intro**: Before starting any book, users must type the book introduction (author, date, historical setting, major themes, and key verse) to unlock Chapter 1.
  - **View Intro Anytime**: Click "📖 Intro" anytime to view the rich overview modal.
  - **Locked Book Review**: Book reviews and reflections are locked (🔒) until every verse in the book has been typed.
  - **Mandatory Book Review**: Typing the final review is mandatory to achieve 100% completion and earn the book completion trophy.
- **Dynamic Atmospheric Backgrounds**:
  - The application background, ambient glow, and accent colors automatically morph based on the genre and atmosphere of the current book:
    - *The Law / Pentateuch*: Warm Sinai sunrise, desert amber & golden glow.
    - *Historical Books*: Ancient fortress stone, royal bronze & ruby crimson.
    - *Wisdom & Poetry*: Midnight starry sanctuary, royal indigo & sapphire twilight.
    - *Prophets*: Prophetic embers, fiery sunset & copper glow.
    - *The Gospels & Acts*: Sea of Galilee living water, olive dawn & turquoise.
    - *The Epistles*: Mediterranean azure & apostolic parchment blue.
    - *Apocalypse / Revelation*: Celestial city, crystal sea & radiant amethyst.
- **Accurate Real-Time WPM Tracking**:
  - Measured live on every keystroke in the browser from the moment typing starts.
  - Idle pauses (>2.5s) are not penalized against your active typing speed.
  - Rolling active WPM and instant verse WPM displayed smoothly in real time.
- **Character-by-Character Live Typing UI**:
  - Real-time character highlighting (correct letters, mistakes with red wavy underline, active blinking cursor).
  - Backspace error correction: fix mistakes on the fly without losing progress.
  - **Enter to Submit**: Typers must explicitly submit completed passages by pressing `<Enter ↵>` (or clicking the Submit button); typing the last character does not automatically skip ahead, giving typers full control to review before advancing.
  - Optional synthesized mechanical keyboard sounds and completion chimes via Web Audio API (with 1-click mute toggle).
  - Reliable Light and Dark mode toggle with persistent preferences.
- **Favorites & Practice Mode**:
  - Star (★ / ☆) any verse to save it to your Favorites drawer.
  - Practice typing any favorited passage at any time.
- **Daily Streaks & Activity Tracker**:
  - Tracks consecutive daily typing streaks (🔥), longest streaks, today's verses count, total verses, and active typing time.
- **Rewards & Trophy Room**:
  - Individual completion trophies for all 66 books of the Bible.
  - Milestone achievements: First Verse, Century Club (100 verses), Pentateuch Pioneer, Gospel Bearer, Speed Scribe (40/60+ WPM), Streak badges, and more!
  - Confetti celebration when completing books and unlocking badges.
- **Scripture Progression & Locked Verse Protection**:
  - Skipping ahead to uncompleted passages is strictly prohibited on both client and server: locked verses display with `🔒` in the verse picker and cannot be jumped to.
  - Navigate back to previously unlocked verses at any time with `◀` / `▶` buttons or `Alt+Left` / `Alt+Right` without losing milestone progress.
- **Persistent Storage**:
  - Zero-configuration local JSON store (`.elespe/data.json`) for instant out-of-the-box usage (`go run .`).
  - Production-ready PostgreSQL storage with automatic schema migrations.

## Running locally

No database or build step needed — Postgres is optional.

```bash
go run .
```

Then open `http://localhost:8080`.

For PostgreSQL:

```bash
export DATABASE_URL="postgres://user:pass@localhost:5432/elespe"
go run .
```

## Testing

Run unit and integration tests:

```bash
go test -v ./...
```
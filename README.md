> **Moved:** this project now lives in the monorepo  
> **https://github.com/niiithish/shopify-spy** (`backend/` folder).  
> Prefer that repo for new work.

---

# Shopify App Store Scraper

A Go-based scraper for the Shopify App Store that stores results in Turso (SQLite) database.

## Features

- 🔍 Search Shopify App Store by keyword
- 🎯 Filter results by relevance score
- 📊 Scrape detailed app information (ratings, reviews, recent activity)
- 💾 Store results in Turso database (with JSON backup)
- 📈 Query and manage stored results
- 🗺️ **Sitemap discovery** — find *all* App Store apps (no keyword needed), skip known URLs, sequential agent-browser detail scrape, crash-safe resume

## Prerequisites

- Go 1.23 or higher
- [agent-browser](https://github.com/mariozechner/agent-browser) CLI tool
- Turso account (https://turso.tech/)

## Setup

1. **Install dependencies:**
   ```bash
   go mod tidy
   ```

2. **Configure Turso database:**
   ```bash
   cp .env.example .env
   # Edit .env with your Turso credentials
   ```

3. **Set environment variables:**
   ```bash
   export TURSO_DATABASE_URL="libsql://your-database.turso.io"
   export TURSO_AUTH_TOKEN="your-token"
   ```

## Usage

### Scrape and Store Results

```bash
./shopify-scraper "price monitoring"
```

This will:
1. Search the Shopify App Store for "price monitoring"
2. Filter results by relevance (30%+ score, top 5)
3. Scrape detailed information for each app
4. Store results in Turso database
5. Save backup to JSON file

### Query Existing Results

```bash
./shopify-scraper --query "price monitoring"
# or
./shopify-scraper -q "price monitoring"
```

### List All Keywords

```bash
./shopify-scraper --list
# or
./shopify-scraper -l
```

### Show Database Statistics

```bash
./shopify-scraper --stats
# or
./shopify-scraper -s
```

### Show Help

```bash
./shopify-scraper --help
# or
./shopify-scraper -h
```

### Sitemap discovery (full catalog, no keyword)

Uses Shopify's public EN sitemap (`~23k` app URLs), diffs against apps already in the DB, then detail-scrapes **new** apps one-by-one with **agent-browser** (same stack as the Go scraper).

```bash
# One-time: create apps table + backfill known apps from search_results
python3 discover.py init

# Fetch sitemap and queue unknown slugs (no browser)
python3 discover.py sync-sitemap

# Detail-scrape up to N new apps (sequential, resume-safe)
python3 discover.py run --limit 2000 --delay 20

# Status anytime
python3 discover.py status
# or
python3 db.py --discover-status

# Live log while a run is active
tail -f discover_run.log
```

**Resume / crash safety:** progress is stored in the `apps` table (`scrape_status`, `last_scraped_at`). Re-running only processes `pending` rows — finished apps are never re-scraped.

**Dedupe key:** app slug / URL (not title).

**DB notes:**
- Canonical catalog → `apps` table
- Keyword research stays in `search_results`
- Successful discovery scrapes are also mirrored into `search_results` with keyword `__sitemap__` so existing tooling can see them

## Database Schema

### `search_results` (keyword research)

The scraper creates a `search_results` table with the following columns:

| Column | Type | Description |
|--------|------|-------------|
| `id` | INTEGER | Primary key |
| `keyword` | TEXT | Search keyword |
| `title` | TEXT | App title |
| `url` | TEXT | App URL |
| `rating` | TEXT | Star rating |
| `review_count` | TEXT | Total review count |
| `price` | TEXT | Pricing info |
| `relevance_score` | REAL | Relevance percentage |
| `recent_reviews_30_days` | INTEGER | Reviews in last 30 days |
| `trending_score` | REAL | % of recent reviews vs total (higher = more trending) |
| `created_at` | DATETIME | First seen timestamp |
| `updated_at` | DATETIME | Last update timestamp |

**Trending Score** indicates how "hot" an app is. A high trending score means most of the app's total reviews came in the last 30 days, suggesting it's either new or gaining popularity rapidly. Example: An app with 10 total reviews where 5 came in the last 30 days has a 50% trending score.

### `apps` (catalog / discovery)

| Column | Type | Description |
|--------|------|-------------|
| `slug` | TEXT UNIQUE | App Store slug (from URL) |
| `url` | TEXT UNIQUE | Canonical listing URL |
| `title` / `rating` / `review_count` / `price` | TEXT | Listing fields |
| `recent_reviews_30_days` | INTEGER | Recent review momentum |
| `trending_score` | REAL | % recent vs total reviews |
| `source` | TEXT | `sitemap` or `keyword` |
| `scrape_status` | TEXT | `pending` / `done` / `failed` |
| `last_scraped_at` | DATETIME | When detail scrape finished |
| `first_seen_at` | DATETIME | When first discovered |
| `lastmod` | TEXT | Sitemap lastmod (if from sitemap) |
| `scrape_attempts` / `last_error` | | Retry / error tracking |

## Project Structure

```
.
├── main.go              # Keyword scrape entry point
├── scraper.go           # Phase 1: Search and extract
├── relevance.go         # Phase 2: Relevance scoring
├── detail.go            # Phase 3: Recent reviews via agent-browser
├── db/
│   └── db.go            # Turso client (Go)
├── discover.py          # Sitemap discovery (full catalog)
├── run_discover.sh      # Long-running discovery helper
├── db.py                # Quick Turso SQL / status CLI
├── go.mod / go.sum
├── mise.toml            # Optional Go toolchain pin
├── requirements.txt     # Notes only (stdlib + agent-browser)
└── README.md
```

Local-only (gitignored): `.env`, `shopify-scraper` binary, `*.log`, JSON dumps.

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `TURSO_DATABASE_URL` | Yes | Turso database URL (e.g., `libsql://shopify-spy-nithish.aws-ap-south-1.turso.io`) |
| `TURSO_AUTH_TOKEN` | Yes | Turso authentication token |

## How It Works

### Phase 1: Search and Extract
- Opens Shopify App Store search page using agent-browser
- Extracts app titles, ratings, review counts, and prices
- Collects app URLs from page links

### Phase 2: Relevance Filtering
- Calculates relevance score based on keyword matching
- Filters apps with score ≥ 30%
- Returns top 5 most relevant apps

### Phase 3: Detail Scraping
- Visits each app's reviews page
- Counts reviews from last 30 days
- Checks up to 2 pages of reviews

### Phase 4: Database Storage
- Saves all app data to Turso database
- Updates existing records if app already exists for keyword
- Creates backup JSON file

## License

MIT

# Shopify App Store Scraper

A Go-based scraper for the Shopify App Store that stores results in Turso (SQLite) database.

## Features

- 🔍 Search Shopify App Store by keyword
- 🎯 Filter results by relevance score
- 📊 Scrape detailed app information (ratings, reviews, recent activity)
- 💾 Store results in Turso database (with JSON backup)
- 📈 Query and manage stored results

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

## Database Schema

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

## Project Structure

```
.
├── main.go              # Main entry point
├── scraper.go           # Phase 1: Search and extract
├── relevance.go         # Phase 2: Relevance filtering
├── detail.go            # Phase 3: App detail scraping
├── db/
│   └── db.go           # Turso database client
├── go.mod              # Go module definition
├── go.sum              # Go dependencies
└── README.md           # This file
```

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

/*
Shopify App Store Scraper - Go Version
Phase 1: Search and extract apps
Phase 2: Relevance filtering
Phase 3: Scrape individual app details
Phase 4: Store results in Turso database

Usage:
  ./shopify-scraper "keyword"              - Scrape and store results
  ./shopify-scraper --query "keyword"      - Query existing results from database
  ./shopify-scraper --list                 - List all keywords in database
  ./shopify-scraper --stats                - Show database statistics
  ./shopify-scraper --bulk                 - Run bulk scrape for all keywords
  ./shopify-scraper --bulk-list            - List bulk keywords
*/

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"shopify-scraper/db"
)

func init() {
	// Load .env file if it exists
	_ = godotenv.Load()
}

// Targeted keywords - not too generic, good volume, specific use cases
var bulkKeywords = []string{
	"app builder",
	"bundle",
	"product options",
	"wholesale",
	"pre-order",
	"back in stock",
	"store locator",
	"loyalty rewards",
	"subscription",
	"upsell",
	"age verification",
	"cookie consent",
	"currency converter",
	"page builder",
	"form builder",
	"wishlist",
	"size chart",
	"gift card",
	"referral",
	"affiliate",
}

func main() {
	// Parse command line arguments
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Check for command flags
	arg := os.Args[1]
	
	switch arg {
	case "--query", "-q":
		if len(os.Args) < 3 {
			color.Red("❌ Please provide a keyword to query")
			color.Yellow("Usage: ./shopify-scraper --query \"price monitoring\"")
			os.Exit(1)
		}
		keyword := strings.Join(os.Args[2:], " ")
		queryDatabase(keyword)
		
	case "--list", "-l":
		listKeywords()
		
	case "--stats", "-s":
		showStats()
		
	case "--bulk":
		bulkScrape()
		
	case "--bulk-list":
		listBulkKeywords()
		
	case "--normalize-prices":
		normalizePrices()

	case "--import", "-i":
		if len(os.Args) < 3 {
			color.Red("❌ Please provide a JSON file to import")
			color.Yellow("Usage: ./shopify-scraper --import shopify_apps_keyword.json")
			os.Exit(1)
		}
		importJSONFile(os.Args[2])
		
	case "--help", "-h":
		printUsage()
		
	default:
		// Scrape mode
		keyword := strings.Join(os.Args[1:], " ")
		scrapeAndStore(keyword)
	}
}

// printUsage prints usage information
func printUsage() {
	color.Cyan("Shopify App Store Scraper")
	fmt.Println()
	color.White("Usage:")
	fmt.Println("  ./shopify-scraper \"keyword\"              Scrape and store results")
	fmt.Println("  ./shopify-scraper --query \"keyword\"      Query existing results from database")
	fmt.Println("  ./shopify-scraper --list                 List all keywords in database")
	fmt.Println("  ./shopify-scraper --stats                Show database statistics")
	fmt.Println("  ./shopify-scraper --import <file.json>   Import existing JSON file to database")
	fmt.Println("  ./shopify-scraper --normalize-prices    Normalize price column to Free/Free trial/Paid")
	fmt.Println("  ./shopify-scraper --bulk                 Run bulk scrape for all keywords")
	fmt.Println("  ./shopify-scraper --bulk-list            List bulk keywords")
	fmt.Println()
	color.White("Environment variables:")
	fmt.Println("  TURSO_DATABASE_URL    Turso database URL (e.g., libsql://...)")
	fmt.Println("  TURSO_AUTH_TOKEN      Turso authentication token")
}

// scrapeAndStore performs the full scraping workflow and stores in database
func scrapeAndStore(keyword string) {
	// Initialize database connection
	color.Cyan("🗄️  Connecting to Turso database...")
	database, err := db.New()
	if err != nil {
		color.Red("❌ Failed to connect to database: %v", err)
		color.Yellow("Make sure TURSO_DATABASE_URL and TURSO_AUTH_TOKEN are set")
		os.Exit(1)
	}
	color.Green("✅ Connected to database")

	// Phase 1: Search and extract
	color.Cyan("🔍 Phase 1: Searching for '%s'...", keyword)
	scraper := NewScraper(5)
	apps, err := scraper.SearchAndExtract(keyword)
	if err != nil {
		color.Red("❌ Phase 1 failed: %v", err)
		os.Exit(1)
	}
	color.Green("✅ Phase 1: Found %d raw apps", len(apps))

	// Phase 2: Relevance filtering
	color.Cyan("\n🎯 Phase 2: Filtering by relevance...")
	scorer := NewRelevanceScorer(keyword)
	filteredApps := scorer.FilterAndSort(apps, 30.0, 5)
	color.Green("✅ Phase 2: Filtered to %d relevant apps", len(filteredApps))

	// Display filtered results
	fmt.Println()
	for i, app := range filteredApps {
		color.Yellow("%d. %s (Relevance: %.1f%%)", i+1, app.Title, app.RelevanceScore)
		fmt.Printf("   URL: %s\n", app.URL)
		if app.Rating != "" {
			fmt.Printf("   Rating: %s stars (%s reviews)\n", app.Rating, app.ReviewCount)
		}
		fmt.Println()
	}

	// Phase 3: Scrape app details concurrently
	color.Cyan("🚀 Phase 3: Scraping app details concurrently...")
	detailedApps := scrapeAppDetailsConcurrent(filteredApps, 5)
	color.Green("✅ Phase 3: Scraped details for %d apps\n", len(detailedApps))

	// Phase 4: Save results to database
	color.Cyan("💾 Phase 4: Saving results to database...")
	appResults := convertToAppResults(keyword, detailedApps)
	if err := database.SaveResults(keyword, appResults); err != nil {
		color.Red("❌ Failed to save results to database: %v", err)
		os.Exit(1)
	}
	color.Green("✅ Results saved to database for keyword: %s", keyword)

	// Print summary
	color.Cyan("\n📊 Summary:")
	color.White("   Raw apps found: %d", len(apps))
	color.White("   Relevant apps: %d", len(filteredApps))
	color.White("   Detailed apps: %d", len(detailedApps))
	color.White("   Stored in database: ✅")
}

// queryDatabase retrieves and displays results from the database
func queryDatabase(keyword string) {
	color.Cyan("🗄️  Connecting to Turso database...")
	database, err := db.New()
	if err != nil {
		color.Red("❌ Failed to connect to database: %v", err)
		os.Exit(1)
	}

	color.Cyan("🔍 Querying results for '%s'...", keyword)
	results, err := database.GetResults(keyword)
	if err != nil {
		color.Red("❌ Failed to query results: %v", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		color.Yellow("⚠️  No results found for keyword: %s", keyword)
		return
	}

	color.Green("✅ Found %d results for '%s'\n", len(results), keyword)

	for i, app := range results {
		color.Yellow("%d. %s (Relevance: %.1f%%)", i+1, app.Title, app.RelevanceScore)
		fmt.Printf("   URL: %s\n", app.URL)
		if app.Rating != "" {
			fmt.Printf("   Rating: %s stars (%s reviews)\n", app.Rating, app.ReviewCount)
		}
		fmt.Printf("   Price: %s\n", app.Price)
		fmt.Printf("   Recent reviews (30 days): %d\n", app.RecentReviews30Days)
		if !app.UpdatedAt.IsZero() {
			fmt.Printf("   Last updated: %s\n", app.UpdatedAt.Format("2006-01-02 %H:%M"))
		}
		fmt.Println()
	}
}

// listKeywords lists all keywords stored in the database
func listKeywords() {
	color.Cyan("🗄️  Connecting to Turso database...")
	database, err := db.New()
	if err != nil {
		color.Red("❌ Failed to connect to database: %v", err)
		os.Exit(1)
	}

	color.Cyan("📋 Listing all keywords...")
	keywords, err := database.GetKeywords()
	if err != nil {
		color.Red("❌ Failed to list keywords: %v", err)
		os.Exit(1)
	}

	if len(keywords) == 0 {
		color.Yellow("⚠️  No keywords found in database")
		return
	}

	color.Green("✅ Found %d keywords:\n", len(keywords))
	for i, keyword := range keywords {
		fmt.Printf("  %d. %s\n", i+1, keyword)
	}
}

// showStats displays database statistics
func showStats() {
	color.Cyan("🗄️  Connecting to Turso database...")
	database, err := db.New()
	if err != nil {
		color.Red("❌ Failed to connect to database: %v", err)
		os.Exit(1)
	}

	color.Cyan("📊 Fetching database statistics...")
	stats, err := database.GetStats()
	if err != nil {
		color.Red("❌ Failed to get stats: %v", err)
		os.Exit(1)
	}

	color.Green("✅ Database Statistics:\n")
	color.White("   Total apps: %d", stats["total_apps"])
	color.White("   Total keywords: %d", stats["total_keywords"])
	if latest, ok := stats["latest_scrape"].(string); ok && latest != "" {
		color.White("   Latest scrape: %s", latest)
	}
}

// normalizePrices normalizes all price values in the database
func normalizePrices() {
	color.Cyan("🗄️  Connecting to Turso database...")
	database, err := db.New()
	if err != nil {
		color.Red("❌ Failed to connect to database: %v", err)
		os.Exit(1)
	}

	color.Cyan("🔄 Normalizing price values...")
	updated, err := database.NormalizePrices()
	if err != nil {
		color.Red("❌ Failed to normalize prices: %v", err)
		os.Exit(1)
	}

	color.Green("✅ Normalized %d price values to Free/Free trial/Paid", updated)
}

// listBulkKeywords lists all keywords in the bulk scrape list
func listBulkKeywords() {
	color.Cyan("📋 Bulk Keywords (%d total):", len(bulkKeywords))
	fmt.Println()
	for i, kw := range bulkKeywords {
		fmt.Printf("  %2d. %s\n", i+1, kw)
	}
}

// bulkScrape runs the scraper for all keywords sequentially
func bulkScrape() {
	color.Cyan("🚀 Starting Bulk Scrape")
	color.White("   Keywords: %d", len(bulkKeywords))
	color.White("   Mode: Sequential (one at a time)")
	fmt.Println()

	startTime := time.Now()
	successCount := 0
	failCount := 0
	var failedKeywords []string

	for i, keyword := range bulkKeywords {
		color.Cyan("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		color.Cyan("📍 Keyword %d/%d: %q", i+1, len(bulkKeywords), keyword)
		color.Cyan("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println()

		keywordStart := time.Now()

		// Run the scraper for this keyword
		cmd := exec.Command("./shopify-scraper", keyword)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err := cmd.Run()

		keywordDuration := time.Since(keywordStart)

		if err != nil {
			color.Red("❌ Failed: %s (took %v)", keyword, keywordDuration.Round(time.Second))
			failCount++
			failedKeywords = append(failedKeywords, keyword)
		} else {
			color.Green("✅ Completed: %s (took %v)", keyword, keywordDuration.Round(time.Second))
			successCount++
		}

		// Small delay between keywords to let system breathe
		if i < len(bulkKeywords)-1 {
			color.Yellow("⏳ Waiting 5 seconds before next keyword...")
			time.Sleep(5 * time.Second)
		}
	}

	totalDuration := time.Since(startTime)

	// Summary
	fmt.Println()
	color.Cyan("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	color.Cyan("📊 BULK SCRAPE SUMMARY")
	color.Cyan("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	color.White("   Total keywords: %d", len(bulkKeywords))
	color.Green("   Successful:     %d", successCount)
	if failCount > 0 {
		color.Red("   Failed:         %d", failCount)
		color.Red("   Failed keywords:")
		for _, kw := range failedKeywords {
			color.Red("     - %s", kw)
		}
	}
	color.White("   Total time:     %v", totalDuration.Round(time.Second))
	color.White("   Avg per keyword: %v", (totalDuration/time.Duration(len(bulkKeywords))).Round(time.Second))
	fmt.Println()

	if failCount > 0 {
		os.Exit(1)
	}
}

// scrapeAppDetailsConcurrent scrapes app details concurrently
func scrapeAppDetailsConcurrent(apps []App, maxConcurrency int) []AppDetail {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, maxConcurrency)
	results := make(chan AppDetail, len(apps))

	for _, app := range apps {
		wg.Add(1)
		go func(a App) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			color.Blue("   Scraping: %s", a.Title)
			detail := scrapeSingleApp(a)
			results <- detail
		}(app)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var detailedApps []AppDetail
	for detail := range results {
		detailedApps = append(detailedApps, detail)
	}

	return detailedApps
}

// convertToAppResults converts AppDetail slice to db.AppResult slice
func convertToAppResults(keyword string, apps []AppDetail) []db.AppResult {
	results := make([]db.AppResult, len(apps))
	for i, app := range apps {
		results[i] = db.AppResult{
			Keyword:             keyword,
			Title:               app.Title,
			URL:                 app.URL,
			Rating:              app.Rating,
			ReviewCount:         app.ReviewCount,
			Price:               app.Price,
			RelevanceScore:      app.RelevanceScore,
			RecentReviews30Days: app.RecentReviews30Days,
		}
	}
	return results
}



// importJSONFile imports an existing JSON file to the database
func importJSONFile(filename string) {
	// Extract keyword from filename (shopify_apps_<keyword>.json)
	base := strings.TrimSuffix(filename, ".json")
	if !strings.HasPrefix(base, "shopify_apps_") {
		color.Red("❌ Invalid filename format. Expected: shopify_apps_<keyword>.json")
		os.Exit(1)
	}
	keyword := strings.ReplaceAll(strings.TrimPrefix(base, "shopify_apps_"), "_", " ")

	color.Cyan("📥 Importing %s for keyword '%s'...", filename, keyword)

	// Read JSON file
	data, err := os.ReadFile(filename)
	if err != nil {
		color.Red("❌ Failed to read file: %v", err)
		os.Exit(1)
	}

	// Parse JSON
	var apps []AppDetail
	if err := json.Unmarshal(data, &apps); err != nil {
		color.Red("❌ Failed to parse JSON: %v", err)
		os.Exit(1)
	}

	color.Green("✅ Loaded %d apps from JSON", len(apps))

	// Connect to database
	color.Cyan("🗄️  Connecting to Turso database...")
	database, err := db.New()
	if err != nil {
		color.Red("❌ Failed to connect to database: %v", err)
		os.Exit(1)
	}

	// Convert and save
	appResults := convertToAppResults(keyword, apps)
	if err := database.SaveResults(keyword, appResults); err != nil {
		color.Red("❌ Failed to save results: %v", err)
		os.Exit(1)
	}

	color.Green("✅ Successfully imported %d apps to database for keyword: %s", len(apps), keyword)
}
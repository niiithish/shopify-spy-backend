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
*/

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"shopify-scraper/db"
)

func init() {
	// Load .env file if it exists
	_ = godotenv.Load()
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
		// Still save to JSON as fallback
		saveToJSON(keyword, detailedApps)
	} else {
		color.Green("✅ Results saved to database for keyword: %s", keyword)
	}

	// Also save to JSON for backward compatibility
	saveToJSON(keyword, detailedApps)

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
			Launched:            app.Launched,
			RecentReviews30Days: app.RecentReviews30Days,
		}
	}
	return results
}

// saveToJSON saves results to a JSON file (backward compatibility)
func saveToJSON(keyword string, apps []AppDetail) {
	outputFile := fmt.Sprintf("shopify_apps_%s.json", strings.ReplaceAll(keyword, " ", "_"))
	data, _ := json.MarshalIndent(apps, "", "  ")
	os.WriteFile(outputFile, data, 0644)
	color.Green("💾 Results also saved to: %s", outputFile)
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
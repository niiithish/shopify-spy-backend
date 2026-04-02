/*
Shopify App Store Scraper - Go Version
Phase 1: Search and extract apps
Phase 2: Relevance filtering
Phase 3: Scrape individual app details
*/

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/fatih/color"
)

func main() {
	// Check for keyword argument
	if len(os.Args) < 2 {
		color.Red("❌ Please provide a search keyword")
		color.Yellow("Usage: ./shopify-scraper \"price monitoring\"")
		os.Exit(1)
	}

	keyword := strings.Join(os.Args[1:], " ")

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

	// Save results
	outputFile := fmt.Sprintf("shopify_apps_%s.json", strings.ReplaceAll(keyword, " ", "_"))
	data, _ := json.MarshalIndent(detailedApps, "", "  ")
	os.WriteFile(outputFile, data, 0644)
	color.Green("💾 Results saved to: %s", outputFile)

	// Print summary
	color.Cyan("\n📊 Summary:")
	color.White("   Raw apps found: %d", len(apps))
	color.White("   Relevant apps: %d", len(filteredApps))
	color.White("   Detailed apps: %d", len(detailedApps))
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

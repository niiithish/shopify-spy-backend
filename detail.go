package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Phase 3: Scrape individual app details
// This scrapes:
// - Recent reviews count (last 30 days)

const (
	// Threshold for capping the count (if we reach page 2 and still within 30 days)
	RecentReviewsCap = 20
	RecentReviewsCapLabel = 20 // Display as 20+
)

// scrapeSingleApp scrapes details for a single app
func scrapeSingleApp(app App) AppDetail {
	// Get recent reviews count from the reviews page
	recentCount := scrapeRecentReviewsCount(app.URL)

	return AppDetail{
		App:                 app,
		Launched:            "", // TODO: Can be added later if needed
		RecentReviews30Days: recentCount,
	}
}

// scrapeRecentReviewsCount opens the app's reviews page and counts reviews from last 30 days
// Checks up to 2 pages. If we reach page 2 and still within 30 days, returns capped value (20+)
func scrapeRecentReviewsCount(appURL string) int {
	if appURL == "" {
		return 0
	}

	cutoffDate := time.Now().AddDate(0, 0, -30)
	totalCount := 0
	pageCount := 0

	// Check page 1
	count, hasMore, foundOldReview := scrapeReviewsPage(appURL, 1, cutoffDate)
	totalCount += count
	pageCount++
	
	if foundOldReview {
		// Found a review older than 30 days on page 1, we're done
		return totalCount
	}

	// If page 1 has 10 reviews (full page) and all within 30 days, check page 2
	if hasMore && count >= 10 {
		count, _, foundOldReview = scrapeReviewsPage(appURL, 2, cutoffDate)
		totalCount += count
		pageCount++
		
		// If we reached page 2 and still haven't found old reviews, cap at 20
		// This means there are 20+ reviews in last 30 days
		if !foundOldReview {
			if totalCount > RecentReviewsCap {
				return RecentReviewsCap
			}
		}
	}

	// Close browser after all pages
	runCommand("agent-browser --engine chrome close")

	return totalCount
}

// scrapeReviewsPage scrapes a specific page of reviews and returns count within cutoff date
// Returns: (count, hasMorePages, foundOldReview)
func scrapeReviewsPage(appURL string, page int, cutoffDate time.Time) (int, bool, bool) {
	// Build reviews URL with sort by newest and page
	reviewsURL := fmt.Sprintf("%s/reviews?sort_by=newest&page=%d", appURL, page)

	// Use a unique session per app to avoid conflicts
	sessionName := fmt.Sprintf("reviews_%d", time.Now().UnixNano())
	
	// Open page and get snapshot
	cmd := fmt.Sprintf(
		`agent-browser --session %s --engine chrome open "%s" && agent-browser --session %s wait --load networkidle && agent-browser --session %s snapshot`,
		sessionName, reviewsURL, sessionName, sessionName,
	)

	output, err := runCommand(cmd)
	
	// Always close this session
	defer runCommand(fmt.Sprintf("agent-browser --session %s close", sessionName))
	
	if err != nil {
		return 0, false, false
	}

	// Check for pagination - look for "Go to Page" or page numbers
	hasMore := strings.Contains(output, fmt.Sprintf("Go to Page %d", page+1)) || 
	           strings.Contains(output, fmt.Sprintf("Page %d", page+1))
	
	// Also check if there's a "Next Page" link
	if !hasMore {
		hasMore = strings.Contains(output, "Go to Next Page")
	}
	
	// Count reviews and check if we found any old reviews
	count, foundOldReview := countReviewsOnPage(output, cutoffDate)
	
	return count, hasMore, foundOldReview
}

// countReviewsOnPage parses snapshot and counts reviews within cutoff date
// Returns: (count, foundOldReview)
func countReviewsOnPage(output string, cutoffDate time.Time) (int, bool) {
	count := 0
	foundOldReview := false

	lines := strings.Split(output, "\n")

	for _, line := range lines {
		// Skip lines that contain "replied" (these are merchant replies, not reviews)
		if strings.Contains(line, "replied") {
			continue
		}

		// Look for date pattern in StaticText
		dateRegex := regexp.MustCompile(`StaticText "([A-Za-z]+ \d{1,2}, \d{4})"`)
		matches := dateRegex.FindStringSubmatch(line)

		if len(matches) > 1 {
			dateStr := matches[1]
			reviewDate, err := time.Parse("January 2, 2006", dateStr)
			if err == nil {
				if reviewDate.After(cutoffDate) {
					count++
				} else {
					// Found a review older than 30 days
					foundOldReview = true
				}
			}
		}
	}

	return count, foundOldReview
}

// runCommand executes a shell command
func runCommand(cmd string) (string, error) {
	command := exec.Command("sh", "-c", cmd)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
}

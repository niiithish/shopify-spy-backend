package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/fatih/color"
)

// App represents a basic app from search results
type App struct {
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	Rating         string  `json:"rating"`
	ReviewCount    string  `json:"review_count"`
	Price          string  `json:"price"`
	LinkRef        string  `json:"link_ref"`
	RelevanceScore float64 `json:"relevance_score"`
}

// AppDetail represents detailed app information
type AppDetail struct {
	App
	RecentReviews30Days int `json:"recent_reviews_30_days"`
}

// Browser args for sandbox workaround (required in containers/VMs)
const browserArgs = `--args "--no-sandbox"`

// Scraper handles browser automation via agent-browser CLI
type Scraper struct {
	WaitSeconds int
}

// NewScraper creates a new scraper instance
func NewScraper(waitSeconds int) *Scraper {
	return &Scraper{WaitSeconds: waitSeconds}
}

// runCommand executes agent-browser CLI command
func (s *Scraper) runCommand(cmd string) (string, error) {
	command := exec.Command("sh", "-c", cmd)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command failed: %v", err)
	}
	return string(output), nil
}

// LinkHref represents a link with href from JavaScript eval
type LinkHref struct {
	Text string `json:"text"`
	Href string `json:"href"`
}

// EvalResult represents the JSON result from agent-browser eval
type EvalResult struct {
	Success bool `json:"success"`
	Data    struct {
		Result []LinkHref `json:"result"`
	} `json:"data"`
}

// SearchAndExtract performs Phase 1: search and extract apps
func (s *Scraper) SearchAndExtract(keyword string) ([]App, error) {
	encoded := strings.ReplaceAll(keyword, " ", "%20")
	url := fmt.Sprintf("https://apps.shopify.com/search?q=%s", encoded)

	// Use a session to maintain browser state
	sessionName := "scraper_phase1"

	// Build SINGLE command chain: open, wait, snapshot, eval (all chained with &&)
	// This ensures browser persists between snapshot and eval
	cmd := fmt.Sprintf(
		`agent-browser %s --session %s --engine chrome %s open "%s" && agent-browser %s --session %s wait --load networkidle && agent-browser %s --session %s wait %d000 && agent-browser %s --session %s snapshot -i && agent-browser %s --session %s eval 'Array.from(document.querySelectorAll("a")).filter(a=>{const h=a.href;const p=h.split("/").pop().split("?")[0];return p.length>=3&&!h.includes("/categories/")&&!h.includes("/stories/")&&!h.includes("/login")}).map(a=>({text:a.textContent.trim(),href:a.href.split("?")[0]}))' --json`,
		browserArgs, sessionName, browserArgs, url, browserArgs, sessionName, browserArgs, sessionName, s.WaitSeconds, browserArgs, sessionName, browserArgs, sessionName,
	)

	output, err := s.runCommand(cmd)
	if err != nil {
		return nil, err
	}

	// Parse snapshot for apps (everything before the JSON eval result)
	// Find where the eval JSON starts
	jsonStart := strings.Index(output, "\n{\"success\"")
	if jsonStart < 0 {
		jsonStart = strings.Index(output, "{\"success\"")
	}

	var snapshotOutput, evalOutput string
	if jsonStart > 0 {
		snapshotOutput = output[:jsonStart]
		evalOutput = output[jsonStart:]
	} else {
		snapshotOutput = output
		evalOutput = ""
	}

	// Parse snapshot for apps
	apps := s.parseSnapshot(snapshotOutput)

	// Parse eval output and assign URLs
	if evalOutput != "" {
		color.Blue("Debug: eval output length = %d", len(evalOutput))
		apps = s.assignHrefs(apps, evalOutput)
	}

	// Close browser session
	s.runCommand(fmt.Sprintf("agent-browser %s --session %s close", browserArgs, sessionName))

	return apps, nil
}

// parseSnapshot extracts app data from agent-browser output
func (s *Scraper) parseSnapshot(output string) []App {
	var apps []App
	lines := strings.Split(output, "\n")
	var appButtons []Button

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Match button with app info
		buttonRegex := regexp.MustCompile(`- button "(.+?)"\s+\[ref=(\w+)\]`)
		if matches := buttonRegex.FindStringSubmatch(line); matches != nil {
			text := matches[1]
			// Filter: must contain app-like info
			if strings.Contains(text, "out of 5 stars") || strings.Contains(text, "total reviews") {
				appButtons = append(appButtons, Button{Text: text, Ref: matches[2]})
			}
		}
	}

	// Extract app info from buttons
	seenTitles := make(map[string]bool)

	for _, button := range appButtons {
		app := s.parseAppButton(button.Text)

		// Avoid duplicates
		if app.Title != "" && !seenTitles[app.Title] {
			seenTitles[app.Title] = true
			apps = append(apps, app)
		}
	}

	return apps
}

// parseAppButton extracts structured data from button text
func (s *Scraper) parseAppButton(text string) App {
	// Extract title (everything before rating)
	titleRegex := regexp.MustCompile(`^(.+?)\s+([\d.]+)\s+out\s+of\s+5\s+stars`)
	var title string
	if matches := titleRegex.FindStringSubmatch(text); matches != nil {
		title = strings.TrimSpace(matches[1])
	} else {
		parts := strings.Split(text, "•")
		title = strings.TrimSpace(parts[0])
	}

	// Extract rating
	ratingRegex := regexp.MustCompile(`([\d.]+)\s+out\s+of\s+5\s+stars`)
	rating := ""
	if matches := ratingRegex.FindStringSubmatch(text); matches != nil {
		rating = matches[1]
	}

	// Extract review count
	reviewsRegex := regexp.MustCompile(`(\d+)\s+total\s+reviews`)
	reviews := ""
	if matches := reviewsRegex.FindStringSubmatch(text); matches != nil {
		reviews = matches[1]
	}

	// Extract price info - only the pricing keyword, not the description
	price := ""
	if strings.Contains(text, "•") {
		parts := strings.Split(text, "•")
		if len(parts) > 1 {
			// Price is the first part after •, description follows
			// Common price patterns: "Free", "Free plan available", "Free trial available", "Free to install", "$9.99/month", etc.
			afterBullet := strings.TrimSpace(parts[1])
			
			// Split by common description starters (capital letter after space indicates new sentence)
			// Look for price patterns and stop at first description word
			pricePatterns := []string{
				"Free plan available",
				"Free trial available", 
				"Free to install",
				"Free",
			}
			
			for _, pattern := range pricePatterns {
				if strings.HasPrefix(afterBullet, pattern) {
					price = pattern
					break
				}
			}
			
			// If no pattern matched, take everything up to first sentence-like break
			// (lowercase followed by uppercase usually indicates new sentence)
			if price == "" {
				words := strings.Fields(afterBullet)
				var priceWords []string
				for i, word := range words {
					if i > 0 {
						// Check if previous word ends with period or this word starts with uppercase
						prevWord := words[i-1]
						if strings.HasSuffix(prevWord, ".") || (len(word) > 0 && word[0] >= 'A' && word[0] <= 'Z' && len(prevWord) > 0 && prevWord[len(prevWord)-1] >= 'a' && prevWord[len(prevWord)-1] <= 'z') {
							break
						}
					}
					priceWords = append(priceWords, word)
				}
				price = strings.Join(priceWords, " ")
			}
		}
	}

	return App{
		Title:       title,
		Rating:      rating,
		ReviewCount: reviews,
		Price:       price,
	}
}

// assignHrefs matches apps to hrefs from JavaScript eval output
func (s *Scraper) assignHrefs(apps []App, evalOutput string) []App {
	// Find JSON in output
	jsonStart := strings.Index(evalOutput, "{" )
	if jsonStart < 0 {
		return apps
	}
	jsonStr := evalOutput[jsonStart:]

	var result EvalResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return apps
	}

	// Build href lookup map
	hrefMap := make(map[string]string)
	for _, link := range result.Data.Result {
		hrefMap[strings.TrimSpace(link.Text)] = link.Href
	}

	color.Blue("Debug: found %d links from eval", len(hrefMap))
	// Debug: show some link names
	count := 0
	for text, _ := range hrefMap {
		if count < 10 {
			if len(text) > 40 {
				color.Blue("  Link: '%s'", text[:40])
			} else {
				color.Blue("  Link: '%s'", text)
			}
		}
		count++
	}

	// Assign URLs to apps by title match
	for i := range apps {
		title := strings.TrimSpace(apps[i].Title)
		if href, ok := hrefMap[title]; ok {
			// Exact match - accept if valid app URL
			if isValidAppURL(href) {
				apps[i].URL = href
			}
		} else {
			// Try case-insensitive partial match as fallback
			titleLower := strings.ToLower(title)
			for linkText, href := range hrefMap {
				// Only consider valid app URLs
				if !isValidAppURL(href) {
					continue
				}
				linkLower := strings.ToLower(linkText)
				// Require bidirectional match or significant overlap
				if strings.Contains(linkLower, titleLower) || strings.Contains(titleLower, linkLower) {
					apps[i].URL = href
					break
				}
			}
		}
	}

	return apps
}

// isValidAppURL checks if a URL is a valid app page (not search/category/etc)
func isValidAppURL(href string) bool {
	if href == "" {
		return false
	}
	// Must be apps.shopify.com with a slug
	if !strings.Contains(href, "apps.shopify.com/") {
		return false
	}
	// Exclude non-app pages
	invalidPaths := []string{"/categories/", "/stories/", "/login", "/search", "/sitemap"}
	for _, invalid := range invalidPaths {
		if strings.Contains(href, invalid) {
			return false
		}
	}
	// Must have a slug (last path segment > 3 chars)
	parts := strings.Split(strings.Split(href, "?")[0], "/")
	if len(parts) < 4 || len(parts[len(parts)-1]) < 4 {
		return false
	}
	return true
}

// Button represents a button element
type Button struct {
	Text string
	Ref  string
}

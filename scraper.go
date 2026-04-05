package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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
	Launched            string `json:"launched"`
	RecentReviews30Days int    `json:"recent_reviews_30_days"`
}

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

// SearchAndExtract performs Phase 1: search and extract apps
func (s *Scraper) SearchAndExtract(keyword string) ([]App, error) {
	encoded := strings.ReplaceAll(keyword, " ", "%20")
	url := fmt.Sprintf("https://apps.shopify.com/search?q=%s", encoded)

	// Build command chain
	cmd := fmt.Sprintf(
		`agent-browser --engine chrome open "%s" && agent-browser wait --load networkidle && agent-browser wait %d000 && agent-browser snapshot -i`,
		url, s.WaitSeconds,
	)

	output, err := s.runCommand(cmd)
	if err != nil {
		return nil, err
	}

	// Parse snapshot
	apps, allLinks := s.parseSnapshot(output)

	// Get app URLs
	apps = s.getAppURLs(apps, allLinks)

	// Close browser
	s.runCommand("agent-browser --engine chrome close")

	return apps, nil
}

// parseSnapshot extracts app data from agent-browser output
func (s *Scraper) parseSnapshot(output string) ([]App, []Link) {
	var apps []App
	var allLinks []Link

	lines := strings.Split(output, "\n")

	var appButtons []Button

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Match button with app info
		buttonRegex := regexp.MustCompile(`- button "(.+?)"\s+\[ref=(\w+)\]`)
		if matches := buttonRegex.FindStringSubmatch(line); matches != nil {
			text := matches[1]
			ref := matches[2]
			// Filter: must contain app-like info
			if strings.Contains(text, "out of 5 stars") || strings.Contains(text, "total reviews") {
				appButtons = append(appButtons, Button{Text: text, Ref: ref})
			}
			continue
		}

		// Match link
		linkRegex := regexp.MustCompile(`- link "(.+?)"\s+\[ref=(\w+)\]`)
		if matches := linkRegex.FindStringSubmatch(line); matches != nil {
			text := matches[1]
			ref := matches[2]
			if len(strings.TrimSpace(text)) >= 3 {
				allLinks = append(allLinks, Link{Text: text, Ref: ref})
			}
		}
	}

	// Build ref->text map
	linkMap := make(map[string]string)
	for _, link := range allLinks {
		linkMap[link.Ref] = link.Text
	}

	// Extract app info from buttons
	seenTitles := make(map[string]bool)

	for _, button := range appButtons {
		app := s.parseAppButton(button.Text)

		// Find matching link by consecutive refs
		buttonNumRegex := regexp.MustCompile(`\d+`)
		buttonNumStr := buttonNumRegex.FindString(button.Ref)
		if buttonNumStr != "" {
			buttonNum, _ := strconv.Atoi(buttonNumStr)
			expectedLinkRef := fmt.Sprintf("e%d", buttonNum+1)
			if linkText, ok := linkMap[expectedLinkRef]; ok {
				app.LinkRef = expectedLinkRef
				_ = linkText
			}
		}

		// Avoid duplicates
		if app.Title != "" && !seenTitles[app.Title] {
			seenTitles[app.Title] = true
			apps = append(apps, app)
		}
	}

	return apps, allLinks
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

// getAppURLs fetches actual URLs for apps
func (s *Scraper) getAppURLs(apps []App, allLinks []Link) []App {
	// Build lookup map
	linkTextToRef := make(map[string]string)
	for _, link := range allLinks {
		linkTextToRef[strings.TrimSpace(link.Text)] = link.Ref
	}

	for i := range apps {
		app := &apps[i]
		var linkRef string

		// Try exact match first
		if ref, ok := linkTextToRef[strings.TrimSpace(app.Title)]; ok {
			linkRef = ref
		} else {
			// Try case-insensitive partial match
			titleLower := strings.ToLower(strings.TrimSpace(app.Title))
			for linkText, ref := range linkTextToRef {
				linkLower := strings.ToLower(linkText)
				if strings.Contains(linkLower, titleLower) || strings.Contains(titleLower, linkLower) {
					if len(linkText) >= 5 {
						linkRef = ref
						break
					}
				}
			}
		}

		if linkRef != "" {
			cmd := fmt.Sprintf("agent-browser --engine chrome get attr @%s href", linkRef)
			result, _ := s.runCommand(cmd)
			result = strings.TrimSpace(result)

			// Extract URL
			urlRegex := regexp.MustCompile(`https://apps\.shopify\.com/[^\s]+`)
			if matches := urlRegex.FindStringSubmatch(result); matches != nil {
				fullURL := matches[0]
				// Clean up tracking parameters
				if idx := strings.Index(fullURL, "?"); idx != -1 {
					fullURL = fullURL[:idx]
				}
				app.URL = fullURL
				app.LinkRef = linkRef
			}
		}
	}

	return apps
}

// Button represents a button element
type Button struct {
	Text string
	Ref  string
}

// Link represents a link element
type Link struct {
	Text string
	Ref  string
}

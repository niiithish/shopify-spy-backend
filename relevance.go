package main

import (
	"regexp"
	"sort"
	"strings"
)

// RelevanceScorer implements Phase 2: relevance filtering
type RelevanceScorer struct {
	OriginalQuery string
	Keywords      []string
}

// Keyword expansion map
var keywordMap = map[string][]string{
	"price":      {"price", "pricing", "cost", "fee", "rate"},
	"monitor":    {"monitor", "monitoring", "track", "tracking", "watch", "watching"},
	"competitor": {"competitor", "competition", "competitive", "rival", "market"},
	"dynamic":    {"dynamic", "automated", "auto", "smart", "intelligent"},
	"compare":    {"compare", "comparison", "benchmark", "versus", "vs"},
	"email":      {"email", "mail", "newsletter", "campaign"},
	"marketing":  {"marketing", "promotion", "advertising", "outreach"},
	"order":      {"order", "tracking", "shipment", "shipping", "delivery"},
	"track":      {"track", "tracking", "trace", "status", "locate"},
	"review":     {"review", "feedback", "rating", "testimonial"},
	"inventory":  {"inventory", "stock", "warehouse", "supply"},
	"stock":      {"stock", "inventory", "warehouse"},
	"warehouse":  {"warehouse", "inventory", "storage", "fulfillment"},
	"seo":        {"seo", "search", "ranking", "optimize", "optimization"},
	"shipping":   {"shipping", "delivery", "shipment", "fulfillment"},
	"analytics":  {"analytics", "report", "dashboard", "metrics", "insights"},
	"popup":      {"popup", "pop-up", "modal", "notification"},
	"sms":        {"sms", "text", "message", "messaging"},
}

// Weights for different fields
var weights = map[string]float64{
	"title":       3.0,
	"description": 1.5,
}

// NewRelevanceScorer creates a new scorer for a query
func NewRelevanceScorer(query string) *RelevanceScorer {
	query = strings.ToLower(strings.TrimSpace(query))
	keywords := extractKeywords(query)
	return &RelevanceScorer{
		OriginalQuery: query,
		Keywords:      keywords,
	}
}

// extractKeywords extracts and expands keywords
func extractKeywords(query string) []string {
	wordRegex := regexp.MustCompile(`\b\w+\b`)
	words := wordRegex.FindAllString(query, -1)

	keywords := make(map[string]bool)
	for _, word := range words {
		keywords[word] = true
		// Add related terms
		for key, related := range keywordMap {
			if word == key {
				for _, r := range related {
					keywords[r] = true
				}
			}
			for _, r := range related {
				if word == r {
					keywords[key] = true
					for _, rr := range related {
						keywords[rr] = true
					}
				}
			}
		}
	}

	result := make([]string, 0, len(keywords))
	for k := range keywords {
		result = append(result, k)
	}
	return result
}

// calculateTextScore calculates relevance score for text
func (rs *RelevanceScorer) calculateTextScore(text string) float64 {
	if text == "" {
		return 0.0
	}

	textLower := strings.ToLower(text)

	matches := 0
	for _, keyword := range rs.Keywords {
		if strings.Contains(textLower, keyword) {
			matches++
		}
	}

	if matches == 0 {
		return 0.0
	}

	score := float64(matches) / float64(len(rs.Keywords))
	if score > 1.0 {
		score = 1.0
	}

	// Boost for exact phrase match
	if strings.Contains(textLower, rs.OriginalQuery) {
		score += 0.3
		if score > 1.0 {
			score = 1.0
		}
	}

	return score
}

// scoreApp calculates overall relevance for an app
func (rs *RelevanceScorer) scoreApp(app App) float64 {
	titleScore := rs.calculateTextScore(app.Title) * weights["title"]
	descScore := rs.calculateTextScore(app.Price) * weights["description"]

	totalWeight := weights["title"] + weights["description"]
	finalScore := ((titleScore + descScore) / totalWeight) * 100

	return finalScore
}

// FilterAndSort filters and sorts apps by relevance
func (rs *RelevanceScorer) FilterAndSort(apps []App, threshold float64, minResults int) []App {
	// Score all apps
	for i := range apps {
		apps[i].RelevanceScore = rs.scoreApp(apps[i])
	}

	// Sort by relevance (descending)
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].RelevanceScore > apps[j].RelevanceScore
	})

	// Filter by threshold
	var filtered []App
	for _, app := range apps {
		if app.RelevanceScore >= threshold {
			filtered = append(filtered, app)
		}
	}

	// Ensure minimum results
	if len(filtered) < minResults && len(apps) >= minResults {
		filtered = apps[:minResults]
	}

	return filtered
}

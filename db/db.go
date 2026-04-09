package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// TursoClient handles communication with Turso HTTP API
type TursoClient struct {
	DatabaseURL string
	AuthToken   string
	HTTPClient  *http.Client
}

// AppResult represents a scraped app result stored in the database
type AppResult struct {
	ID                  int64     `json:"id,omitempty"`
	Keyword             string    `json:"keyword"`
	Title               string    `json:"title"`
	URL                 string    `json:"url"`
	Rating              string    `json:"rating"`
	ReviewCount         string    `json:"review_count"`
	Price               string    `json:"price"`
	RelevanceScore      float64   `json:"relevance_score"`
	RecentReviews30Days int       `json:"recent_reviews_30_days"`
	TrendingScore       float64   `json:"trending_score"` // % of recent reviews vs total
	CreatedAt           time.Time `json:"created_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

// TursoRequest represents the request body for Turso API
type TursoRequest struct {
	Requests []StatementRequest `json:"requests"`
}

// StatementRequest represents a single SQL statement
type StatementRequest struct {
	Type string          `json:"type"`
	Stmt StatementDetail `json:"stmt"`
}

// StatementDetail contains the SQL and arguments
type StatementDetail struct {
	SQL  string     `json:"sql"`
	Args []ArgValue `json:"args,omitempty"`
}

// ArgValue represents a typed argument value for Turso API
type ArgValue struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

// TursoResponse represents the response from Turso API
type TursoResponse struct {
	Results []struct {
		Type     string `json:"type"`
		Response struct {
			Type   string `json:"type"`
			Result struct {
				Cols               []Column        `json:"cols"`
				Rows               [][]TypedValue  `json:"rows"`
				AffectedRowCount   int             `json:"affected_row_count"`
				LastInsertRowID    *string         `json:"last_insert_rowid"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error,omitempty"`
		} `json:"response"`
	} `json:"results"`
}

// Column represents a column definition
type Column struct {
	Name      string `json:"name"`
	DeclType  *string `json:"decltype"`
}

// TypedValue represents a typed value in a row
type TypedValue struct {
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

// New creates a new Turso client using environment variables
func New() (*TursoClient, error) {
	databaseURL := os.Getenv("TURSO_DATABASE_URL")
	authToken := os.Getenv("TURSO_AUTH_TOKEN")

	if databaseURL == "" {
		return nil, fmt.Errorf("TURSO_DATABASE_URL environment variable not set")
	}

	// Convert libsql:// to https:// for HTTP API
	apiURL := databaseURL
	if strings.HasPrefix(databaseURL, "libsql://") {
		apiURL = "https://" + strings.TrimPrefix(databaseURL, "libsql://")
	}

	client := &TursoClient{
		DatabaseURL: apiURL,
		AuthToken:   authToken,
		HTTPClient:  &http.Client{Timeout: 30 * time.Second},
	}

	// Create tables if they don't exist
	if err := client.createTables(); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	// Run migrations
	if err := client.migrateDropLaunchedColumn(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}
	if err := client.migrateAddTrendingScore(); err != nil {
		return nil, fmt.Errorf("failed to add trending_score column: %w", err)
	}

	return client, nil
}

// escapeJSON escapes a string for JSON
func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1]) // Remove surrounding quotes
}

// argToJSON converts an argument to Turso JSON format
// Turso API format:
// - text: value is a JSON string (quoted)
// - integer: value is a JSON string (quoted)
// - float: value is a JSON number (unquoted)
// - null: no value field
func argToJSON(v interface{}) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf(`{"type":"text","value":"%s"}`, escapeJSON(val))
	case int:
		return fmt.Sprintf(`{"type":"integer","value":"%d"}`, val)
	case int64:
		return fmt.Sprintf(`{"type":"integer","value":"%d"}`, val)
	case float64:
		return fmt.Sprintf(`{"type":"float","value":%g}`, val)
	case float32:
		return fmt.Sprintf(`{"type":"float","value":%g}`, val)
	case bool:
		if val {
			return `{"type":"integer","value":"1"}`
		}
		return `{"type":"integer","value":"0"}`
	case nil:
		return `{"type":"null"}`
	default:
		return fmt.Sprintf(`{"type":"text","value":"%s"}`, escapeJSON(fmt.Sprintf("%v", v)))
	}
}

// executeQuery executes a SQL query via Turso HTTP API
func (c *TursoClient) executeQuery(sql string, args []interface{}) (*TursoResponse, error) {
	// Build request body manually to handle numeric types correctly
	var requestBuilder strings.Builder
	requestBuilder.WriteString(`{"requests":[{"type":"execute","stmt":{"sql":"`)
	requestBuilder.WriteString(escapeJSON(sql))
	requestBuilder.WriteString(`"`)
	
	if len(args) > 0 {
		requestBuilder.WriteString(`,"args":[`)
		for i, arg := range args {
			if i > 0 {
				requestBuilder.WriteString(",")
			}
			requestBuilder.WriteString(argToJSON(arg))
		}
		requestBuilder.WriteString(`]`)
	}
	
	requestBuilder.WriteString(`}}]}`)
	jsonBody := []byte(requestBuilder.String())

	// Create request
	url := fmt.Sprintf("%s/v2/pipeline", c.DatabaseURL)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.AuthToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.AuthToken))
	}

	// Execute request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result TursoResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(result.Results) == 0 {
		return nil, fmt.Errorf("no results returned")
	}

	if result.Results[0].Response.Error != nil {
		return nil, fmt.Errorf("query error: %s", result.Results[0].Response.Error.Message)
	}

	return &result, nil
}

// createTables creates the necessary tables if they don't exist
func (c *TursoClient) createTables() error {
	// Create table
	query := `CREATE TABLE IF NOT EXISTS search_results (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		keyword TEXT NOT NULL,
		title TEXT NOT NULL,
		url TEXT,
		rating TEXT,
		review_count TEXT,
		price TEXT,
		relevance_score REAL,
		recent_reviews_30_days INTEGER DEFAULT 0,
		trending_score REAL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(keyword, title)
	)`

	_, err := c.executeQuery(query, nil)
	if err != nil {
		return err
	}

	// Create indexes
	_, _ = c.executeQuery("CREATE INDEX IF NOT EXISTS idx_keyword ON search_results(keyword)", nil)
	_, _ = c.executeQuery("CREATE INDEX IF NOT EXISTS idx_created_at ON search_results(created_at)", nil)

	return nil
}

// migrateDropLaunchedColumn removes the launched column if it exists
// SQLite doesn't support DROP COLUMN directly, so we recreate the table
func (c *TursoClient) migrateDropLaunchedColumn() error {
	// Check if launched column exists
	result, err := c.executeQuery("PRAGMA table_info(search_results)", nil)
	if err != nil {
		return err
	}

	hasLaunched := false
	for _, row := range result.Results[0].Response.Result.Rows {
		if len(row) >= 2 {
			colName := getString(row[1])
			if colName == "launched" {
				hasLaunched = true
				break
			}
		}
	}

	if !hasLaunched {
		return nil // Column already removed
	}

	// Recreate table without launched column (but with trending_score)
	queries := []string{
		`CREATE TABLE search_results_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			keyword TEXT NOT NULL,
			title TEXT NOT NULL,
			url TEXT,
			rating TEXT,
			review_count TEXT,
			price TEXT,
			relevance_score REAL,
			recent_reviews_30_days INTEGER DEFAULT 0,
			trending_score REAL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(keyword, title)
		)`,
		`INSERT INTO search_results_new 
			(id, keyword, title, url, rating, review_count, price, relevance_score, recent_reviews_30_days, trending_score, created_at, updated_at)
			SELECT id, keyword, title, url, rating, review_count, price, relevance_score, recent_reviews_30_days, 0, created_at, updated_at 
			FROM search_results`,
		`DROP TABLE search_results`,
		`ALTER TABLE search_results_new RENAME TO search_results`,
		`CREATE INDEX IF NOT EXISTS idx_keyword ON search_results(keyword)`,
		`CREATE INDEX IF NOT EXISTS idx_created_at ON search_results(created_at)`,
	}

	for _, query := range queries {
		_, err := c.executeQuery(query, nil)
		if err != nil {
			return fmt.Errorf("migration failed on query '%s': %w", query, err)
		}
	}

	return nil
}

// migrateAddTrendingScore adds the trending_score column if it doesn't exist
func (c *TursoClient) migrateAddTrendingScore() error {
	// Check if trending_score column exists
	result, err := c.executeQuery("PRAGMA table_info(search_results)", nil)
	if err != nil {
		return err
	}

	hasTrendingScore := false
	for _, row := range result.Results[0].Response.Result.Rows {
		if len(row) >= 2 {
			colName := getString(row[1])
			if colName == "trending_score" {
				hasTrendingScore = true
				break
			}
		}
	}

	if hasTrendingScore {
		return nil // Column already exists
	}

	// Add the column
	_, err = c.executeQuery("ALTER TABLE search_results ADD COLUMN trending_score REAL DEFAULT 0", nil)
	if err != nil {
		return fmt.Errorf("failed to add trending_score column: %w", err)
	}

	return nil
}

// SaveResults saves a batch of app results for a keyword
func (c *TursoClient) SaveResults(keyword string, apps []AppResult) error {
	for _, app := range apps {
		// Calculate trending score: % of recent reviews vs total
		trendingScore := calculateTrendingScore(app.RecentReviews30Days, app.ReviewCount)

		query := `INSERT INTO search_results 
			(keyword, title, url, rating, review_count, price, relevance_score, recent_reviews_30_days, trending_score)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(keyword, title) DO UPDATE SET
				url = excluded.url,
				rating = excluded.rating,
				review_count = excluded.review_count,
				price = excluded.price,
				relevance_score = excluded.relevance_score,
				recent_reviews_30_days = excluded.recent_reviews_30_days,
				trending_score = excluded.trending_score,
				updated_at = CURRENT_TIMESTAMP`

		args := []interface{}{
			keyword,
			app.Title,
			app.URL,
			app.Rating,
			app.ReviewCount,
			app.Price,
			app.RelevanceScore,
			app.RecentReviews30Days,
			trendingScore,
		}

		_, err := c.executeQuery(query, args)
		if err != nil {
			return fmt.Errorf("failed to save app %s: %w", app.Title, err)
		}
	}

	return nil
}

// calculateTrendingScore computes the trending score from recent and total reviews
func calculateTrendingScore(recentReviews int, totalReviewsStr string) float64 {
	if recentReviews == 0 {
		return 0
	}
	totalReviews := getIntFromString(totalReviewsStr)
	if totalReviews == 0 {
		return 0
	}
	return float64(recentReviews) / float64(totalReviews) * 100
}

// getIntFromString parses an integer from a string
func getIntFromString(s string) int {
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}

// GetResults retrieves all app results for a keyword
func (c *TursoClient) GetResults(keyword string) ([]AppResult, error) {
	query := `SELECT id, keyword, title, url, rating, review_count, price, 
		       relevance_score, recent_reviews_30_days, trending_score, created_at, updated_at
		FROM search_results
		WHERE keyword = ?
		ORDER BY relevance_score DESC`

	result, err := c.executeQuery(query, []interface{}{keyword})
	if err != nil {
		return nil, fmt.Errorf("failed to query results: %w", err)
	}

	var apps []AppResult
	for _, row := range result.Results[0].Response.Result.Rows {
		if len(row) < 12 {
			continue
		}
		app := AppResult{
			ID:                  getInt64(row[0]),
			Keyword:             getString(row[1]),
			Title:               getString(row[2]),
			URL:                 getString(row[3]),
			Rating:              getString(row[4]),
			ReviewCount:         getString(row[5]),
			Price:               getString(row[6]),
			RelevanceScore:      getFloat64(row[7]),
			RecentReviews30Days: getInt(row[8]),
			TrendingScore:       getFloat64(row[9]),
			CreatedAt:           getTime(row[10]),
			UpdatedAt:           getTime(row[11]),
		}
		apps = append(apps, app)
	}

	return apps, nil
}

// GetKeywords retrieves all unique keywords that have been searched
func (c *TursoClient) GetKeywords() ([]string, error) {
	query := `SELECT DISTINCT keyword FROM search_results ORDER BY keyword`

	result, err := c.executeQuery(query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query keywords: %w", err)
	}

	var keywords []string
	for _, row := range result.Results[0].Response.Result.Rows {
		if len(row) > 0 {
			keywords = append(keywords, getString(row[0]))
		}
	}

	return keywords, nil
}

// DeleteResults deletes all results for a keyword
func (c *TursoClient) DeleteResults(keyword string) error {
	query := `DELETE FROM search_results WHERE keyword = ?`
	_, err := c.executeQuery(query, []interface{}{keyword})
	return err
}

// GetStats returns statistics about the database
func (c *TursoClient) GetStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Total apps
	result, err := c.executeQuery(`SELECT COUNT(*) FROM search_results`, nil)
	if err != nil {
		return nil, err
	}
	if len(result.Results) > 0 && len(result.Results[0].Response.Result.Rows) > 0 {
		stats["total_apps"] = getInt(result.Results[0].Response.Result.Rows[0][0])
	}

	// Total keywords
	result, err = c.executeQuery(`SELECT COUNT(DISTINCT keyword) FROM search_results`, nil)
	if err != nil {
		return nil, err
	}
	if len(result.Results) > 0 && len(result.Results[0].Response.Result.Rows) > 0 {
		stats["total_keywords"] = getInt(result.Results[0].Response.Result.Rows[0][0])
	}

	// Latest scrape
	result, err = c.executeQuery(`SELECT MAX(updated_at) FROM search_results`, nil)
	if err != nil {
		return nil, err
	}
	if len(result.Results) > 0 && len(result.Results[0].Response.Result.Rows) > 0 {
		stats["latest_scrape"] = getTime(result.Results[0].Response.Result.Rows[0][0])
	}

	return stats, nil
}

// NormalizePrices updates all price values to only "Free", "Free trial", or "Paid"
func (c *TursoClient) NormalizePrices() (int, error) {
	result, err := c.executeQuery("SELECT id, price FROM search_results", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to query prices: %w", err)
	}

	updated := 0
	for _, row := range result.Results[0].Response.Result.Rows {
		if len(row) < 2 {
			continue
		}
		id := getInt64(row[0])
		rawPrice := getString(row[1])
		normalized := normalizePrice(rawPrice)
		if normalized != rawPrice {
			_, err := c.executeQuery("UPDATE search_results SET price = ? WHERE id = ?", []interface{}{normalized, id})
			if err != nil {
				return updated, fmt.Errorf("failed to update id %d: %w", id, err)
			}
			updated++
		}
	}

	return updated, nil
}

// normalizePrice maps any price string to "Free", "Free trial", or "Paid"
func normalizePrice(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return "Paid"
	}
	if strings.Contains(lower, "free trial") {
		return "Free trial"
	}
	if strings.Contains(lower, "free") {
		return "Free"
	}
	return "Paid"
}

// Helper functions to convert TypedValue to specific types
func getString(v TypedValue) string {
	if v.Type == "null" || v.Value == nil {
		return ""
	}
	switch val := v.Value.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}

func getInt(v TypedValue) int {
	if v.Type == "null" || v.Value == nil {
		return 0
	}
	switch val := v.Value.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	case string:
		n, _ := strconv.Atoi(val)
		return n
	default:
		return 0
	}
}

func getInt64(v TypedValue) int64 {
	if v.Type == "null" || v.Value == nil {
		return 0
	}
	switch val := v.Value.(type) {
	case float64:
		return int64(val)
	case int:
		return int64(val)
	case int64:
		return val
	case string:
		n, _ := strconv.ParseInt(val, 10, 64)
		return n
	default:
		return 0
	}
}

func getFloat64(v TypedValue) float64 {
	if v.Type == "null" || v.Value == nil {
		return 0
	}
	switch val := v.Value.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		n, _ := strconv.ParseFloat(val, 64)
		return n
	default:
		return 0
	}
}

func getTime(v TypedValue) time.Time {
	if v.Type == "null" || v.Value == nil {
		return time.Time{}
	}
	switch val := v.Value.(type) {
	case string:
		t, _ := time.Parse(time.RFC3339, val)
		return t
	default:
		return time.Time{}
	}
}
	
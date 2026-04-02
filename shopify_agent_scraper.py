"""
Shopify App Store Scraper using agent-browser
Uses agent-browser CLI for reliable extraction
Phase 2: Added relevance filtering
"""

import json
import re
import subprocess
import sys
from urllib.parse import quote
from typing import List, Dict, Tuple


class RelevanceScorer:
    """
    Phase 2: Relevance scoring system for filtering apps based on search query.
    Uses keyword matching and semantic similarity without ML libraries.
    """

    # Keyword expansion map - related terms that should match
    KEYWORD_MAP = {
        # Price related
        "price": ["price", "pricing", "cost", "fee", "rate"],
        "monitor": ["monitor", "monitoring", "track", "tracking", "watch", "watching"],
        "competitor": ["competitor", "competition", "competitive", "rival", "market"],
        "dynamic": ["dynamic", "automated", "auto", "smart", "intelligent"],
        "compare": ["compare", "comparison", "benchmark", "versus", "vs"],
        # Email related
        "email": ["email", "mail", "newsletter", "campaign"],
        "marketing": ["marketing", "promotion", "advertising", "outreach"],
        # Order related
        "order": ["order", "tracking", "shipment", "shipping", "delivery"],
        "track": ["track", "tracking", "trace", "status", "locate"],
        # Review related
        "review": ["review", "feedback", "rating", "testimonial"],
        # Inventory
        "inventory": ["inventory", "stock", "warehouse", "supply"],
        # SEO
        "seo": ["seo", "search", "ranking", "optimize", "optimization"],
    }

    # Weights for different parts of the app data
    WEIGHTS = {
        "title": 3.0,      # Title match is most important
        "description": 1.5,  # Description/subtitle match
        "category": 1.0,   # Category hint match
    }

    def __init__(self, query: str):
        """
        Initialize scorer with search query

        Args:
            query: Original search query
        """
        self.original_query = query.lower().strip()
        self.keywords = self._extract_keywords(query)
        print(f"🎯 Phase 2: Expanded keywords for '{query}': {self.keywords}")

    def _extract_keywords(self, query: str) -> List[str]:
        """
        Extract and expand keywords from query

        Args:
            query: Search query

        Returns:
            List of relevant keywords including synonyms
        """
        query_lower = query.lower().strip()
        words = re.findall(r'\b\w+\b', query_lower)

        keywords = set()
        for word in words:
            keywords.add(word)
            # Add related terms from keyword map
            for key, related in self.KEYWORD_MAP.items():
                if word == key or word in related:
                    keywords.update(related)

        return list(keywords)

    def _calculate_text_score(self, text: str) -> float:
        """
        Calculate relevance score for a text string

        Args:
            text: Text to score

        Returns:
            Score between 0 and 1
        """
        if not text:
            return 0.0

        text_lower = text.lower()
        text_words = set(re.findall(r'\b\w+\b', text_lower))

        if not text_words:
            return 0.0

        # Count matching keywords
        matches = sum(1 for keyword in self.keywords if keyword in text_lower)

        # Calculate score based on matches and text coverage
        if matches == 0:
            return 0.0

        # Score formula: matches / total unique keywords, capped at 1.0
        score = min(matches / len(self.keywords), 1.0)

        # Boost score if exact phrase appears
        if self.original_query in text_lower:
            score = min(score + 0.3, 1.0)

        return score

    def score_app(self, app: Dict) -> float:
        """
        Calculate overall relevance score for an app

        Args:
            app: App dictionary with title, price, etc.

        Returns:
            Relevance score between 0 and 100
        """
        # Combine title and price (description) for scoring
        title = app.get("title", "")
        description = app.get("price", "")  # The 'price' field contains the subtitle/description

        title_score = self._calculate_text_score(title) * self.WEIGHTS["title"]
        desc_score = self._calculate_text_score(description) * self.WEIGHTS["description"]

        # Normalize by total weight
        total_weight = self.WEIGHTS["title"] + self.WEIGHTS["description"]
        final_score = ((title_score + desc_score) / total_weight) * 100

        return round(final_score, 1)

    def filter_and_sort(
        self,
        apps: List[Dict],
        threshold: float = 30.0,
        min_results: int = 5
    ) -> Tuple[List[Dict], List[Dict]]:
        """
        Filter and sort apps by relevance

        Args:
            apps: List of app dictionaries
            threshold: Minimum relevance score to keep (0-100)
            min_results: Minimum number of results to return regardless of threshold

        Returns:
            Tuple of (filtered_sorted_apps, all_scored_apps)
        """
        # Score all apps
        scored_apps = []
        for app in apps:
            score = self.score_app(app)
            app["relevance_score"] = score
            scored_apps.append(app)

        # Sort by relevance score (descending)
        scored_apps.sort(key=lambda x: x["relevance_score"], reverse=True)

        # Filter: keep apps above threshold, but ensure at least min_results
        filtered = [app for app in scored_apps if app["relevance_score"] >= threshold]

        if len(filtered) < min_results and len(scored_apps) >= min_results:
            # If filtering removed too many, keep top min_results
            filtered = scored_apps[:min_results]

        return filtered, scored_apps


class ShopifyAgentScraper:
    """Scraper using agent-browser CLI with Phase 2 relevance filtering"""

    BASE_URL = "https://apps.shopify.com"

    def __init__(self, wait_seconds=5, relevance_threshold=30.0):
        """
        Initialize scraper

        Args:
            wait_seconds: Seconds to wait for page load
            relevance_threshold: Minimum relevance score (0-100) to keep an app
        """
        self.wait_seconds = wait_seconds
        self.relevance_threshold = relevance_threshold

    def run_command(self, cmd: str) -> str:
        """Run agent-browser command and return output"""
        result = subprocess.run(
            cmd, shell=True, capture_output=True, text=True, timeout=60
        )
        return result.stdout + result.stderr

    def search_and_extract(self, keyword: str):
        """
        Search for apps and extract using agent-browser

        Args:
            keyword: Search term

        Returns:
            List of app dictionaries
        """
        # URL encode the keyword
        encoded = quote(keyword)
        url = f"{self.BASE_URL}/search?q={encoded}"

        print(f"🔍 Searching for: '{keyword}'")
        print(f"🌐 URL: {url}")

        # Build command chain: open -> wait -> wait -> snapshot
        cmd = (
            f'agent-browser open "{url}" && '
            f"agent-browser wait --load networkidle && "
            f"agent-browser wait {self.wait_seconds * 1000} && "
            f"agent-browser snapshot -i"
        )

        print(f"⏳ Running agent-browser (this may take {self.wait_seconds + 5}s)...")
        output = self.run_command(cmd)

        try:
            # Parse the snapshot output
            apps, all_links = self.parse_snapshot(output)

            # Get actual URLs for each app using the link refs
            apps = self._get_app_urls(apps, all_links)
        finally:
            # Always close browser, even if an error occurs
            print("🛑 Closing agent-browser...")
            self.run_command("agent-browser close")

        return apps

    def _get_app_urls(self, apps, all_links):
        """Fetch actual URLs from link elements using agent-browser

        Args:
            apps: List of app dictionaries
            all_links: List of (text, ref) tuples from all links in the snapshot
        """
        # Build a lookup map from link text to ref for all links
        link_text_to_ref = {text.strip(): ref for text, ref in all_links}

        for app in apps:
            # First try to find link by matching title
            link_ref = None

            # Try exact match first
            if app["title"].strip() in link_text_to_ref:
                link_ref = link_text_to_ref[app["title"].strip()]
            else:
                # Try case-insensitive partial match
                title_lower = app["title"].lower().strip()
                for link_text, ref in all_links:
                    # Check if link text is contained in title or vice versa
                    if (
                        link_text.lower() in title_lower
                        or title_lower in link_text.lower()
                    ):
                        # Make sure it's a substantial match (at least 5 chars)
                        if len(link_text) >= 5:
                            link_ref = ref
                            break

            if link_ref:
                # Get the href attribute from the link
                cmd = f"agent-browser get attr @{link_ref} href"
                result = self.run_command(cmd).strip()

                # Extract URL from output (format: "https://apps.shopify.com/...")
                url_match = re.search(r"https://apps\.shopify\.com/[^\s]+", result)
                if url_match:
                    full_url = url_match.group(0)
                    # Clean up tracking parameters (keep only the base URL)
                    clean_url = full_url.split("?")[0]
                    app["url"] = clean_url
                    app["link_ref"] = link_ref
                    continue

            # If we couldn't get a proper URL, mark it as None
            # Don't construct fake URLs - they're useless
            app["url"] = None

        return apps

    def parse_snapshot(self, output: str):
        """
        Parse agent-browser snapshot output to extract apps

        Args:
            output: Raw snapshot text

        Returns:
            Tuple of (list of app dictionaries, list of all link tuples)
        """
        apps = []
        lines = output.split("\n")

        # Pattern to match app buttons and links
        # Example: - button "PriceMole | Price Monitoring 4.5 out of 5 stars 34 total reviews • Free trial available..." [ref=e9]
        # Example: - link "PriceMole | Price Monitoring" [ref=e10]

        app_buttons = []
        all_links = []  # Store ALL links, not just filtered ones

        for line in lines:
            line = line.strip()

            # Match button with app info
            button_match = re.match(r'- button "(.+?)"\s+\[ref=(\w+)\]', line)
            if button_match:
                text = button_match.group(1)
                ref = button_match.group(2)

                # Filter: must contain app-like info (rating or reviews)
                if "out of 5 stars" in text or "total reviews" in text:
                    app_buttons.append((text, ref))
                continue

            # Match link with app name - store ALL links
            link_match = re.match(r'- link "(.+?)"\s+\[ref=(\w+)\]', line)
            if link_match:
                text = link_match.group(1)
                ref = link_match.group(2)

                # Skip very short text (likely navigation icons)
                if len(text.strip()) < 3:
                    continue

                all_links.append((text, ref))

        print(f"✅ Found {len(app_buttons)} app buttons")
        print(f"✅ Found {len(all_links)} potential app links")

        # Build ref->text maps
        link_ref_to_text = {ref: text for text, ref in all_links}

        # Extract app info from buttons
        seen_titles = set()

        for button_text, button_ref in app_buttons:
            app_info = self._parse_app_button(button_text)

            # Find the matching link by looking for consecutive refs
            # Button refs are odd (e9, e11, e13...), link refs are even (e10, e12, e14...)
            # So if button is e9, the link should be e10
            button_num = int(re.search(r"\d+", button_ref).group())
            expected_link_ref = f"e{button_num + 1}"

            if expected_link_ref in link_ref_to_text:
                link_text = link_ref_to_text[expected_link_ref]
                app_info["link_text"] = link_text
                app_info["link_ref"] = expected_link_ref
            else:
                # Fallback: try to find any link that contains the title
                title_lower = app_info["title"].lower()
                for ref, text in link_ref_to_text.items():
                    if text.lower() in title_lower or title_lower in text.lower():
                        app_info["link_text"] = text
                        app_info["link_ref"] = ref
                        break

            # Avoid duplicates
            if app_info["title"] and app_info["title"] not in seen_titles:
                seen_titles.add(app_info["title"])
                apps.append(app_info)

        return apps, all_links

    def _parse_app_button(self, text: str):
        """Parse app button text to extract structured data"""

        # Patterns
        # "PriceMole | Price Monitoring 4.5 out of 5 stars 34 total reviews • Free trial available ..."

        # Extract title (everything before the rating)
        title_match = re.match(r"^(.+?)\s+(\d+\.?\d*)\s+out\s+of\s+5\s+stars", text)
        title = (
            title_match.group(1).strip() if title_match else text.split("•")[0].strip()
        )

        # Extract rating
        rating_match = re.search(r"(\d+\.?\d*)\s+out\s+of\s+5\s+stars", text)
        rating = rating_match.group(1) if rating_match else None

        # Extract review count
        reviews_match = re.search(r"(\d+)\s+total\s+reviews", text)
        reviews = reviews_match.group(1) if reviews_match else None

        # Extract price info (after the bullet)
        price = None
        if "•" in text:
            parts = text.split("•")
            if len(parts) > 1:
                price = parts[1].strip().split(".")[0]  # Get first sentence

        # URL will be fetched later using the link ref
        # For now, leave it empty
        return {
            "title": title,
            "url": None,  # Will be fetched from actual link element
            "rating": rating,
            "review_count": reviews,
            "price": price,
            "link_ref": None,
        }

    def scrape(self, keyword: str, save_to_file: str = None, filter_results: bool = True):
        """
        Main scraping method with Phase 2 relevance filtering

        Args:
            keyword: Search term
            save_to_file: Optional JSON file to save results
            filter_results: Whether to apply relevance filtering (Phase 2)

        Returns:
            List of app dictionaries
        """
        apps = self.search_and_extract(keyword)

        print(f"\n📊 Found {len(apps)} raw apps for keyword: '{keyword}'")

        # Phase 2: Apply relevance filtering
        if filter_results and apps:
            scorer = RelevanceScorer(keyword)
            filtered_apps, all_scored = scorer.filter_and_sort(
                apps,
                threshold=self.relevance_threshold,
                min_results=5
            )

            removed_count = len(apps) - len(filtered_apps)
            print(f"🎯 Phase 2: Filtered out {removed_count} low-relevance apps")
            print(f"✅ Keeping {len(filtered_apps)} relevant apps\n")

            apps = filtered_apps

            # Save full scored results for reference
            if save_to_file:
                debug_file = save_to_file.replace(".json", "_all_scored.json")
                with open(debug_file, "w", encoding="utf-8") as f:
                    json.dump(all_scored, f, indent=2, ensure_ascii=False)
        else:
            print()

        # Display results
        for i, app in enumerate(apps, 1):
            print(f"{i}. {app['title']}", end="")
            if "relevance_score" in app:
                print(f" (Relevance: {app['relevance_score']}%)")
            else:
                print()
            print(f"   URL: {app['url']}")
            if app.get("rating"):
                print(f"   Rating: {app['rating']} stars")
            if app.get("review_count"):
                print(f"   Reviews: {app['review_count']}")
            if app.get("price"):
                print(f"   Price: {app['price']}")
            print()

        # Save to file if requested
        if save_to_file:
            with open(save_to_file, "w", encoding="utf-8") as f:
                json.dump(apps, f, indent=2, ensure_ascii=False)
            print(f"💾 Results saved to: {save_to_file}")

        return apps


def main():
    """CLI entry point with Phase 2 relevance filtering"""
    # Get keyword from args or prompt
    if len(sys.argv) > 1:
        keyword = " ".join(sys.argv[1:])
    else:
        keyword = input(
            "Enter search keyword (e.g., 'track order', 'email marketing'): "
        ).strip()

    if not keyword:
        print("❌ Please provide a search keyword")
        return

    # Create scraper with Phase 2 filtering enabled
    scraper = ShopifyAgentScraper(wait_seconds=5, relevance_threshold=30.0)

    output_file = f"shopify_apps_{keyword.replace(' ', '_')}.json"
    apps = scraper.scrape(keyword, save_to_file=output_file, filter_results=True)

    print(f"\n✅ Phase 2 complete! Found {len(apps)} relevant apps.")


if __name__ == "__main__":
    main()

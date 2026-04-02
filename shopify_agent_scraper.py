"""
Shopify App Store Scraper using agent-browser
Uses agent-browser CLI for reliable extraction
"""

import json
import re
import subprocess
import sys
from urllib.parse import quote


class ShopifyAgentScraper:
    """Scraper using agent-browser CLI"""

    BASE_URL = "https://apps.shopify.com"

    def __init__(self, wait_seconds=5):
        self.wait_seconds = wait_seconds

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

        # Parse the snapshot output
        apps, all_links = self.parse_snapshot(output)

        # Get actual URLs for each app using the link refs
        apps = self._get_app_urls(apps, all_links)

        # Close browser
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

    def scrape(self, keyword: str, save_to_file: str = None):
        """
        Main scraping method

        Args:
            keyword: Search term
            save_to_file: Optional JSON file to save results

        Returns:
            List of app dictionaries
        """
        apps = self.search_and_extract(keyword)

        print(f"\n📊 Found {len(apps)} apps for keyword: '{keyword}'\n")

        # Display results
        for i, app in enumerate(apps, 1):
            print(f"{i}. {app['title']}")
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
    """CLI entry point"""
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

    # Create scraper and run
    scraper = ShopifyAgentScraper(wait_seconds=5)

    output_file = f"shopify_apps_{keyword.replace(' ', '_')}.json"
    apps = scraper.scrape(keyword, save_to_file=output_file)

    print(f"\n✅ Scraping complete! Found {len(apps)} apps.")


if __name__ == "__main__":
    main()

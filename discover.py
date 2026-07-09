#!/usr/bin/env python3
"""
Sitemap-based Shopify app discovery scraper.

Finds apps from the official EN sitemap that are NOT already in the DB,
then detail-scrapes them one-by-one (sequential, resume-safe).

Usage:
  python3 discover.py status
  python3 discover.py init              # create apps table + backfill from search_results
  python3 discover.py sync-sitemap      # fetch sitemap, insert unknown slugs (no detail)
  python3 discover.py run --limit 2000  # detail-scrape up to N unscraped apps
  python3 discover.py dry-run --limit 2000

Status / progress is always in the DB — crash-safe resume:
  python3 discover.py status
  python3 discover.py status --watch    # refresh every 30s
"""

from __future__ import annotations

import argparse
import json
import os
import random
import re
import sys
import time
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from datetime import datetime, timedelta, timezone
from typing import Any, Optional
from urllib.parse import urlparse

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

SITEMAP_URL = "https://apps.shopify.com/sitemap_apps_en.xml"
SITEMAP_INDEX_URL = "https://apps.shopify.com/sitemap.xml"
USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

# Sequential pacing — keep VPS load low
DEFAULT_DELAY = 20.0          # seconds between successful scrapes
MIN_DELAY = 12.0
ERROR_BACKOFF_BASE = 30.0    # first error wait
ERROR_BACKOFF_MAX = 300.0    # cap
MAX_RETRIES = 4
RECENT_REVIEWS_CAP = 20

DB_URL = "https://shopify-spy-v2-nithish.aws-ap-south-1.turso.io/v2/pipeline"
ENV_PATH = os.path.expanduser("~/Work/shopify-spy/.env")
LOG_DIR = os.path.expanduser("~/Work/shopify-spy")

# Synthetic keyword so rows also land in search_results for existing tooling
SITEMAP_KEYWORD = "__sitemap__"


# ---------------------------------------------------------------------------
# Turso helpers
# ---------------------------------------------------------------------------

def get_token() -> str:
    with open(ENV_PATH) as f:
        for line in f:
            if "TURSO_AUTH_TOKEN" in line and "=" in line:
                return line.split("=", 1)[1].strip().strip('"').strip("'")
    raise RuntimeError("TURSO_AUTH_TOKEN not found in .env")


TOKEN = get_token()


def db_run(sql: str, args: Optional[list] = None) -> list[dict]:
    """Execute SQL against Turso. Args are positional ? placeholders."""
    stmt: dict[str, Any] = {"sql": sql}
    if args:
        turso_args = []
        for a in args:
            if a is None:
                turso_args.append({"type": "null"})
            elif isinstance(a, bool):
                turso_args.append({"type": "integer", "value": "1" if a else "0"})
            elif isinstance(a, int):
                turso_args.append({"type": "integer", "value": str(a)})
            elif isinstance(a, float):
                turso_args.append({"type": "float", "value": a})
            else:
                turso_args.append({"type": "text", "value": str(a)})
        stmt["args"] = turso_args

    body = json.dumps({"requests": [{"type": "execute", "stmt": stmt}]}).encode()
    req = urllib.request.Request(
        DB_URL,
        data=body,
        headers={
            "Authorization": f"Bearer {TOKEN}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            result = json.loads(resp.read())
    except urllib.error.HTTPError as e:
        err_body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"Turso HTTP {e.code}: {err_body[:300]}") from e

    out: list[dict] = []
    for r in result.get("results", []):
        if "error" in r:
            raise RuntimeError(f"Turso error: {r['error']}")
        if "response" not in r:
            continue
        resp_obj = r["response"]
        if "error" in resp_obj and resp_obj["error"]:
            raise RuntimeError(f"Turso stmt error: {resp_obj['error']}")
        res = resp_obj.get("result", {})
        cols = [c["name"] for c in res.get("cols", [])]
        for row in res.get("rows", []):
            vals = []
            for cell in row:
                if isinstance(cell, dict):
                    vals.append(cell.get("value"))
                else:
                    vals.append(cell)
            out.append(dict(zip(cols, vals)))
    return out


def db_exec(sql: str, args: Optional[list] = None) -> None:
    db_run(sql, args)


def _arg_to_turso(a: Any) -> dict:
    if a is None:
        return {"type": "null"}
    if isinstance(a, bool):
        return {"type": "integer", "value": "1" if a else "0"}
    if isinstance(a, int):
        return {"type": "integer", "value": str(a)}
    if isinstance(a, float):
        return {"type": "float", "value": a}
    return {"type": "text", "value": str(a)}


def db_batch(statements: list[tuple[str, Optional[list]]]) -> None:
    """Execute many statements in one Turso pipeline (chunked)."""
    requests = []
    for sql, args in statements:
        stmt: dict[str, Any] = {"sql": sql}
        if args:
            stmt["args"] = [_arg_to_turso(a) for a in args]
        requests.append({"type": "execute", "stmt": stmt})
    chunk_size = 100
    for i in range(0, len(requests), chunk_size):
        chunk = requests[i : i + chunk_size]
        body = json.dumps({"requests": chunk}).encode()
        req = urllib.request.Request(
            DB_URL,
            data=body,
            headers={
                "Authorization": f"Bearer {TOKEN}",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read())
        for r in result.get("results", []):
            if "error" in r:
                msg = str(r["error"])
                if "UNIQUE" not in msg:
                    raise RuntimeError(f"Turso batch error: {r['error']}")
            resp_obj = r.get("response") or {}
            err = resp_obj.get("error")
            if err:
                msg = str(err)
                if "UNIQUE" not in msg:
                    raise RuntimeError(f"Turso batch stmt error: {err}")


# ---------------------------------------------------------------------------
# Schema
# ---------------------------------------------------------------------------

CREATE_APPS_SQL = """
CREATE TABLE IF NOT EXISTS apps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  slug TEXT NOT NULL UNIQUE,
  url TEXT NOT NULL UNIQUE,
  title TEXT,
  rating TEXT,
  review_count TEXT,
  price TEXT,
  recent_reviews_30_days INTEGER DEFAULT 0,
  trending_score REAL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'sitemap',
  lastmod TEXT,
  scrape_status TEXT NOT NULL DEFAULT 'pending',
  scrape_attempts INTEGER DEFAULT 0,
  last_error TEXT,
  first_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  last_scraped_at DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
)
"""

# scrape_status: pending | done | failed | skipped


def init_schema() -> None:
    print("📦 Creating apps table (if missing)...")
    db_exec(CREATE_APPS_SQL)
    db_exec("CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(scrape_status)")
    db_exec("CREATE INDEX IF NOT EXISTS idx_apps_lastmod ON apps(lastmod)")
    db_exec("CREATE INDEX IF NOT EXISTS idx_apps_scraped ON apps(last_scraped_at)")
    print("✅ apps table ready")


def slug_from_url(url: str) -> Optional[str]:
    if not url:
        return None
    path = urlparse(url).path.strip("/")
    if not path or "/" in path:
        # only accept top-level app slugs, not /categories/x or /foo/reviews
        parts = [p for p in path.split("/") if p]
        if len(parts) != 1:
            return None
        path = parts[0]
    # filter non-app paths
    if path in {
        "search", "categories", "stories", "login", "partners",
        "collections", "browse", "sitemap", "internal", "services",
    }:
        return None
    if path.startswith("sitemap"):
        return None
    return path


def backfill_from_search_results() -> int:
    """Import known apps from search_results so we don't re-scrape them."""
    print("🔄 Backfilling known apps from search_results...")
    rows = db_run(
        """
        SELECT title, url, rating, review_count, price,
               recent_reviews_30_days, trending_score, updated_at
        FROM search_results
        WHERE url IS NOT NULL AND url != ''
        """
    )
    by_slug: dict[str, dict] = {}
    skipped = 0
    for r in rows:
        url = (r.get("url") or "").split("?")[0].rstrip("/")
        slug = slug_from_url(url)
        if not slug:
            skipped += 1
            continue
        if slug not in by_slug:
            by_slug[slug] = r

    sql = """
        INSERT INTO apps (
          slug, url, title, rating, review_count, price,
          recent_reviews_30_days, trending_score, source,
          scrape_status, last_scraped_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'keyword', 'done', ?, CURRENT_TIMESTAMP)
        ON CONFLICT(slug) DO UPDATE SET
          title = COALESCE(excluded.title, apps.title),
          rating = COALESCE(excluded.rating, apps.rating),
          review_count = COALESCE(excluded.review_count, apps.review_count),
          price = COALESCE(excluded.price, apps.price),
          recent_reviews_30_days = COALESCE(excluded.recent_reviews_30_days, apps.recent_reviews_30_days),
          trending_score = COALESCE(excluded.trending_score, apps.trending_score),
          updated_at = CURRENT_TIMESTAMP
    """
    stmts: list[tuple[str, Optional[list]]] = []
    for slug, r in by_slug.items():
        stmts.append(
            (
                sql,
                [
                    slug,
                    f"https://apps.shopify.com/{slug}",
                    r.get("title"),
                    r.get("rating"),
                    r.get("review_count"),
                    r.get("price"),
                    int(r.get("recent_reviews_30_days") or 0),
                    float(r.get("trending_score") or 0),
                    r.get("updated_at") or datetime.now(timezone.utc).isoformat(),
                ],
            )
        )
    if stmts:
        print(f"  writing {len(stmts)} apps in batches...")
        db_batch(stmts)
    print(f"✅ Backfill done: {len(stmts)} upserted, {skipped} skipped bad urls")
    return len(stmts)


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------

def http_get(url: str, timeout: int = 45) -> str:
    req = urllib.request.Request(
        url,
        headers={
            "User-Agent": USER_AGENT,
            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
            "Accept-Language": "en-US,en;q=0.9",
        },
        method="GET",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status = getattr(resp, "status", 200)
            if status == 429:
                raise RateLimitError("HTTP 429")
            data = resp.read()
            # try decode
            for enc in ("utf-8", "latin-1"):
                try:
                    return data.decode(enc)
                except UnicodeDecodeError:
                    continue
            return data.decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        if e.code == 429:
            raise RateLimitError("HTTP 429") from e
        if e.code in (503, 502, 504):
            raise TransientError(f"HTTP {e.code}") from e
        if e.code == 404:
            raise NotFoundError(f"HTTP 404 for {url}") from e
        raise TransientError(f"HTTP {e.code}") from e
    except urllib.error.URLError as e:
        raise TransientError(f"URL error: {e}") from e


class RateLimitError(Exception):
    pass


class TransientError(Exception):
    pass


class NotFoundError(Exception):
    pass


# ---------------------------------------------------------------------------
# Sitemap
# ---------------------------------------------------------------------------

def fetch_sitemap_apps() -> list[dict]:
    """Return list of {slug, url, lastmod} from EN apps sitemap."""
    print(f"🌐 Fetching sitemap: {SITEMAP_URL}")
    xml_text = http_get(SITEMAP_URL, timeout=120)
    # strip namespaces for easy parse
    xml_text = re.sub(r'\sxmlns="[^"]+"', "", xml_text, count=1)
    root = ET.fromstring(xml_text)
    apps = []
    for url_el in root.findall("url"):
        loc = (url_el.findtext("loc") or "").strip()
        lastmod = (url_el.findtext("lastmod") or "").strip() or None
        if not loc.startswith("https://apps.shopify.com/"):
            continue
        slug = slug_from_url(loc)
        if not slug:
            continue
        apps.append(
            {
                "slug": slug,
                "url": f"https://apps.shopify.com/{slug}",
                "lastmod": lastmod,
            }
        )
    print(f"✅ Sitemap parsed: {len(apps)} app URLs")
    return apps


def sync_sitemap() -> dict:
    """Insert unknown sitemap apps as pending. Never overwrites done rows' scrape status."""
    apps = fetch_sitemap_apps()
    print("  loading known slugs from DB...")
    known_rows = db_run("SELECT slug FROM apps")
    known_set = {r["slug"] for r in known_rows}
    print(f"  known in DB: {len(known_set)}")

    new_apps = [a for a in apps if a["slug"] not in known_set]
    insert_sql = """
        INSERT INTO apps (slug, url, lastmod, source, scrape_status)
        VALUES (?, ?, ?, 'sitemap', 'pending')
        ON CONFLICT(slug) DO NOTHING
    """
    stmts = [(insert_sql, [a["slug"], a["url"], a["lastmod"]]) for a in new_apps]
    if stmts:
        print(f"  inserting {len(stmts)} new pending apps...")
        db_batch(stmts)
    print(
        f"✅ Sitemap sync: {len(new_apps)} new pending, "
        f"{len(known_set)} already known, sitemap_total={len(apps)}"
    )
    return {
        "new": len(new_apps),
        "known": len(known_set),
        "sitemap_total": len(apps),
    }


# ---------------------------------------------------------------------------
# Detail parsing (agent-browser — same stack as shopify-spy Go scraper)
# ---------------------------------------------------------------------------

BROWSER_ARGS = '--engine chrome --args "--no-sandbox"'


def run_browser(cmd: str, timeout: int = 90) -> str:
    """Run an agent-browser command; return combined stdout+stderr."""
    import subprocess
    full = f"agent-browser {BROWSER_ARGS} {cmd}" if not cmd.startswith("agent-browser") else cmd
    # Always prefix engine args when using subcommands
    if cmd.startswith("agent-browser"):
        full = cmd
    else:
        full = f"agent-browser {BROWSER_ARGS} {cmd}"
    try:
        p = subprocess.run(
            full,
            shell=True,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        out = (p.stdout or "") + (p.stderr or "")
        if p.returncode != 0 and not out.strip():
            raise TransientError(f"agent-browser exit {p.returncode}")
        return out
    except subprocess.TimeoutExpired as e:
        raise TransientError(f"agent-browser timeout: {e}") from e


def browser_close_all() -> None:
    try:
        run_browser("close --all", timeout=30)
    except Exception:
        pass


def browser_get_page_text(url: str) -> str:
    """Open URL with agent-browser and return main text (JS-rendered)."""
    session = f"discover_{int(time.time() * 1000) % 1000000}"
    try:
        cmd = (
            f'agent-browser {BROWSER_ARGS} --session {session} open "{url}" && '
            f"agent-browser {BROWSER_ARGS} --session {session} wait --load networkidle && "
            f"agent-browser {BROWSER_ARGS} --session {session} wait 2500 && "
            f'agent-browser {BROWSER_ARGS} --session {session} get text "main"'
        )
        import subprocess
        p = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=120)
        out = (p.stdout or "") + (p.stderr or "")
        if p.returncode != 0 and len(out) < 50:
            raise TransientError(f"browser open failed rc={p.returncode}: {out[:200]}")
        low = out.lower()
        if "429" in low or "rate limit" in low or "too many requests" in low:
            raise RateLimitError("rate limited by shopify/app store")
        if "captcha" in low or "access denied" in low:
            raise TransientError("blocked/captcha")
        # Strip agent-browser chrome lines (✓ ... / session noise)
        cleaned = []
        for ln in out.splitlines():
            s = ln.strip()
            if not s:
                cleaned.append("")
                continue
            if s.startswith("✓") or s.startswith("✗"):
                continue
            if s.startswith("http") and "apps.shopify.com" in s and " " not in s:
                continue
            cleaned.append(ln)
        return "\n".join(cleaned)
    finally:
        try:
            import subprocess
            subprocess.run(
                f"agent-browser {BROWSER_ARGS} --session {session} close",
                shell=True,
                capture_output=True,
                timeout=30,
            )
        except Exception:
            pass


def parse_listing_text(text: str) -> dict:
    """Parse agent-browser main text from an app listing page."""
    lines = [ln.strip() for ln in text.splitlines() if ln.strip()]
    title = None
    rating = None
    review_count = None
    price_bits = []

    # Title: first substantial line that isn't chrome UI
    skip_prefixes = ("shopify app store", "browse apps", "log in", "install", "pricing", "rating", "developer")
    for ln in lines[:40]:
        low = ln.lower()
        if any(low.startswith(s) for s in skip_prefixes):
            continue
        if "shopify app store" in low:
            continue
        if low in {"free plan available", "free trial available", "free to install", "featured images gallery"}:
            continue
        if re.fullmatch(r"\d+(?:\.\d+)?", ln):
            continue
        if re.fullmatch(r"\(\d+\)", ln):
            continue
        if re.fullmatch(r"\+\s*\d+\s*more", low):
            continue
        if len(ln) >= 3:
            title = ln
            break

    # Rating: line "Rating" followed by number, or "4.7 out of 5"
    for i, ln in enumerate(lines):
        if ln.lower() == "rating" and i + 1 < len(lines):
            m = re.match(r"^(\d(?:\.\d)?)$", lines[i + 1])
            if m:
                rating = m.group(1)
        m = re.search(r"(\d(?:\.\d)?)\s*out of 5", ln, re.I)
        if m:
            rating = m.group(1)
        m = re.fullmatch(r"\((\d+)\)", ln)
        if m and review_count is None:
            # Only first rating-adjacent count (listing hero), never related-apps later
            if i > 0 and (
                lines[i - 1].lower() == "rating"
                or re.fullmatch(r"\d(?:\.\d)?", lines[i - 1])
            ):
                review_count = m.group(1)
        m = re.search(r"(\d+)\s*total reviews", ln, re.I)
        if m and review_count is None:
            review_count = m.group(1)
        m = re.search(r"Reviews\s*\((\d+)\)", ln, re.I)
        if m and review_count is None:
            review_count = m.group(1)

    blob = "\n".join(lines[:80]).lower()
    if "free plan available" in blob:
        price_bits.append("Free plan available")
    if "free trial available" in blob:
        price_bits.append("Free trial available")
    if "free to install" in blob:
        price_bits.append("Free to install")
    m = re.search(r"\$(\d+(?:\.\d+)?)\s*/\s*month", "\n".join(lines[:120]), re.I)
    if m:
        price_bits.append(f"${m.group(1)}/month")
    if not price_bits:
        if re.search(r"\bfree\b", blob):
            price_bits.append("Free")
        else:
            price_bits.append("Paid")

    # Fallback title from page header line in agent-browser output
    if not title:
        m = re.search(r"✓\s+(.+?)\s+-\s+.+?\|\s+Shopify App Store", text)
        if m:
            title = m.group(1).strip()

    return {
        "title": title,
        "rating": rating,
        "review_count": review_count,
        "price": " · ".join(price_bits[:3]) if price_bits else None,
    }


def count_recent_reviews_browser(app_url: str) -> int:
    """Count reviews in last 30 days via agent-browser (up to 2 pages, cap 20)."""
    cutoff = datetime.now() - timedelta(days=30)
    date_re = re.compile(
        r"((?:January|February|March|April|May|June|July|August|September|"
        r"October|November|December)\s+\d{1,2},\s+\d{4})"
    )
    total = 0
    for page in (1, 2):
        url = f"{app_url}/reviews?sort_by=newest&page={page}"
        try:
            text = browser_get_page_text(url)
        except NotFoundError:
            break
        except TransientError:
            if page == 1:
                raise
            break
        dates = date_re.findall(text)
        if not dates:
            break
        page_recent = 0
        found_old = False
        for d in dates:
            try:
                dt = datetime.strptime(d, "%B %d, %Y")
            except ValueError:
                continue
            if dt >= cutoff:
                page_recent += 1
            else:
                found_old = True
        total += page_recent
        if found_old or page_recent < 8:
            break
        time.sleep(2)
    return min(total, RECENT_REVIEWS_CAP)


def trending_score(recent: int, total_str: Optional[str]) -> float:
    try:
        total = int(str(total_str or "0").replace(",", ""))
    except ValueError:
        total = 0
    if total <= 0 or recent <= 0:
        return 0.0
    return round(recent / total * 100.0, 4)


# ---------------------------------------------------------------------------
# Scrape one app
# ---------------------------------------------------------------------------

def scrape_one(slug: str, url: str) -> dict:
    """Detail-scrape one app with agent-browser (listing + recent reviews)."""
    browser_close_all()
    listing_text = browser_get_page_text(url)
    # 404 detection
    low = listing_text.lower()
    if "page not found" in low or "couldn't find" in low or "404" in low[:500]:
        raise NotFoundError(f"app not found: {slug}")
    meta = parse_listing_text(listing_text)
    if not meta.get("title"):
        raise TransientError(f"no title parsed for {slug}")
    time.sleep(2.0 + random.random())
    recent = count_recent_reviews_browser(url)
    trend = trending_score(recent, meta.get("review_count"))
    browser_close_all()
    return {
        **meta,
        "recent_reviews_30_days": recent,
        "trending_score": trend,
    }


def mark_done(slug: str, data: dict) -> None:
    db_exec(
        """
        UPDATE apps SET
          title = ?,
          rating = ?,
          review_count = ?,
          price = ?,
          recent_reviews_30_days = ?,
          trending_score = ?,
          scrape_status = 'done',
          last_error = NULL,
          last_scraped_at = CURRENT_TIMESTAMP,
          updated_at = CURRENT_TIMESTAMP,
          scrape_attempts = scrape_attempts + 1
        WHERE slug = ?
        """,
        [
            data.get("title"),
            data.get("rating"),
            data.get("review_count"),
            data.get("price"),
            int(data.get("recent_reviews_30_days") or 0),
            float(data.get("trending_score") or 0),
            slug,
        ],
    )
    # Mirror into search_results for existing tooling
    title = data.get("title") or slug
    db_exec(
        """
        INSERT INTO search_results
          (keyword, title, url, rating, review_count, price, relevance_score,
           recent_reviews_30_days, trending_score)
        VALUES (?, ?, ?, ?, ?, ?, 100.0, ?, ?)
        ON CONFLICT(keyword, title) DO UPDATE SET
          url = excluded.url,
          rating = excluded.rating,
          review_count = excluded.review_count,
          price = excluded.price,
          recent_reviews_30_days = excluded.recent_reviews_30_days,
          trending_score = excluded.trending_score,
          updated_at = CURRENT_TIMESTAMP
        """,
        [
            SITEMAP_KEYWORD,
            title,
            f"https://apps.shopify.com/{slug}",
            data.get("rating"),
            data.get("review_count"),
            data.get("price"),
            int(data.get("recent_reviews_30_days") or 0),
            float(data.get("trending_score") or 0),
        ],
    )


def mark_failed(slug: str, err: str, permanent: bool = False) -> None:
    status = "failed" if permanent else "pending"
    db_exec(
        """
        UPDATE apps SET
          scrape_status = ?,
          last_error = ?,
          scrape_attempts = scrape_attempts + 1,
          updated_at = CURRENT_TIMESTAMP
        WHERE slug = ?
        """,
        [status, err[:500], slug],
    )


# ---------------------------------------------------------------------------
# Status
# ---------------------------------------------------------------------------

def status(json_mode: bool = False) -> dict:
    def count(sql: str) -> int:
        rows = db_run(sql)
        if not rows:
            return 0
        v = list(rows[0].values())[0]
        try:
            return int(v)
        except (TypeError, ValueError):
            return 0

    stats = {
        "total_apps": count("SELECT COUNT(*) AS c FROM apps"),
        "pending": count("SELECT COUNT(*) AS c FROM apps WHERE scrape_status = 'pending'"),
        "done": count("SELECT COUNT(*) AS c FROM apps WHERE scrape_status = 'done'"),
        "failed": count("SELECT COUNT(*) AS c FROM apps WHERE scrape_status = 'failed'"),
        "sitemap_source": count("SELECT COUNT(*) AS c FROM apps WHERE source = 'sitemap'"),
        "keyword_source": count("SELECT COUNT(*) AS c FROM apps WHERE source = 'keyword'"),
        "done_sitemap": count(
            "SELECT COUNT(*) AS c FROM apps WHERE source = 'sitemap' AND scrape_status = 'done'"
        ),
        "search_results_rows": count("SELECT COUNT(*) AS c FROM search_results"),
        "search_results_sitemap": count(
            f"SELECT COUNT(*) AS c FROM search_results WHERE keyword = '{SITEMAP_KEYWORD}'"
        ),
    }

    # recent activity
    recent = db_run(
        """
        SELECT slug, title, scrape_status, last_scraped_at, last_error, scrape_attempts
        FROM apps
        WHERE last_scraped_at IS NOT NULL OR last_error IS NOT NULL
        ORDER BY COALESCE(last_scraped_at, updated_at) DESC
        LIMIT 8
        """
    )
    stats["recent"] = recent

    # progress log file if present
    log_path = os.path.join(LOG_DIR, "discover_run.log")
    if os.path.exists(log_path):
        stats["log_file"] = log_path
        stats["log_mtime"] = datetime.fromtimestamp(os.path.getmtime(log_path)).isoformat()
        try:
            with open(log_path) as f:
                lines = f.readlines()
            stats["log_tail"] = [ln.rstrip() for ln in lines[-12:]]
        except OSError:
            stats["log_tail"] = []

    if json_mode:
        print(json.dumps(stats, indent=2, default=str))
        return stats

    print("=" * 60)
    print("Shopify Spy — Discovery Status")
    print("=" * 60)
    print(f"  Total in apps table : {stats['total_apps']}")
    print(f"  Done                : {stats['done']}")
    print(f"  Pending (queue)     : {stats['pending']}")
    print(f"  Failed              : {stats['failed']}")
    print(f"  From sitemap        : {stats['sitemap_source']} (done: {stats['done_sitemap']})")
    print(f"  From keywords       : {stats['keyword_source']}")
    print(f"  search_results rows : {stats['search_results_rows']} (__sitemap__: {stats['search_results_sitemap']})")
    if stats.get("log_file"):
        print(f"  Log                 : {stats['log_file']}")
        print(f"  Log mtime           : {stats.get('log_mtime')}")
    print("-" * 60)
    print("Recent activity:")
    for r in recent:
        title = (r.get("title") or r.get("slug") or "?")[:40]
        st = r.get("scrape_status")
        when = r.get("last_scraped_at") or "-"
        err = r.get("last_error") or ""
        print(f"  [{st}] {title:<40} scraped={when} {err[:40]}")
    if stats.get("log_tail"):
        print("-" * 60)
        print("Log tail:")
        for ln in stats["log_tail"]:
            print(f"  {ln}")
    print("=" * 60)
    print("Commands:")
    print("  python3 discover.py status")
    print("  python3 discover.py run --limit 2000")
    print("  tail -f ~/Work/shopify-spy/discover_run.log")
    return stats


def status_watch(interval: int = 30) -> None:
    while True:
        os.system("clear" if sys.platform != "win32" else "cls")
        status()
        print(f"\n(refreshing every {interval}s — Ctrl+C to stop)")
        time.sleep(interval)


# ---------------------------------------------------------------------------
# Run loop
# ---------------------------------------------------------------------------

def log(msg: str, log_file: Optional[str] = None) -> None:
    ts = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    line = f"[{ts}] {msg}"
    print(line, flush=True)
    if log_file:
        try:
            with open(log_file, "a") as f:
                f.write(line + "\n")
        except OSError:
            pass


def pick_pending(limit: int) -> list[dict]:
    # Prefer newest lastmod first (fresh apps), then id
    return db_run(
        """
        SELECT slug, url, lastmod, scrape_attempts
        FROM apps
        WHERE scrape_status = 'pending'
          AND scrape_attempts < ?
        ORDER BY
          CASE WHEN lastmod IS NULL THEN 1 ELSE 0 END,
          lastmod DESC,
          id ASC
        LIMIT ?
        """,
        [MAX_RETRIES, limit],
    )


def run_discover(limit: int, delay: float, dry_run: bool = False) -> None:
    log_file = os.path.join(LOG_DIR, "discover_run.log")
    log(f"Starting discover run limit={limit} delay={delay}s dry_run={dry_run}", log_file)

    # Ensure schema + queue
    init_schema()
    # Always re-sync sitemap so new apps appear; cheap HTTP
    try:
        sync_sitemap()
    except Exception as e:
        log(f"⚠ sitemap sync failed (continuing with existing queue): {e}", log_file)

    queue = pick_pending(limit)
    if not queue:
        log("Queue empty — nothing to scrape.", log_file)
        status()
        return

    log(f"Queue size this run: {len(queue)}", log_file)
    if dry_run:
        for a in queue[:20]:
            print(f"  would scrape: {a['slug']} lastmod={a.get('lastmod')}")
        if len(queue) > 20:
            print(f"  ... and {len(queue) - 20} more")
        return

    done = 0
    failed = 0
    consecutive_errors = 0

    for i, app in enumerate(queue, 1):
        slug = app["slug"]
        url = app["url"] or f"https://apps.shopify.com/{slug}"
        log(f"[{i}/{len(queue)}] Scraping {slug} ...", log_file)

        success = False
        last_err = None
        for attempt in range(1, MAX_RETRIES + 1):
            try:
                data = scrape_one(slug, url)
                mark_done(slug, data)
                done += 1
                consecutive_errors = 0
                success = True
                log(
                    f"  ✅ {data.get('title', slug)[:50]} | "
                    f"★{data.get('rating') or '?'} | "
                    f"rev={data.get('review_count') or '0'} | "
                    f"recent30={data.get('recent_reviews_30_days')} | "
                    f"trend={data.get('trending_score')}",
                    log_file,
                )
                break
            except NotFoundError as e:
                last_err = str(e)
                mark_failed(slug, last_err, permanent=True)
                failed += 1
                consecutive_errors += 1
                log(f"  ❌ 404 permanent: {slug}", log_file)
                break
            except RateLimitError as e:
                last_err = str(e)
                consecutive_errors += 1
                wait = min(ERROR_BACKOFF_MAX, ERROR_BACKOFF_BASE * (2 ** (attempt - 1)))
                wait += random.uniform(0, 10)
                log(f"  ⏳ rate limited attempt {attempt}/{MAX_RETRIES}, sleep {wait:.0f}s", log_file)
                time.sleep(wait)
            except TransientError as e:
                last_err = str(e)
                consecutive_errors += 1
                wait = min(ERROR_BACKOFF_MAX, ERROR_BACKOFF_BASE * (2 ** (attempt - 1)))
                log(f"  ⏳ transient ({e}) attempt {attempt}/{MAX_RETRIES}, sleep {wait:.0f}s", log_file)
                time.sleep(wait)
            except Exception as e:
                last_err = str(e)
                consecutive_errors += 1
                wait = min(ERROR_BACKOFF_MAX, ERROR_BACKOFF_BASE * attempt)
                log(f"  ⏳ error ({e}) attempt {attempt}/{MAX_RETRIES}, sleep {wait:.0f}s", log_file)
                time.sleep(wait)

        if not success and last_err and "404" not in last_err:
            # leave pending for resume if under max attempts, else failed
            attempts_now = int(app.get("scrape_attempts") or 0) + MAX_RETRIES
            permanent = attempts_now >= MAX_RETRIES
            mark_failed(slug, last_err or "unknown", permanent=permanent)
            failed += 1
            log(f"  ❌ gave up on {slug}: {last_err}", log_file)

        # Circuit breaker: many consecutive errors → long pause
        if consecutive_errors >= 5:
            pause = ERROR_BACKOFF_MAX
            log(f"  🛑 {consecutive_errors} consecutive errors — cooling down {pause:.0f}s", log_file)
            time.sleep(pause)
            consecutive_errors = 0

        # Pace between apps
        if i < len(queue):
            jitter = random.uniform(0, 2)
            time.sleep(max(MIN_DELAY, delay) + jitter)

        # Periodic status every 25 apps
        if i % 25 == 0:
            st = status(json_mode=False)
            log(
                f"Progress checkpoint: done_this_run={done} failed_this_run={failed} "
                f"pending_left={st.get('pending')}",
                log_file,
            )

    log(f"Run complete. success={done} failed={failed}", log_file)
    status()


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main() -> None:
    parser = argparse.ArgumentParser(description="Shopify sitemap discovery scraper")
    sub = parser.add_subparsers(dest="cmd", required=True)

    sub.add_parser("init", help="Create apps table + backfill from search_results")
    sub.add_parser("sync-sitemap", help="Fetch sitemap and queue unknown apps")
    p_status = sub.add_parser("status", help="Show discovery progress")
    p_status.add_argument("--json", action="store_true")
    p_status.add_argument("--watch", action="store_true")
    p_status.add_argument("--interval", type=int, default=30)

    p_run = sub.add_parser("run", help="Detail-scrape pending apps sequentially")
    p_run.add_argument("--limit", type=int, default=2000)
    p_run.add_argument("--delay", type=float, default=DEFAULT_DELAY)

    p_dry = sub.add_parser("dry-run", help="Show what would be scraped")
    p_dry.add_argument("--limit", type=int, default=2000)

    args = parser.parse_args()

    if args.cmd == "init":
        init_schema()
        backfill_from_search_results()
        status()
    elif args.cmd == "sync-sitemap":
        init_schema()
        sync_sitemap()
        status()
    elif args.cmd == "status":
        # tolerate missing table
        try:
            if args.watch:
                status_watch(args.interval)
            else:
                status(json_mode=args.json)
        except RuntimeError as e:
            if "no such table" in str(e).lower():
                print("apps table missing — run: python3 discover.py init")
                sys.exit(1)
            raise
    elif args.cmd == "run":
        run_discover(limit=args.limit, delay=args.delay, dry_run=False)
    elif args.cmd == "dry-run":
        init_schema()
        try:
            sync_sitemap()
        except Exception as e:
            print(f"sitemap sync warning: {e}")
        run_discover(limit=args.limit, delay=DEFAULT_DELAY, dry_run=True)
    else:
        parser.print_help()


if __name__ == "__main__":
    main()

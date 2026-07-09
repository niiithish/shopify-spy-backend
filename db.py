#!/usr/bin/env python3
"""Quick Turso DB helper for Shopify Spy data.
Usage:  python3 db.py "SELECT * FROM search_results LIMIT 5"
        python3 db.py --tables          # list tables
        python3 db.py --schema <table>  # show table schema
        python3 db.py --search <keyword> # search results for keyword
"""
import urllib.request, json, sys, os
from urllib.parse import urlencode

DB_URL = "https://shopify-spy-v2-nithish.aws-ap-south-1.turso.io/v2/pipeline"

def get_token():
    env_path = os.path.expanduser("~/Work/shopify-spy/.env")
    with open(env_path) as f:
        for line in f:
            if "TURSO_AUTH_TOKEN" in line:
                return line.split("=", 1)[1].strip()
    raise RuntimeError("TURSO_AUTH_TOKEN not found in .env")

token = get_token()

def run(sql, pretty=True):
    data = json.dumps({"requests": [{"type": "execute", "stmt": {"sql": sql}}]}).encode()
    req = urllib.request.Request(DB_URL, data=data, headers={
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json"
    })
    result = json.loads(urllib.request.urlopen(req).read())
    output = []
    for r in result["results"]:
        if "response" in r:
            cols = [c["name"] for c in r["response"]["result"]["cols"]]
            rows = r["response"]["result"]["rows"]
            for row in rows:
                vals = [cell.get("value", str(cell)) for cell in row]
                output.append(dict(zip(cols, vals)))
        elif "error" in r:
            output.append({"ERROR": r["error"]["message"]})
    return output

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(0)

    cmd = sys.argv[1]

    if cmd == "--tables":
        res = run("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
        print("Tables:")
        for r in res:
            print(f"  {r['name']}")

    elif cmd == "--schema":
        if len(sys.argv) < 3:
            print("Usage: python3 db.py --schema <table_name>")
            sys.exit(1)
        res = run(f"SELECT sql FROM sqlite_master WHERE name='{sys.argv[2]}'")
        for r in res:
            print(r.get("sql", "Not found"))

    elif cmd == "--search":
        if len(sys.argv) < 3:
            print("Usage: python3 db.py --search <keyword>")
            sys.exit(1)
        keyword = sys.argv[2]
        res = run(f"SELECT * FROM search_results WHERE keyword LIKE '%{keyword}%' ORDER BY review_count DESC LIMIT 30")
        if not res:
            print(f"No results for '{keyword}'")
        else:
            print(f"{'App':<50} {'Rating':<8} {'Reviews':<8} {'Price':<12} {'Keyword'}")
            print("-"*100)
            for r in res:
                print(f"{r['title'][:48]:<50} {r.get('rating','?'):<8} {r.get('review_count','0'):<8} {r.get('price','?'):<12} {r.get('keyword','')}")

    elif cmd == "--favorites":
        res = run("SELECT * FROM favorites ORDER BY created_at DESC")
        print("Favorites:")
        for r in res:
            print(f"  app_id={r['app_id']}, saved={r['created_at']}")

    elif cmd == "--discover-status":
        import subprocess, sys
        subprocess.check_call([sys.executable, "discover.py", "status"])

    else:
        # Treat as raw SQL
        res = run(" ".join(sys.argv[1:]))
        for r in res:
            print(json.dumps(r, indent=2))

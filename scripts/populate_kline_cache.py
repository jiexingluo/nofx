#!/usr/bin/env python3
"""Download kline data from Binance public archive and populate the local cache.
Uses curl to avoid Python SSL certificate issues."""

import csv
import io
import json
import os
import subprocess
import zipfile
from datetime import datetime, timedelta, timezone

CACHE_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "data", "kline_cache")
BASE_URL = "https://data.binance.vision/data/futures/um/daily/klines"

SYMBOLS = ["POWERUSDT", "PIPPINUSDT", "ZROUSDT"]
TIMEFRAMES = ["15m", "1h", "4h"]

START_DATE = datetime(2025, 12, 25, tzinfo=timezone.utc)
END_DATE = datetime(2026, 2, 28, tzinfo=timezone.utc)


def download_day(symbol, tf, date):
    date_str = date.strftime("%Y-%m-%d")
    url = f"{BASE_URL}/{symbol}/{tf}/{symbol}-{tf}-{date_str}.zip"
    try:
        result = subprocess.run(
            ["curl", "-s", "--max-time", "15", "-o", "-", url],
            capture_output=True, timeout=20
        )
        if result.returncode != 0 or len(result.stdout) < 100:
            return None
        zf = zipfile.ZipFile(io.BytesIO(result.stdout))
        names = zf.namelist()
        if not names:
            return None
        csv_data = zf.read(names[0]).decode("utf-8")
    except Exception:
        return None

    klines = []
    reader = csv.reader(io.StringIO(csv_data))
    next(reader, None)  # skip header
    for row in reader:
        if len(row) < 7:
            continue
        try:
            klines.append({
                "openTime": int(row[0]),
                "open": float(row[1]),
                "high": float(row[2]),
                "low": float(row[3]),
                "close": float(row[4]),
                "volume": float(row[5]),
                "closeTime": int(row[6]),
            })
        except (ValueError, IndexError):
            continue
    return klines


def main():
    os.makedirs(CACHE_DIR, exist_ok=True)
    total = len(SYMBOLS) * len(TIMEFRAMES)
    done = 0

    for symbol in SYMBOLS:
        for tf in TIMEFRAMES:
            done += 1
            cache_file = os.path.join(CACHE_DIR, f"{symbol}_{tf}.json")
            print(f"[{done}/{total}] {symbol} {tf}...", end=" ", flush=True)

            all_klines = []
            date = START_DATE
            days_ok = 0
            days_fail = 0

            while date <= END_DATE:
                day_klines = download_day(symbol, tf, date)
                if day_klines:
                    all_klines.extend(day_klines)
                    days_ok += 1
                else:
                    days_fail += 1
                date += timedelta(days=1)

            by_time = {}
            for k in all_klines:
                by_time[k["openTime"]] = k
            merged = sorted(by_time.values(), key=lambda x: x["openTime"])

            with open(cache_file, "w") as f:
                json.dump(merged, f)

            print(f"{len(merged)} klines ({days_ok} days ok, {days_fail} failed)")

    print(f"\nDone! Cache populated in: {CACHE_DIR}")


if __name__ == "__main__":
    main()

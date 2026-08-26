#!/usr/bin/env python3
"""行业数据预热脚本：遍历全市场股票，逐个调用 /api/industry 灌入SQLite缓存。
用法: python warm_industry.py [--base http://localhost:8080] [--workers 4]
"""
import argparse
import json
import sys
import time
import urllib.request

BASE = "http://localhost:8080"


def get_json(url, timeout=30):
    for attempt in range(3):
        try:
            with urllib.request.urlopen(url, timeout=timeout) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except Exception as e:
            print(f"  重试{attempt + 1}/3: {e}", flush=True)
            time.sleep(2 * (attempt + 1))
    return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=BASE)
    parser.add_argument("--delay", type=float, default=1.5, help="每次请求间隔秒数")
    args = parser.parse_args()

    # 1. 拉取全市场股票代码
    data = get_json(f"{args.base}/api/codes")
    if not data or data.get("code") != 0:
        print("获取股票列表失败")
        sys.exit(1)
    all_codes = [c["code"] for c in data["data"]["codes"]]

    # 2. 拉取已缓存代码，只补缺的；全部已有则直接退出
    cached_data = get_json(f"{args.base}/api/industry/codes")
    if cached_data and cached_data.get("code") == 0:
        cached = set(cached_data["data"]["list"])
        codes = [c for c in all_codes if c not in cached]
        print(f"全市场 {len(all_codes)} 只，已缓存 {len(cached)} 只", flush=True)
        if not codes:
            print("所有股票行业数据均已缓存，无需预热")
            return
        print(f"待拉取 {len(codes)} 只，开始预热...", flush=True)
    else:
        codes = all_codes
        print(f"未获取到缓存列表，全量预热 {len(codes)} 只...", flush=True)

    total = len(codes)

    ok = fail = 0
    start = time.time()
    for i, code in enumerate(codes, 1):
        result = get_json(f"{args.base}/api/industry?code={code}")
        if result and result.get("code") == 0:
            ok += 1
        else:
            fail += 1
            print(f"  [{code}] 失败", flush=True)
        if i % 100 == 0:
            speed = i / (time.time() - start)
            remain = (total - i) / max(speed, 0.01) / 60
            print(f"进度 {i}/{total}  成功{ok} 失败{fail}  预计剩余 {remain:.0f} 分钟", flush=True)
        time.sleep(args.delay)

    print(f"完成：成功 {ok}，失败 {fail}，耗时 {(time.time() - start) / 60:.0f} 分钟")


if __name__ == "__main__":
    main()

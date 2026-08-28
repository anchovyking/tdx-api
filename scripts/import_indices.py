#!/usr/bin/env python3
"""
导入投资数据网指数数据到 SQLite 数据库
数据源: 投资数据网导出的 xlsx 文件（加权平均值-3年）
存储: data/database/indices.db

表 indices 仅保留核心字段：
  code       指数代码（去后缀，如 000300）
  name       指数名称
  market     市场/后缀（如 sh/sz/cs/cn/bj/us/hk）
  source     数据来源（固定 touzid）
  updated_at 导入时间

用法:
  python import_indices.py
  python import_indices.py --file /path/to/file.xlsx
  python import_indices.py --file /path/to/file.xlsx --db data/database/indices.db
"""
import argparse
import os
import re
import sqlite3
import sys
import zipfile
from datetime import datetime


def decode_xlsx_strings(xlsx_path):
    """从 xlsx 中解码 sharedStrings.xml，正确处理 GBK 编码的中文"""
    strings = []
    with zipfile.ZipFile(xlsx_path, 'r') as z:
        with z.open('xl/sharedStrings.xml') as f:
            content = f.read()
    si_blocks = re.findall(rb'<si>(.*?)</si>', content, re.DOTALL)
    for block in si_blocks:
        t_matches = re.findall(rb'<t[^>]*>([^<]*)</t>', block)
        if not t_matches:
            strings.append('')
            continue
        raw = b''.join(t_matches)
        try:
            decoded = raw.decode('utf-8')
        except UnicodeDecodeError:
            try:
                decoded = raw.decode('gbk')
            except UnicodeDecodeError:
                decoded = raw.decode('utf-8', errors='replace')
        strings.append(decoded)
    return strings


def parse_sheet1_xml(xlsx_path):
    """从 xlsx 的 sheet1.xml 解析行数据，处理 inlineStr 与 sharedString 引用"""
    with zipfile.ZipFile(xlsx_path, 'r') as z:
        with z.open('xl/sharedStrings.xml') as f:
            ss_content = f.read()
        with z.open('xl/worksheets/sheet1.xml') as f:
            sheet_content = f.read()

    strings = decode_xlsx_strings(xlsx_path)

    rows = []
    row_blocks = re.findall(rb'<row[^>]*r="(\d+)"[^>]*>(.*?)</row>', sheet_content, re.DOTALL)
    for row_num, row_content in row_blocks:
        cells = {}
        cell_matches = re.findall(rb'<c\s+([^/>]*)(?:/>|>(.*?)</c>)', row_content, re.DOTALL)
        for attrs, val in cell_matches:
            ref_match = re.search(rb'r="([A-Z]+)\d+"', attrs)
            if not ref_match:
                continue
            col = ref_match.group(1).decode('ascii')
            t_match = re.search(rb't="([^"]+)"', attrs)
            cell_type = t_match.group(1).decode('ascii') if t_match else 'n'

            if val:
                v_match = re.search(rb'<v>([^<]*)</v>', val)
                is_match = re.search(rb'<is><t[^>]*>([^<]*)</t></is>', val, re.DOTALL)
                if is_match:
                    raw = is_match.group(1)
                    try:
                        value = raw.decode('utf-8')
                    except UnicodeDecodeError:
                        try:
                            value = raw.decode('gbk')
                        except UnicodeDecodeError:
                            value = raw.decode('utf-8', errors='replace')
                elif v_match:
                    raw_val = v_match.group(1)
                    if cell_type == 's':
                        try:
                            idx = int(raw_val.decode('ascii'))
                            value = strings[idx] if idx < len(strings) else ''
                        except (ValueError, IndexError):
                            value = ''
                    elif cell_type == 'b':
                        value = raw_val.decode('ascii')
                    else:
                        try:
                            value = float(raw_val.decode('ascii'))
                        except ValueError:
                            value = raw_val.decode('ascii', errors='replace')
                else:
                    value = None
            else:
                value = None
            cells[col] = value
        rows.append((int(row_num), cells))
    return rows


def split_code(raw):
    """将 000300.sh / 931247.cs / HSSCID 拆分为 (code, market)。

    market 取值及含义：
        sh = 上证（沪市）
        sz = 深证（深市）
        cs = 中证（中证指数有限公司编制）
        cn = 国证·跨市场（深圳证券信息/中证国证）
        bj = 北证（北京证券交易所）
        hk = 港股（恒生/港股通指数，原文件无后缀的 H 系列也归为 hk）
        us = 海外（美股，如 .NDX.US 纳指、.INX.US 标普500、.DJI.US 道琼斯）
        ms = MSCI（明晟指数）
    """
    if not raw or not isinstance(raw, str):
        return None, None
    if '.' in raw:
        code, market = raw.rsplit('.', 1)
        market = market.lower()
        # tz 后缀为投资数据网自定义指数（100000-100030 等），非 TDX 可查询品种，跳过不导入
        if market == 'tz':
            return None, None
        return code, market
    # 无后缀：H 系列为恒生/港股指数
    return raw, 'hk'


def main():
    parser = argparse.ArgumentParser(description='导入投资数据网指数数据到 SQLite')
    parser.add_argument('--file', default=r'C:\Users\Administrator\Downloads\指数列表-加权平均值-3年.xlsx',
                        help='xlsx 文件路径')
    parser.add_argument('--db', default='data/database/indices.db', help='SQLite 数据库路径')
    args = parser.parse_args()

    if not os.path.exists(args.file):
        print(f'文件不存在: {args.file}')
        sys.exit(1)

    print(f'读取文件: {args.file}')
    rows = parse_sheet1_xml(args.file)
    print(f'共 {len(rows)} 行（含表头）')

    # 第 1-2 行是表头，从第 3 行开始是数据
    data_rows = rows[2:]
    print(f'数据行: {len(data_rows)}')

    os.makedirs(os.path.dirname(args.db) or '.', exist_ok=True)
    conn = sqlite3.connect(args.db)
    cur = conn.cursor()
    cur.execute('DROP TABLE IF EXISTS indices')
    cur.execute('''
        CREATE TABLE indices (
            code       TEXT NOT NULL,
            name       TEXT,
            market     TEXT,
            source     TEXT DEFAULT 'touzid',
            updated_at TEXT,
            PRIMARY KEY (code, market)
        )
    ''')
    conn.commit()

    now = datetime.now().isoformat(timespec='seconds')
    inserted = 0
    skipped = 0

    for row_num, cells in data_rows:
        code, market = split_code(cells.get('A'))
        if not code:
            skipped += 1
            continue

        name = cells.get('B', '')
        if not isinstance(name, str):
            name = str(name) if name else ''
        name = name.strip()

        try:
            cur.execute(
                'INSERT OR REPLACE INTO indices (code, name, market, source, updated_at) VALUES (?, ?, ?, ?, ?)',
                (code, name, market, 'touzid', now),
            )
            inserted += 1
        except Exception as e:
            print(f'插入 {code} 失败: {e}')
            skipped += 1

    conn.commit()
    conn.close()
    print(f'插入 {inserted} 条，跳过 {skipped} 条')
    print(f'数据库: {args.db}')


if __name__ == '__main__':
    main()

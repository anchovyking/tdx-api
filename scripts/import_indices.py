#!/usr/bin/env python3
"""
导入投资数据网指数数据到 SQLite 数据库
数据源: 投资数据网导出的 xlsx 文件（加权平均值-3年）
存储: data/database/indices.db

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
import time
import zipfile
from datetime import datetime


def decode_xlsx_strings(xlsx_path):
    """从 xlsx 中解码 sharedStrings.xml，正确处理 GBK 编码的中文"""
    strings = []
    with zipfile.ZipFile(xlsx_path, 'r') as z:
        with z.open('xl/sharedStrings.xml') as f:
            content = f.read()

    # 匹配所有 <si>...</si> 块
    si_blocks = re.findall(rb'<si>(.*?)</si>', content, re.DOTALL)
    for block in si_blocks:
        # 块内可能有多个 <t>，也可能没有
        t_matches = re.findall(rb'<t[^>]*>([^<]*)</t>', block)
        if not t_matches:
            strings.append('')
            continue
        # 拼接所有 t 内容
        raw = b''.join(t_matches)
        # 尝试 GBK 解码
        try:
            decoded = raw.decode('gbk')
        except UnicodeDecodeError:
            try:
                decoded = raw.decode('utf-8')
            except UnicodeDecodeError:
                decoded = raw.decode('gbk', errors='replace')
        strings.append(decoded)
    return strings


def parse_sheet1_xml(xlsx_path):
    """从 xlsx 的 sheet1.xml 解析行数据，正确处理 inlineStr 和 sharedString 引用"""
    with zipfile.ZipFile(xlsx_path, 'r') as z:
        with z.open('xl/sharedStrings.xml') as f:
            ss_content = f.read()
        with z.open('xl/worksheets/sheet1.xml') as f:
            sheet_content = f.read()

    strings = decode_xlsx_strings(xlsx_path)

    rows = []
    # 匹配每个 <row>...</row>
    row_blocks = re.findall(rb'<row[^>]*r="(\d+)"[^>]*>(.*?)</row>', sheet_content, re.DOTALL)
    for row_num, row_content in row_blocks:
        cells = {}
        # 匹配每个 <c>
        cell_matches = re.findall(rb'<c\s+([^/>]*)(?:/>|>(.*?)</c>)', row_content, re.DOTALL)
        for attrs, val in cell_matches:
            # 解析 ref 如 r="A3"
            ref_match = re.search(rb'r="([A-Z]+)\d+"', attrs)
            if not ref_match:
                continue
            col = ref_match.group(1).decode('ascii')

            # 解析类型
            t_match = re.search(rb't="([^"]+)"', attrs)
            cell_type = t_match.group(1).decode('ascii') if t_match else 'n'

            # 解析值
            if val:
                v_match = re.search(rb'<v>([^<]*)</v>', val)
                is_match = re.search(rb'<is><t[^>]*>([^<]*)</t></is>', val, re.DOTALL)
                if is_match:
                    # inline string
                    raw = is_match.group(1)
                    try:
                        value = raw.decode('gbk')
                    except:
                        value = raw.decode('gbk', errors='replace')
                elif v_match:
                    raw_val = v_match.group(1)
                    if cell_type == 's':
                        # shared string index
                        try:
                            idx = int(raw_val.decode('ascii'))
                            value = strings[idx] if idx < len(strings) else ''
                        except:
                            value = ''
                    elif cell_type == 'b':
                        value = raw_val.decode('ascii')
                    else:
                        # numeric
                        try:
                            value = float(raw_val.decode('ascii'))
                        except:
                            value = raw_val.decode('ascii', errors='replace')
                else:
                    value = None
            else:
                value = None
            cells[col] = value
        rows.append((int(row_num), cells))
    return rows


def col_letter_to_index(letter):
    """A->0, B->1, ..., Z->25, AA->26, ..."""
    result = 0
    for c in letter:
        result = result * 26 + (ord(c) - ord('A') + 1)
    return result - 1


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
    print(f'共 {len(rows)} 行')

    if len(rows) < 3:
        print('数据行数不足')
        sys.exit(1)

    # 第 1-2 行是表头，从第 3 行开始是数据
    data_rows = rows[2:]
    print(f'数据行: {len(data_rows)}')

    # 建库
    os.makedirs(os.path.dirname(args.db) or '.', exist_ok=True)
    conn = sqlite3.connect(args.db)
    cur = conn.cursor()
    cur.execute('''
        CREATE TABLE IF NOT EXISTS indices (
            code TEXT PRIMARY KEY,
            name TEXT,
            publish_date TEXT,
            current_level REAL,
            year_return REAL,
            category TEXT,
            pe_ttm_current REAL,
            pe_ttm_percentile REAL,
            pe_ttm_avg REAL,
            pe_ttm_min REAL,
            pe_ttm_p20 REAL,
            pe_ttm_p50 REAL,
            pe_ttm_p80 REAL,
            pe_ttm_max REAL,
            pb_current REAL,
            pb_percentile REAL,
            pb_avg REAL,
            pb_min REAL,
            pb_p20 REAL,
            pb_p50 REAL,
            pb_p80 REAL,
            pb_max REAL,
            ps_current REAL,
            ps_percentile REAL,
            ps_avg REAL,
            ps_min REAL,
            ps_p20 REAL,
            ps_p50 REAL,
            ps_p80 REAL,
            ps_max REAL,
            roe_2025 REAL,
            roe_2024 REAL,
            roe_2023 REAL,
            dividend_yield REAL,
            market_cap TEXT,
            note TEXT,
            source TEXT DEFAULT 'touzid',
            updated_at TEXT
        )
    ''')
    conn.commit()

    now = datetime.now().isoformat(timespec='seconds')
    inserted = 0
    skipped = 0

    for row_num, cells in data_rows:
        code = cells.get('A')
        if not code or not isinstance(code, str):
            skipped += 1
            continue

        # 解析代码：去后缀作为主代码
        name = cells.get('B', '')
        if not isinstance(name, str):
            name = str(name) if name else ''

        # 发布时间
        publish_date = cells.get('C', '')
        if hasattr(publish_date, 'isoformat'):
            publish_date = publish_date.isoformat()
        elif not isinstance(publish_date, str):
            publish_date = str(publish_date) if publish_date else ''

        current_level = cells.get('D') if isinstance(cells.get('D'), (int, float)) else None
        year_return = cells.get('E') if isinstance(cells.get('E'), (int, float)) else None
        category = cells.get('F', '')
        if not isinstance(category, str):
            category = str(category) if category else ''

        # PE-TTM: G-N (列 6-13)
        pe_vals = []
        for col in ['G', 'H', 'I', 'J', 'K', 'L', 'M', 'N']:
            v = cells.get(col)
            pe_vals.append(v if isinstance(v, (int, float)) else None)

        # PB: O-V (列 14-21)
        pb_vals = []
        for col in ['O', 'P', 'Q', 'R', 'S', 'T', 'U', 'V']:
            v = cells.get(col)
            pb_vals.append(v if isinstance(v, (int, float)) else None)

        # PS: W-AD (列 22-29)
        ps_vals = []
        for col in ['W', 'X', 'Y', 'Z', 'AA', 'AB', 'AC', 'AD']:
            v = cells.get(col)
            ps_vals.append(v if isinstance(v, (int, float)) else None)

        # ROE: AE-AG (列 30-32)
        roe_vals = []
        for col in ['AE', 'AF', 'AG']:
            v = cells.get(col)
            roe_vals.append(v if isinstance(v, (int, float)) else None)

        # 股息率: AH
        dividend_yield = cells.get('AH') if isinstance(cells.get('AH'), (int, float)) else None
        # 市值: AI (可能是 "68.47万亿" 等文本，存为字符串)
        market_cap = cells.get('AI', '')
        if not isinstance(market_cap, str):
            market_cap = str(market_cap) if market_cap else ''
        # 备注: AJ
        note = cells.get('AJ', '')
        if not isinstance(note, str):
            note = str(note) if note else ''

        try:
            cur.execute('''
                INSERT OR REPLACE INTO indices
                (code, name, publish_date, current_level, year_return, category,
                 pe_ttm_current, pe_ttm_percentile, pe_ttm_avg, pe_ttm_min,
                 pe_ttm_p20, pe_ttm_p50, pe_ttm_p80, pe_ttm_max,
                 pb_current, pb_percentile, pb_avg, pb_min,
                 pb_p20, pb_p50, pb_p80, pb_max,
                 ps_current, ps_percentile, ps_avg, ps_min,
                 ps_p20, ps_p50, ps_p80, ps_max,
                 roe_2025, roe_2024, roe_2023,
                 dividend_yield, market_cap, note, source, updated_at)
                VALUES (?, ?, ?, ?, ?, ?,
                        ?, ?, ?, ?, ?, ?, ?, ?,
                        ?, ?, ?, ?, ?, ?, ?, ?,
                        ?, ?, ?, ?, ?, ?, ?, ?,
                        ?, ?, ?,
                        ?, ?, ?, 'touzid', ?)
            ''', (code, name, publish_date, current_level, year_return, category,
                  *(pe_vals + pb_vals + ps_vals + roe_vals),
                  dividend_yield, market_cap, note, now))
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

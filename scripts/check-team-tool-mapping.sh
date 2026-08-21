#!/usr/bin/env bash
# check-team-tool-mapping.sh — P5 零孤儿校验（Gate2 第 6 维，§6.2）
#
# 校验 docs/team-mcp-port/TOOL_MAPPING.md 与 cache/mult_agent_mcp/mult_agent_mcp.py
# 的 @mcp.tool 注册清单双向差集为零、无重复、无孤儿行。只读，可重复执行。
#
# cache/ 是 gitignore 的只读移植参考源（TASK.md §5.4），CI 检出里不存在：此时本
# 脚本跳过并退出 0。CI 侧的等价护栏是 internal/team/toolmapping_test.go，它把 71
# 个工具名内联，不依赖 cache/。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAP="$ROOT/docs/team-mcp-port/TOOL_MAPPING.md"
SRC="$ROOT/cache/mult_agent_mcp/mult_agent_mcp.py"

# 注册源实测数量（2026-08-21 复核）：71 个列首 @mcp.tool 装饰器。
readonly EXPECTED_TOOLS=71

fail=0
note() { printf '%s\n' "$*"; }
die()  { printf 'FAIL: %s\n' "$*" >&2; fail=1; }

[ -f "$MAP" ] || { printf 'FAIL: mapping not found: %s\n' "$MAP" >&2; exit 1; }
if [ ! -f "$SRC" ]; then
  note "SKIP: 移植参考源缺失（$SRC）"
  note "      cache/ 未检出属正常；等价校验见 internal/team/toolmapping_test.go"
  exit 0
fi

# 1. 映射表工具名（表首列；反引号可选，与表格实际写法一致）
map_names() {
  sed -n 's/^| *`\{0,1\}\([A-Za-z_][A-Za-z0-9_]*\)`\{0,1\} *|.*/\1/p' "$MAP"
}

# 2. 源注册工具名（仅真装饰器行，排除注释/字符串内 "@mcp.tool" 文本）
src_names() {
  python3 - "$SRC" <<'EOF'
import re, sys
path = sys.argv[1]
names, pending = [], False
with open(path, encoding='utf-8') as f:
    for line in f:
        stripped = line.strip()
        if re.fullmatch(r'@mcp\.tool\s*', stripped):
            pending = True
            continue
        if pending:
            m = re.match(r'def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(', stripped)
            if m:
                names.append(m.group(1))
                pending = False
if len(names) != len(set(names)):
    raise SystemExit(f"source has duplicate registrations: {len(names)} vs {len(set(names))}")
for n in sorted(names):
    print(n)
EOF
}

# 3. 数量断言
map_count=$(map_names | wc -l | tr -d ' ')
src_count=$(src_names | wc -l | tr -d ' ')

note "mapping rows:   $map_count"
note "source tools:   $src_count (expected $EXPECTED_TOOLS)"
if [ "$src_count" -ne "$EXPECTED_TOOLS" ]; then
  die "source @mcp.tool count = $src_count, expected $EXPECTED_TOOLS"
fi
if [ "$map_count" -ne "$src_count" ]; then
  die "mapping rows ($map_count) != source tools ($src_count)"
fi

# 4. 双向差集为零（零孤儿 / 零多写）
if ! diff <(map_names | sort) <(src_names | sort) >/dev/null; then
  note "--- diff (mapping vs source) ---"
  diff <(map_names | sort) <(src_names | sort) || true
  die "mapping and source tool sets differ"
fi

# 5. 重复校验
dupes=$(map_names | sort | uniq -d)
if [ -n "$dupes" ]; then
  printf '%s\n' "$dupes" >&2
  die "mapping has duplicate tool names"
fi

# 6. 逐行必填校验：语义域非空；落点非空；状态 ∈ {核心,插件,废弃}
#    列序同 §6.1 模板：旧工具名 | 旧证据 | 语义域 | 新落点 | 状态 | 验收证据
while IFS='|' read -r _ name evidence domain landing status _; do
  name=$(printf '%s' "$name" | tr -d ' `')
  [ -n "$name" ] || continue
  case "$(printf '%s' "$status" | tr -d ' ')" in
    核心|插件|废弃) ;;
    *) die "row $name: illegal status '$(printf '%s' "$status" | tr -d ' ')' (want 核心/插件/废弃)";;
  esac
  [ -n "$(printf '%s' "$domain" | tr -d ' ')" ]   || die "row $name: empty semantic domain"
  [ -n "$(printf '%s' "$landing" | tr -d ' ')" ]  || die "row $name: empty landing"
  case "$evidence" in
    *mult_agent_mcp.py*) ;;
    *) die "row $name: evidence not traceable to the old symbol";;
  esac
done < <(map_names >/dev/null; grep -E '^\| *`?[A-Za-z_][A-Za-z0-9_]*`? *\|' "$MAP")

# 7. 语义域分布（便于人读，不设断言）
note "--- semantic-domain distribution ---"
grep -E '^\| *`?[A-Za-z_][A-Za-z0-9_]*`? *\|' "$MAP" | awk -F'|' '{gsub(/^ +| +$/, "", $4); print $4}' | sort | uniq -c | sort -rn

if [ "$fail" -ne 0 ]; then
  note "RESULT: FAIL"
  exit 1
fi
note "RESULT: PASS — zero orphan, zero duplicate"

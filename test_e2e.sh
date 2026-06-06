#!/bin/bash
#
# test_e2e.sh — 端到端测试执行脚本
#
# 流程: 编译项目 → 启动 Docker 依赖 → 运行测试 → 生成报告 → 清理
#
set -euo pipefail

# ---- 配置 ----
PROJECT_ROOT="$(cd "$(dirname "$0")" && pwd)"
REPORT_DIR="${PROJECT_ROOT}/test/reports"
TIMESTAMP="$(date '+%Y%m%d_%H%M%S')"
REPORT_BASENAME="e2e-report-${TIMESTAMP}"

# 颜色输出
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

info()  { echo -e "${GREEN}[INFO]${NC}  $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; }

# ---- 清理函数 ----
cleanup() {
    local exit_code=$?
    info "清理资源..."

    # 停止并移除 Docker 容器
    if docker compose ps --services --filter "status=running" 2>/dev/null | grep -q .; then
        info "停止 Docker 服务..."
        docker compose down -v --remove-orphans 2>/dev/null || true
    fi

    # 清理缓存二进制
    rm -f "${PROJECT_ROOT}/output/notification-server"

    if [ $exit_code -eq 0 ]; then
        info "全部完成（退出码: 0）"
    else
        error "执行失败（退出码: ${exit_code}）"
    fi
    exit $exit_code
}
trap cleanup EXIT

# ---- 1. 编译 ----
info "编译项目..."
cd "${PROJECT_ROOT}"
bash build.sh
info "编译完成"

# ---- 2. 启动 Docker 依赖 ----
info "启动 Docker 服务 (PostgreSQL, RabbitMQ)..."
docker compose up -d postgres rabbitmq

# 等待 Postgres 就绪
info "等待 PostgreSQL 就绪..."
for i in $(seq 1 30); do
    if docker compose exec -T postgres pg_isready -U notify -d notification >/dev/null 2>&1; then
        info "PostgreSQL 就绪（${i}s）"
        break
    fi
    if [ $i -eq 30 ]; then
        error "PostgreSQL 启动超时"
        exit 1
    fi
    sleep 1
done

# 等待 RabbitMQ 就绪
info "等待 RabbitMQ 就绪..."
for i in $(seq 1 30); do
    if docker compose exec -T rabbitmq rabbitmq-diagnostics check_port_connectivity >/dev/null 2>&1; then
        info "RabbitMQ 就绪（${i}s）"
        break
    fi
    if [ $i -eq 30 ]; then
        error "RabbitMQ 启动超时"
        exit 1
    fi
    sleep 1
done

# ---- 3. 数据库迁移 ----
info "运行数据库迁移..."
for f in "${PROJECT_ROOT}"/migrations/*.up.sql; do
    docker compose exec -T postgres psql -U notify -d notification -f - < "$f" >/dev/null 2>&1 || true
done
info "迁移完成"

# ---- 4. 运行测试 ----
info "运行端到端测试..."
mkdir -p "${REPORT_DIR}"

# 执行测试
# go test -v 输出完整日志; tee 同时输出到终端和日志文件
GOFLAGS="-count=1"
GO_TEST_FLAGS="-v -count=1 -timeout 600s"
set +e  # 允许测试失败，cleanup 仍会执行
go test "${PROJECT_ROOT}/test/e2e/..." ${GO_TEST_FLAGS} 2>&1 \
    | tee "${REPORT_DIR}/${REPORT_BASENAME}.log"
E2E_EXIT_CODE=$?
set -e

# 从日志中提取简明结果摘要
grep -E "^--- (PASS|FAIL)|^[[:space:]]+--- (PASS|FAIL)|^(PASS|FAIL|ok)" "${REPORT_DIR}/${REPORT_BASENAME}.log" \
    > "${REPORT_DIR}/${REPORT_BASENAME}.summary" 2>/dev/null || true

# ---- 5. 生成报告 ----
# Count top-level test functions only (not indented subtests)
PASS_COUNT=$(grep -c "^--- PASS" "${REPORT_DIR}/${REPORT_BASENAME}.log" 2>/dev/null || true)
FAIL_COUNT=$(grep -c "^--- FAIL" "${REPORT_DIR}/${REPORT_BASENAME}.log" 2>/dev/null || true)
# Remove trailing whitespace/newlines that '|| true' can introduce
PASS_COUNT=${PASS_COUNT%% *}
FAIL_COUNT=${FAIL_COUNT%% *}
PASS_COUNT=${PASS_COUNT:-0}
FAIL_COUNT=${FAIL_COUNT:-0}
TOTAL=$(( PASS_COUNT + FAIL_COUNT ))
if [ "${TOTAL}" -gt 0 ] 2>/dev/null; then
    PASS_RATE="$(( PASS_COUNT * 100 / TOTAL ))%"
else
    PASS_RATE="N/A"
fi

cat > "${REPORT_DIR}/${REPORT_BASENAME}.md" << REPORTOF
# E2E 测试报告

- **执行时间**: $(date '+%Y-%m-%d %H:%M:%S')
- **Commit**: $(git -C "${PROJECT_ROOT}" log -1 --format="%h" 2>/dev/null || echo "N/A")
- **测试结果**: $( [ ${E2E_EXIT_CODE} -eq 0 ] && echo "✅ 全部通过" || echo "❌ 有失败" )

## 统计

| 指标 | 数值 |
|------|------|
| 通过 | ${PASS_COUNT} |
| 失败 | ${FAIL_COUNT} |
| 通过率 | ${PASS_RATE} |

## 失败详情

$(grep -E "^--- FAIL|^[[:space:]]+--- FAIL" "${REPORT_DIR}/${REPORT_BASENAME}.log" 2>/dev/null || echo "无")

## 完整日志

见 [${REPORT_BASENAME}.log](${REPORT_BASENAME}.log)
REPORTOF

info "报告已生成: ${REPORT_DIR}/${REPORT_BASENAME}.md"
info "日志已保存: ${REPORT_DIR}/${REPORT_BASENAME}.log"
info "摘要已保存: ${REPORT_DIR}/${REPORT_BASENAME}.summary"

# ---- 6. 更新测试用例文档 ----
DOC_FILE="${PROJECT_ROOT}/docs/notification-test-cases.md"
CURRENT_HASH=$(git -C "${PROJECT_ROOT}" log -1 --format="%h" 2>/dev/null || echo "unknown")
CURRENT_DATE=$(date '+%Y-%m-%d')

if [ -f "${DOC_FILE}" ]; then
    info "更新测试用例文档..."

    # 用 Python 解析日志，更新文档中的状态标记
    python3 -c "
import re, sys

log_file = '${REPORT_DIR}/${REPORT_BASENAME}.log'
doc_file = '${DOC_FILE}'
new_hash = '${CURRENT_HASH}'
new_date = '${CURRENT_DATE}'

# 从日志提取所有 TC 子测试的 PASS/FAIL 结果
tc_results = {}  # tc_id -> 'pass' or 'fail'

with open(log_file, 'r') as f:
    for line in f:
        m = re.search(r'--- (PASS|FAIL):.*?/(TC[\d.]+[-a-zA-Z_]+)', line)
        if m:
            tc_results[m.group(2)] = m.group(1).lower()

# 也处理没有 t.Run 的顶层测试 (通过 @test-case 注释匹配)
import glob, os
for tf in glob.glob(os.path.join('${PROJECT_ROOT}/test/e2e/', '*_test.go')):
    with open(tf, 'r') as f:
        content = f.read()
    for m in re.finditer(r'@test-case (TC[\d.]+[-a-zA-Z_]*)', content):
        tc_id = m.group(1)
        if tc_id not in tc_results:
            # 找下一个 func Test
            pos = m.end()
            rest = content[pos:pos+200]
            fm = re.search(r'func (Test\w+)\(', rest)
            if fm:
                test_name = fm.group(1)
                with open(log_file, 'r') as lf:
                    for ll in lf:
                        tcm = re.match(r'--- (PASS|FAIL): ' + test_name + r' ', ll)
                        if tcm:
                            tc_results[tc_id] = tcm.group(1).lower()
                            break

# 更新文档
with open(doc_file, 'r') as f:
    doc_lines = f.readlines()

updated = 0
for i, line in enumerate(doc_lines):
    for tc_id, result in tc_results.items():
        if line.startswith('| ' + tc_id + ' '):
            # 替换行尾的状态标记: | PASS/FAIL hash (date) |
            icon = '✅' if result == 'pass' else '🔴'
            doc_lines[i] = re.sub(
                r'\| [✅🔴] [-a-z0-9]+ \([0-9-]+\) \|$',
                f'| {icon} {new_hash} ({new_date}) |',
                line
            )
            updated += 1
            break

with open(doc_file, 'w') as f:
    f.writelines(doc_lines)

print(f'updated {updated} test cases')
" 2>&1 || info "更新失败（跳过）"

    info "测试用例文档已更新"
fi

# ---- 结果 ----
if [ ${E2E_EXIT_CODE} -eq 0 ]; then
    info "✅ 所有 ${PASS_COUNT} 个测试通过"
else
    warn "❌ ${FAIL_COUNT} 个测试失败，${PASS_COUNT} 个通过"
fi

exit ${E2E_EXIT_CODE}

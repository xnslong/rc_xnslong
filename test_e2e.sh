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

# ---- 结果 ----
if [ ${E2E_EXIT_CODE} -eq 0 ]; then
    info "✅ 所有 ${PASS_COUNT} 个测试通过"
else
    warn "❌ ${FAIL_COUNT} 个测试失败，${PASS_COUNT} 个通过"
fi

exit ${E2E_EXIT_CODE}

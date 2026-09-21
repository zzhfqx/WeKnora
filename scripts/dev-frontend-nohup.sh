#!/bin/bash
# 前端开发服务器后台启动脚本（nohup + PID 文件管理）
# 用法:
#   ./scripts/dev-frontend-nohup.sh start    # 后台启动前端
#   ./scripts/dev-frontend-nohup.sh stop     # 停止前端
#   ./scripts/dev-frontend-nohup.sh restart  # 重启前端
#   ./scripts/dev-frontend-nohup.sh status   # 查看状态
#   ./scripts/dev-frontend-nohup.sh logs     # 实时查看日志

set -e

# 设置颜色
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

# 获取项目根目录
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"
FRONTEND_DIR="$PROJECT_ROOT/frontend"

# PID 文件和日志文件（放在项目根目录）
PID_FILE="$PROJECT_ROOT/pid.frontend"
LOG_FILE="$PROJECT_ROOT/log_frontend.log"

# 前端端口
FRONTEND_PORT="${FRONTEND_PORT:-5173}"

log_info() {
    printf "%b\n" "${BLUE}[INFO]${NC} $1"
}

log_success() {
    printf "%b\n" "${GREEN}[SUCCESS]${NC} $1"
}

log_error() {
    printf "%b\n" "${RED}[ERROR]${NC} $1"
}

log_warning() {
    printf "%b\n" "${YELLOW}[WARNING]${NC} $1"
}

# 检查进程是否在运行
is_running() {
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null)
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            return 0
        fi
    fi
    return 1
}

# 停止进程
stop_frontend() {
    log_info "停止前端开发服务器..."

    if ! is_running; then
        log_warning "前端未在运行"
        rm -f "$PID_FILE"
        # 额外检查端口占用
        if command -v lsof &> /dev/null; then
            local port_pid
            port_pid=$(lsof -ti:"$FRONTEND_PORT" 2>/dev/null || true)
            if [ -n "$port_pid" ]; then
                log_warning "检测到端口 $FRONTEND_PORT 仍被占用 (PID: $port_pid)，正在清理..."
                kill -9 $port_pid 2>/dev/null || true
                sleep 1
            fi
        fi
        return 0
    fi

    local pid
    pid=$(cat "$PID_FILE")
    log_info "正在停止进程 PID: $pid ..."

    # Vite 是 node 子进程，可能需要杀进程组
    # 先尝试优雅停止
    kill "$pid" 2>/dev/null || true

    # 等待进程退出
    local waited=0
    local timeout=10
    while [ "$waited" -lt "$timeout" ]; do
        if ! kill -0 "$pid" 2>/dev/null; then
            break
        fi
        sleep 1
        waited=$((waited + 1))
    done

    # 强制杀死（包括子进程）
    if kill -0 "$pid" 2>/dev/null; then
        log_warning "进程未在 ${timeout}s 内退出，强制杀死..."
        # 杀掉整个进程组
        kill -9 -- -$pid 2>/dev/null || kill -9 "$pid" 2>/dev/null || true
        sleep 1
    fi

    rm -f "$PID_FILE"

    # 再次检查端口占用
    if command -v lsof &> /dev/null; then
        local port_pid
        port_pid=$(lsof -ti:"$FRONTEND_PORT" 2>/dev/null || true)
        if [ -n "$port_pid" ]; then
            log_warning "端口 $FRONTEND_PORT 仍被占用 (PID: $port_pid)，强制清理..."
            kill -9 $port_pid 2>/dev/null || true
        fi
    fi

    log_success "前端开发服务器已停止"
    return 0
}

# 启动前端（后台运行）
start_frontend() {
    log_info "启动前端开发服务器（后台模式）..."

    if is_running; then
        local pid
        pid=$(cat "$PID_FILE")
        log_warning "前端已在运行 (PID: $pid)"
        return 0
    fi

    # 检查端口占用
    if command -v lsof &> /dev/null; then
        local port_pid
        port_pid=$(lsof -ti:"$FRONTEND_PORT" 2>/dev/null || true)
        if [ -n "$port_pid" ]; then
            log_warning "端口 $FRONTEND_PORT 被占用 (PID: $port_pid)，正在清理..."
            kill -9 $port_pid 2>/dev/null || true
            sleep 1
        fi
    fi

    # 检查 npm
    if ! command -v npm &> /dev/null; then
        log_error "npm 未安装"
        return 1
    fi

    # 检查依赖
    if [ ! -d "$FRONTEND_DIR/node_modules" ]; then
        log_warning "node_modules 不存在，正在安装依赖..."
        cd "$FRONTEND_DIR" && npm install
    fi

    # 加载环境变量
    _source_env_file() {
        local src="$1"
        local tmp
        tmp="$(mktemp)" || return 1
        sed -e 's/\r$//' "$src" > "$tmp"
        set -a
        # shellcheck source=/dev/null
        source "$tmp"
        set +a
        rm -f "$tmp"
    }

    cd "$PROJECT_ROOT"
    if [ -f ".env" ]; then
        _source_env_file .env 2>/dev/null || true
    fi
    if [ -f ".env.local" ]; then
        _source_env_file .env.local 2>/dev/null || true
    fi

    log_info "前端将运行在 http://localhost:$FRONTEND_PORT"
    log_info "日志文件: $LOG_FILE"
    log_info "启动中..."

    # 使用 nohup 后台启动
    # 用 setsid 确保脱离终端，避免 SIGHUP
    cd "$FRONTEND_DIR"

    nohup npm run dev > "$LOG_FILE" 2>&1 &

    local bg_pid=$!
    echo "$bg_pid" > "$PID_FILE"

    # 等待一下确认进程没立即退出
    sleep 3

    if ! kill -0 "$bg_pid" 2>/dev/null; then
        log_error "前端启动失败，请查看日志: $LOG_FILE"
        echo "--- 日志尾部 ---"
        tail -30 "$LOG_FILE" 2>/dev/null || true
        rm -f "$PID_FILE"
        return 1
    fi

    log_success "前端开发服务器已后台启动"
    echo ""
    echo "  PID:        $bg_pid"
    echo "  地址:       http://localhost:$FRONTEND_PORT"
    echo "  日志文件:   $LOG_FILE"
    echo "  查看日志:   tail -f $LOG_FILE"
    echo "  停止服务:   $0 stop"
    echo ""

    # 显示启动日志预览
    log_info "启动日志预览:"
    sleep 2
    tail -15 "$LOG_FILE" 2>/dev/null || true

    return 0
}

# 查看状态
show_status() {
    if is_running; then
        local pid
        pid=$(cat "$PID_FILE")
        log_success "前端运行中 (PID: $pid)"
        echo "  日志文件: $LOG_FILE"
        echo "  地址:     http://localhost:$FRONTEND_PORT"
        return 0
    else
        log_warning "前端未运行"
        return 1
    fi
}

# 查看日志
show_logs() {
    if [ -f "$LOG_FILE" ]; then
        tail -f "$LOG_FILE"
    else
        log_warning "日志文件不存在: $LOG_FILE"
        return 1
    fi
}

# 解析命令
CMD="${1:-status}"
case "$CMD" in
    start)
        start_frontend
        ;;
    stop)
        stop_frontend
        ;;
    restart)
        stop_frontend
        sleep 2
        start_frontend
        ;;
    status)
        show_status
        ;;
    logs)
        show_logs
        ;;
    help|--help|-h)
        echo "用法: $0 {start|stop|restart|status|logs}"
        echo ""
        echo "  start    后台启动前端开发服务器"
        echo "  stop     停止前端开发服务器"
        echo "  restart  重启前端开发服务器"
        echo "  status   查看运行状态"
        echo "  logs     实时查看日志"
        ;;
    *)
        log_error "未知命令: $CMD"
        echo "用法: $0 {start|stop|restart|status|logs}"
        exit 1
        ;;
esac

exit $?

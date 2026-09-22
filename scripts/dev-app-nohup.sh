#!/bin/bash
# 后端应用后台启动脚本（nohup + PID 文件管理）
# 用法:
#   ./scripts/dev-app-nohup.sh start    # 后台启动后端
#   ./scripts/dev-app-nohup.sh stop     # 停止后端
#   ./scripts/dev-app-nohup.sh restart  # 重启后端
#   ./scripts/dev-app-nohup.sh status   # 查看状态
#   ./scripts/dev-app-nohup.sh logs     # 实时查看日志

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

# PID 文件和日志文件（放在项目根目录）
PID_FILE="$PROJECT_ROOT/pid.app"
LOG_FILE="$PROJECT_ROOT/log.log"

# 后端端口（开发模式默认 8082，从 config/config.yaml 读取，读不到则用 8082）
# 注意：不使用 .env 中的 APP_PORT，那个是生产模式 Docker 用的
detect_app_port() {
    local config_file="$PROJECT_ROOT/config/config.yaml"
    if [ -f "$config_file" ]; then
        # 从 yaml 中读取 server.port，兼容不同缩进
        local port
        port=$(grep -A5 '^server:' "$config_file" | grep 'port:' | head -1 | awk '{print $2}' | tr -d ' ')
        if [ -n "$port" ] && [ "$port" -eq "$port" ] 2>/dev/null; then
            echo "$port"
            return
        fi
    fi
    echo "8082"
}
APP_PORT="$(detect_app_port)"

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
stop_app() {
    log_info "停止后端应用..."

    if ! is_running; then
        log_warning "后端应用未在运行"
        # 清理残留的 PID 文件
        rm -f "$PID_FILE"
        # 额外检查端口占用
        if command -v lsof &> /dev/null; then
            local port_pid
            port_pid=$(lsof -ti:"$APP_PORT" 2>/dev/null || true)
            if [ -n "$port_pid" ]; then
                log_warning "检测到端口 $APP_PORT 仍被占用 (PID: $port_pid)，正在清理..."
                kill -9 $port_pid 2>/dev/null || true
                sleep 1
            fi
        fi
        return 0
    fi

    local pid
    pid=$(cat "$PID_FILE")
    log_info "正在停止进程 PID: $pid ..."

    # 优雅停止
    kill "$pid" 2>/dev/null || true

    # 等待进程退出
    local waited=0
    local timeout=15
    while [ "$waited" -lt "$timeout" ]; do
        if ! kill -0 "$pid" 2>/dev/null; then
            break
        fi
        sleep 1
        waited=$((waited + 1))
    done

    # 强制杀死
    if kill -0 "$pid" 2>/dev/null; then
        log_warning "进程未在 ${timeout}s 内退出，强制杀死..."
        kill -9 "$pid" 2>/dev/null || true
        sleep 1
    fi

    rm -f "$PID_FILE"

    # 再次检查端口占用（防止有子进程残留）
    if command -v lsof &> /dev/null; then
        local port_pid
        port_pid=$(lsof -ti:"$APP_PORT" 2>/dev/null || true)
        if [ -n "$port_pid" ]; then
            log_warning "端口 $APP_PORT 仍被占用 (PID: $port_pid)，强制清理..."
            kill -9 $port_pid 2>/dev/null || true
        fi
    fi

    log_success "后端应用已停止"
    return 0
}

# 启动后端应用（后台运行）
start_app() {
    log_info "启动后端应用（后台模式）..."

    if is_running; then
        local pid
        pid=$(cat "$PID_FILE")
        log_warning "后端应用已在运行 (PID: $pid)"
        return 0
    fi

    # 检查端口占用
    if command -v lsof &> /dev/null; then
        local port_pid
        port_pid=$(lsof -ti:"$APP_PORT" 2>/dev/null || true)
        if [ -n "$port_pid" ]; then
            log_warning "端口 $APP_PORT 被占用 (PID: $port_pid)，正在清理..."
            kill -9 $port_pid 2>/dev/null || true
            sleep 1
        fi
    fi

    cd "$PROJECT_ROOT"

    # 检查 Go 是否安装
    if ! command -v go &> /dev/null; then
        log_error "Go 未安装"
        return 1
    fi

    # 加载环境变量（复用 dev.sh 中的 load_env_files 逻辑）
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

    load_env_files() {
        if [ -f ".env" ]; then
            _source_env_file .env || return 1
        else
            return 1
        fi
        if [ -f ".env.local" ]; then
            _source_env_file .env.local || return 1
        fi
        return 0
    }

    log_info "加载环境配置..."
    if ! load_env_files; then
        log_error ".env 文件不存在，请先创建配置文件"
        return 1
    fi

    # 本地开发模式：映射基础设施到宿主机
    if [ -n "${DEV_REMOTE_HOST:-}" ]; then
        export DB_HOST="${DEV_REMOTE_HOST}"
        export DB_PORT="65432"
        export REDIS_ADDR="${DEV_REMOTE_HOST}:16379"
        export DOCREADER_ADDR="${DEV_REMOTE_HOST}:15051"
        export MINIO_ENDPOINT="${DEV_REMOTE_HOST}:19000"
        export MILVUS_ADDRESS="${DEV_REMOTE_HOST}:19530"
        export NEO4J_URI="bolt://${DEV_REMOTE_HOST}:17687"
        export QDRANT_HOST="${DEV_REMOTE_HOST}"
        if [ -z "${LANGFUSE_HOST:-}" ] || [ "$LANGFUSE_HOST" = "http://langfuse-web:3000" ] || [ "$LANGFUSE_HOST" = "http://langfuse-web:13000" ]; then
            export LANGFUSE_HOST="http://${DEV_REMOTE_HOST}:13000"
        fi
    else
        export DB_HOST=127.0.0.1
        export DB_PORT=65432
        export REDIS_ADDR=127.0.0.1:16379
        export DOCREADER_ADDR=127.0.0.1:15051
        export MINIO_ENDPOINT=127.0.0.1:19000
        export MILVUS_ADDRESS=127.0.0.1:19530
        export NEO4J_URI=bolt://127.0.0.1:17687
        export QDRANT_HOST=127.0.0.1
    fi
    export DOCREADER_TRANSPORT="${DOCREADER_TRANSPORT:-grpc}"

    # 本地存储目录
    if [ -z "${LOCAL_STORAGE_BASE_DIR:-}" ] || [ "$LOCAL_STORAGE_BASE_DIR" = "/data/files" ]; then
        export LOCAL_STORAGE_BASE_DIR="$PROJECT_ROOT/.local-data/files"
    fi
    mkdir -p "$LOCAL_STORAGE_BASE_DIR"

    if [ -z "$DB_DRIVER" ]; then
        log_error "DB_DRIVER 环境变量未设置，请检查 .env 文件"
        return 1
    fi

    export CGO_CFLAGS="-Wno-deprecated-declarations -Wno-gnu-folding-constant"
    if [[ "$(uname)" == "Darwin" ]]; then
        export CGO_LDFLAGS="-Wl,-no_warn_duplicate_libraries"
    fi

    # anydoc 构建标签检测
    anydoc_host_archive() {
        case "$(uname -s)-$(uname -m)" in
            Darwin-arm64) echo "$PROJECT_ROOT/third_party/anydoc-go/lib/darwin_arm64/libanydoc_go.a" ;;
            Darwin-x86_64) echo "$PROJECT_ROOT/third_party/anydoc-go/lib/darwin_amd64/libanydoc_go.a" ;;
            Linux-x86_64)
                if [ -f "$PROJECT_ROOT/third_party/anydoc-go/lib/linux_amd64_gnu/libanydoc_go.a" ]; then
                    echo "$PROJECT_ROOT/third_party/anydoc-go/lib/linux_amd64_gnu/libanydoc_go.a"
                else
                    echo "$PROJECT_ROOT/third_party/anydoc-go/lib/linux_amd64_musl/libanydoc_go.a"
                fi
                ;;
            Linux-aarch64)
                if [ -f "$PROJECT_ROOT/third_party/anydoc-go/lib/linux_arm64_gnu/libanydoc_go.a" ]; then
                    echo "$PROJECT_ROOT/third_party/anydoc-go/lib/linux_arm64_gnu/libanydoc_go.a"
                else
                    echo "$PROJECT_ROOT/third_party/anydoc-go/lib/linux_arm64_musl/libanydoc_go.a"
                fi
                ;;
            *) echo "" ;;
        esac
    }

    if [ -z "${GO_BUILD_TAGS+x}" ]; then
        archive="$(anydoc_host_archive)"
        if [ -n "$archive" ] && [ -f "$archive" ]; then
            export GO_BUILD_TAGS=anydoc
            log_info "检测到 anydoc 静态库，已启用 -tags anydoc"
        else
            log_info "未检测到 anydoc 静态库，解析引擎不可用"
        fi
    fi

    # 构建启动命令
    LDFLAGS="$(./scripts/get_version.sh ldflags) -X 'google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn'"

    log_info "数据库地址: $DB_HOST:${DB_PORT:-5432}"
    log_info "日志文件: $LOG_FILE"
    log_info "启动中..."

    # 使用 nohup 后台启动
    # 注意：go run 会启动子进程，需要用 exec 替换当前进程，或者直接构建二进制后运行
    # 为了 PID 准确，这里先构建再运行
    # 但为了保持和 dev-app 一致的体验（自动检测 air），我们用 nohup go run
    # air 的热重载在 nohup 下也能工作

    nohup bash -c "
        cd '$PROJECT_ROOT'
        export CGO_CFLAGS='$CGO_CFLAGS'
        export CGO_LDFLAGS='$CGO_LDFLAGS'
        export GO_BUILD_TAGS='${GO_BUILD_TAGS:-}'
        export DB_HOST='$DB_HOST'
        export DB_PORT='$DB_PORT'
        export REDIS_ADDR='$REDIS_ADDR'
        export DOCREADER_ADDR='$DOCREADER_ADDR'
        export DOCREADER_TRANSPORT='$DOCREADER_TRANSPORT'
        export MINIO_ENDPOINT='$MINIO_ENDPOINT'
        export MILVUS_ADDRESS='$MILVUS_ADDRESS'
        export NEO4J_URI='$NEO4J_URI'
        export QDRANT_HOST='$QDRANT_HOST'
        export LANGFUSE_HOST='${LANGFUSE_HOST:-}'
        export LOCAL_STORAGE_BASE_DIR='$LOCAL_STORAGE_BASE_DIR'
        export DB_DRIVER='$DB_DRIVER'
        # 重新加载所有 env 变量（防止上面漏传）
        if [ -f .env ]; then
            set -a
            while IFS= read -r line || [[ -n \"\$line\" ]]; do
                line=\$(echo \"\$line\" | sed 's/\r$//')
                [[ -z \"\$line\" || \"\$line\" == \\#* ]] && continue
                [[ \"\$line\" == *=* ]] || continue
                key=\"\${line%%=*}\"
                # 只导出还没设置的
                [[ -z \"\${!key}\" ]] && export \"\$line\"
            done < .env
            set +a
        fi
        if [ -f .env.local ]; then
            set -a
            while IFS= read -r line || [[ -n \"\$line\" ]]; do
                line=\$(echo \"\$line\" | sed 's/\r$//')
                [[ -z \"\$line\" || \"\$line\" == \\#* ]] && continue
                [[ \"\$line\" == *=* ]] || continue
                export \"\$line\"
            done < .env.local
            set +a
        fi
        exec go run -tags \"\${GO_BUILD_TAGS:-}\" -ldflags=\"$LDFLAGS\" ./cmd/server
    " > "$LOG_FILE" 2>&1 &

    local bg_pid=$!
    echo "$bg_pid" > "$PID_FILE"

    # 等待一下确认进程没立即退出
    sleep 2

    if ! kill -0 "$bg_pid" 2>/dev/null; then
        log_error "后端应用启动失败，请查看日志: $LOG_FILE"
        echo "--- 日志尾部 ---"
        tail -30 "$LOG_FILE" 2>/dev/null || true
        rm -f "$PID_FILE"
        return 1
    fi

    log_success "后端应用已后台启动"
    echo ""
    echo "  PID:        $bg_pid"
    echo "  端口:       $APP_PORT"
    echo "  日志文件:   $LOG_FILE"
    echo "  查看日志:   tail -f $LOG_FILE"
    echo "  停止服务:   $0 stop"
    echo ""

    # 显示前几秒的启动日志
    log_info "启动日志预览:"
    sleep 2
    tail -20 "$LOG_FILE" 2>/dev/null || true

    return 0
}

# 查看状态
show_status() {
    if is_running; then
        local pid
        pid=$(cat "$PID_FILE")
        log_success "后端应用运行中 (PID: $pid)"
        echo "  日志文件: $LOG_FILE"
        echo "  端口:     $APP_PORT"
        return 0
    else
        log_warning "后端应用未运行"
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
        start_app
        ;;
    stop)
        stop_app
        ;;
    restart)
        stop_app
        sleep 2
        start_app
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
        echo "  start    后台启动后端应用"
        echo "  stop     停止后端应用"
        echo "  restart  重启后端应用"
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

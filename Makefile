.PHONY: help build run test clean docker-build-app docker-build-docreader docker-build-frontend docker-build-all docker-run migrate-up migrate-down docker-restart docker-stop start-all stop-all start-ollama stop-ollama build-images build-images-app build-images-docreader build-images-frontend clean-images check-env list-containers pull-images show-platform dev-start dev-stop dev-restart dev-logs dev-status dev-app dev-frontend docs install-swagger build-lite run-lite package-lite anydoc-lib build-anydoc

# Show help
help:
	@echo "WeKnora Makefile 帮助"
	@echo ""
	@echo "基础命令:"
	@echo "  build             构建应用"
	@echo "  run               运行应用"
	@echo "  test              运行测试"
	@echo "  anydoc-lib        构建 anydoc 静态库（需要 Rust 工具链）"
	@echo "  build-anydoc      构建带 anydoc 解析引擎的应用"
	@echo "  clean             清理构建文件"
	@echo ""
	@echo "Docker 命令:"
	@echo "  docker-build-app       构建应用 Docker 镜像 (wechatopenai/weknora-app)"
	@echo "  docker-build-docreader 构建文档读取器镜像 (wechatopenai/weknora-docreader)"
	@echo "  docker-build-frontend  构建前端镜像 (wechatopenai/weknora-ui)"
	@echo "  docker-build-all       构建所有 Docker 镜像"
	@echo "  docker-run            运行 Docker 容器"
	@echo "  docker-stop           停止 Docker 容器"
	@echo "  docker-restart        重启 Docker 容器"
	@echo ""
	@echo "服务管理:"
	@echo "  start-all         启动所有服务"
	@echo "  stop-all          停止所有服务"
	@echo "  start-ollama      仅启动 Ollama 服务"
	@echo ""
	@echo "镜像构建:"
	@echo "  build-images      从源码构建所有镜像"
	@echo "  build-images-app  从源码构建应用镜像"
	@echo "  build-images-docreader 从源码构建文档读取器镜像"
	@echo "  build-images-frontend  从源码构建前端镜像"
	@echo "  clean-images      清理本地镜像"
	@echo ""
	@echo "数据库:"
	@echo "  migrate-up        执行数据库迁移"
	@echo "  migrate-down      回滚数据库迁移"
	@echo ""
	@echo "开发工具:"
	@echo "  fmt               格式化代码"
	@echo "  lint              代码检查"
	@echo "  deps              安装依赖"
	@echo "  docs              生成 Swagger API 文档"
	@echo "  install-swagger   安装 swag 工具"
	@echo ""
	@echo "环境检查:"
	@echo "  check-env         检查环境配置"
	@echo "  list-containers   列出运行中的容器"
	@echo "  pull-images       拉取最新镜像"
	@echo "  show-platform     显示当前构建平台"
	@echo ""
	@echo "开发模式（推荐）:"
	@echo "  dev-start         启动开发环境基础设施（仅启动依赖服务）"
	@echo "                    可选: make dev-start DEV_ARGS=--odl-hybrid"
	@echo "  dev-stop          停止开发环境"
	@echo "  dev-restart       重启开发环境"
	@echo "  dev-logs          查看开发环境日志"
	@echo "  dev-status        查看开发环境状态"
	@echo "  dev-app           启动后端应用（本地运行，需先运行 dev-start）"
	@echo "                    已 make anydoc-lib 时自动链接 anydoc 引擎"
	@echo "  dev-frontend      启动前端（本地运行，需先运行 dev-start）"
	@echo ""
	@echo "开发模式 - 后台运行（nohup，关闭终端不失效）:"
	@echo "  dev-bg            一键启动基础设施 + 后端 + 前端（全部后台）"
	@echo "  dev-bg-stop       一键停止全部后台服务"
	@echo "  dev-app-bg        后台启动后端（日志: log.log）"
	@echo "  dev-app-bg-stop   停止后端"
	@echo "  dev-app-bg-restart 重启后端"
	@echo "  dev-app-bg-status  查看后端状态"
	@echo "  dev-app-bg-logs    实时查看后端日志"
	@echo "  dev-frontend-bg        后台启动前端（日志: log_frontend.log）"
	@echo "  dev-frontend-bg-stop   停止前端"
	@echo "  dev-frontend-bg-restart 重启前端"
	@echo "  dev-frontend-bg-status  查看前端状态"
	@echo "  dev-frontend-bg-logs    实时查看前端日志"
	@echo ""
	@echo "调试版（fqx专用，不启基础设施，共用pg-zzh的基础设施）:"
	@echo "  dev-fqx            一键启动调试版前后端（后台运行）"
	@echo "  dev-fqx-stop       停止调试版前后端"
	@echo "  dev-fqx-restart    重启调试版前后端"
	@echo "  dev-fqx-status     查看调试版状态"
	@echo "  dev-fqx-logs       查看调试版日志（后端+前端各最后20行）"
	@echo "  dev-fqx-logs-app   实时查看后端日志"
	@echo "  dev-fqx-logs-frontend  实时查看前端日志"
	@echo ""
	@echo "Lite 模式（零外部依赖）:"
	@echo "  build-lite        构建 Lite 版本（先构建前端到 web/，再构建 Go；SKIP_FRONTEND=1 跳过前端）"
	@echo "  run-lite          构建并启动 Lite 版本"
	@echo "  package-lite      构建并打包 Lite 发行包（tarball）"
	@echo "  package-mac-app   构建并打包 macOS 桌面应用 (.app)"

# Go related variables
BINARY_NAME=WeKnora
MAIN_PATH=./cmd/server

# Docker related variables
DOCKER_IMAGE=wechatopenai/weknora-app
DOCKER_TAG=latest

# Platform detection
ifeq ($(shell uname -m),x86_64)
    PLATFORM=linux/amd64
else ifeq ($(shell uname -m),aarch64)
    PLATFORM=linux/arm64
else ifeq ($(shell uname -m),arm64)
    PLATFORM=linux/arm64
else
    PLATFORM=linux/amd64
endif

# Build the application
build:
	go build -o $(BINARY_NAME) $(MAIN_PATH)

# Build the anydoc static archive (Rust) that the `anydoc` build tag links.
# Override the platform with TARGET=<rust-target-triple>.
anydoc-lib:
	./scripts/build-anydoc-lib.sh

# Build the application with the in-process anydoc parser engine linked in.
build-anydoc: anydoc-lib
	go build -tags anydoc -o $(BINARY_NAME) $(MAIN_PATH)

# Run the application
run: build
	./$(BINARY_NAME)

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	go clean
	rm -f $(BINARY_NAME)

# Build Docker image
docker-build-app:
	@echo "获取版本信息..."
	@eval $$(./scripts/get_version.sh env); \
	./scripts/get_version.sh info; \
	docker build --platform $(PLATFORM) \
		--build-arg VERSION_ARG="$$VERSION" \
		--build-arg COMMIT_ID_ARG="$$COMMIT_ID" \
		--build-arg BUILD_TIME_ARG="$$BUILD_TIME" \
		--build-arg GO_VERSION_ARG="$$GO_VERSION" \
		--build-arg WITH_ANYDOC=$${WITH_ANYDOC:-1} \
		-f docker/Dockerfile.app -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Build docreader Docker image
docker-build-docreader:
	docker build --platform $(PLATFORM) -f docker/Dockerfile.docreader -t wechatopenai/weknora-docreader:latest .

# Build frontend Docker image (multi-stage: npm runs inside the builder stage)
docker-build-frontend:
	@eval $$(./scripts/get_version.sh env); \
	docker build --platform $(PLATFORM) \
		--build-arg VITE_FRONTEND_COMMIT="$$COMMIT_ID" \
		-f frontend/Dockerfile -t wechatopenai/weknora-ui:latest frontend/

# Build all Docker images
docker-build-all: docker-build-app docker-build-docreader docker-build-frontend

# Run Docker container (传统方式)
# Touch .env if missing — docker-compose.yml's `env_file: [.env]` is required
# for ${ENV} interpolation in builtin_models.yaml and would otherwise refuse
# to parse on fresh clones. `start-all` handles this via check_env_file; this
# direct path needs its own guard.
docker-run:
	@[ -f .env ] || ([ -f .env.example ] && cp .env.example .env || touch .env)
	docker-compose up

# 使用新脚本启动所有服务
start-all:
	./scripts/start_all.sh

# 使用新脚本仅启动Ollama服务
start-ollama:
	./scripts/start_all.sh --ollama

# 使用新脚本仅启动Docker容器
start-docker:
	./scripts/start_all.sh --docker

# 使用新脚本停止所有服务
stop-all:
	./scripts/start_all.sh --stop

# Stop Docker container (传统方式)
docker-stop:
	docker-compose down

# 从源码构建镜像相关命令
build-images:
	./scripts/build_images.sh

build-images-app:
	./scripts/build_images.sh --app

build-images-docreader:
	./scripts/build_images.sh --docreader

build-images-frontend:
	./scripts/build_images.sh --frontend

clean-images:
	./scripts/build_images.sh --clean

# Restart Docker container (stop, start)
docker-restart:
	@[ -f .env ] || ([ -f .env.example ] && cp .env.example .env || touch .env)
	docker-compose stop -t 60
	docker-compose up

# Database migrations
migrate-up:
	./scripts/migrate.sh up

migrate-down:
	./scripts/migrate.sh down

migrate-version:
	./scripts/migrate.sh version

migrate-create:
	@if [ -z "$(name)" ]; then \
		echo "Error: migration name is required"; \
		echo "Usage: make migrate-create name=your_migration_name"; \
		exit 1; \
	fi
	./scripts/migrate.sh create $(name)

migrate-force:
	@if [ -z "$(version)" ]; then \
		echo "Error: version is required"; \
		echo "Usage: make migrate-force version=4"; \
		exit 1; \
	fi
	./scripts/migrate.sh force $(version)

migrate-goto:
	@if [ -z "$(version)" ]; then \
		echo "Error: version is required"; \
		echo "Usage: make migrate-goto version=3"; \
		exit 1; \
	fi
	./scripts/migrate.sh goto $(version)

# Generate API documentation (Swagger)
docs:
	@echo "生成 Swagger API 文档..."
	swag init -g $(MAIN_PATH)/main.go -o ./docs --parseDependency --parseInternal
	@echo "文档已生成到 ./docs 目录"
	@echo "启动服务后访问 http://localhost:8080/swagger/index.html 查看文档"

# Install swagger tool
install-swagger:
	go install github.com/swaggo/swag/cmd/swag@latest

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golangci-lint run

# Install dependencies
deps:
	go mod download

# Build for production
# google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn for qdrant milvus proto conflict
# GO_BUILD_TAGS adds optional build tags, e.g. GO_BUILD_TAGS=anydoc to link the
# in-process office document parser (run `make anydoc-lib` first).
build-prod:
	VERSION=$$(git describe --tags --abbrev=0 2>/dev/null || echo "$${VERSION:-unknown}"); \
	COMMIT_ID=$${COMMIT_ID:-unknown}; \
	CGO_ENABLED=1 \
	CGO_CFLAGS="-Wno-deprecated-declarations" \
	CGO_LDFLAGS="$$(if [ "$$(uname)" = 'Darwin' ]; then echo '-Wl,-no_warn_duplicate_libraries'; fi)" \
	BUILD_TIME=$${BUILD_TIME:-unknown}; \
	GO_VERSION=$${GO_VERSION:-unknown}; \
	LDFLAGS="-X 'github.com/Tencent/WeKnora/internal/handler.Version=$$VERSION' -X 'github.com/Tencent/WeKnora/internal/handler.Edition=standard' -X 'github.com/Tencent/WeKnora/internal/handler.CommitID=$$COMMIT_ID' -X 'github.com/Tencent/WeKnora/internal/handler.BuildTime=$$BUILD_TIME' -X 'github.com/Tencent/WeKnora/internal/handler.GoVersion=$$GO_VERSION' -X 'google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn'"; \
	go build -tags "$(GO_BUILD_TAGS)" -ldflags="-w -s $$LDFLAGS" -o $(BINARY_NAME) $(MAIN_PATH)

# Build Lite version (single binary, SQLite + in-memory queue)
# 会先构建前端到 web/，再构建 Go 二进制；SKIP_FRONTEND=1 可跳过前端
build-lite:
	@if [ -f frontend/package.json ] && [ "$${SKIP_FRONTEND:-}" != "1" ]; then \
		echo ">> Building frontend for Lite..."; \
		(cd frontend && npm ci --prefer-offline && npm run build) && \
		rm -rf web && cp -r frontend/dist web; \
	elif [ "$${SKIP_FRONTEND:-}" = "1" ]; then \
		echo ">> Skipping frontend (SKIP_FRONTEND=1)"; \
	else \
		echo ">> No frontend/package.json, skipping frontend"; \
	fi
	export EDITION=lite; \
	eval "$$(./scripts/get_version.sh env)"; \
	LDFLAGS="$$(./scripts/get_version.sh ldflags) -X 'google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn'"; \
	CGO_ENABLED=1 \
	CGO_CFLAGS="-Wno-deprecated-declarations" \
	CGO_LDFLAGS="$$(if [ "$$(uname)" = 'Darwin' ]; then echo '-Wl,-no_warn_duplicate_libraries'; fi)" \
	go build -tags "sqlite_fts5" -ldflags="-w -s $$LDFLAGS" -o $(BINARY_NAME)-lite $(MAIN_PATH)

# Run Lite version with .env.lite defaults
run-lite: build-lite
	@if [ ! -f .env.lite ]; then echo "Error: .env.lite not found"; exit 1; fi
	@set -a && . ./.env.lite && set +a && ./$(BINARY_NAME)-lite

# Package Lite version into distributable tarball
package-lite:
	./scripts/package-lite.sh

# Package Mac App
package-mac-app:
	./scripts/package-mac-app.sh

download_spatial:
	go run cmd/download/duckdb/duckdb.go

clean-db:
	@echo "Cleaning database..."
	@if [ $$(docker volume ls -q -f name=weknora_postgres-data) ]; then \
		docker volume rm weknora_postgres-data; \
	fi
	@if [ $$(docker volume ls -q -f name=weknora_minio_data) ]; then \
		docker volume rm weknora_minio_data; \
	fi
	@if [ $$(docker volume ls -q -f name=weknora_redis_data) ]; then \
		docker volume rm weknora_redis_data; \
	fi

# Environment check
check-env:
	./scripts/start_all.sh --check

# List containers
list-containers:
	./scripts/start_all.sh --list

# Pull latest images
pull-images:
	./scripts/start_all.sh --pull

# Show current platform
show-platform:
	@echo "当前系统架构: $(shell uname -m)"
	@echo "Docker构建平台: $(PLATFORM)"

# Development mode commands
dev-start:
	./scripts/dev.sh start $(DEV_ARGS)

dev-stop:
	./scripts/dev.sh stop

dev-restart:
	./scripts/dev.sh restart

dev-logs:
	./scripts/dev.sh logs

dev-status:
	./scripts/dev.sh status

dev-app:
	./scripts/dev.sh app

dev-frontend:
	./scripts/dev.sh frontend

# 后台运行模式（nohup + PID 文件）
dev-app-bg:
	./scripts/dev-app-nohup.sh start

dev-app-bg-stop:
	./scripts/dev-app-nohup.sh stop

dev-app-bg-restart:
	./scripts/dev-app-nohup.sh restart

dev-app-bg-status:
	./scripts/dev-app-nohup.sh status

dev-app-bg-logs:
	./scripts/dev-app-nohup.sh logs

dev-frontend-bg:
	./scripts/dev-frontend-nohup.sh start

dev-frontend-bg-stop:
	./scripts/dev-frontend-nohup.sh stop

dev-frontend-bg-restart:
	./scripts/dev-frontend-nohup.sh restart

dev-frontend-bg-status:
	./scripts/dev-frontend-nohup.sh status

dev-frontend-bg-logs:
	./scripts/dev-frontend-nohup.sh logs

dev-bg: dev-start
	@echo "启动基础设施完成，正在后台启动前后端..."
	./scripts/dev-app-nohup.sh start
	@echo ""
	./scripts/dev-frontend-nohup.sh start
	@echo ""
	@echo "=================================================="
	@echo "  全部服务已后台启动完成"
	@echo "  后端: http://localhost:8082"
	@echo "  前端: http://localhost:5173"
	@echo "  后端日志: tail -f log.log"
	@echo "  前端日志: tail -f log_frontend.log"
	@echo "  停止: make dev-bg-stop"
	@echo "=================================================="

dev-bg-stop:
	./scripts/dev-frontend-nohup.sh stop
	./scripts/dev-app-nohup.sh stop
	./scripts/dev.sh stop

# 调试版专用：只启动前后端（不启动基础设施，共用 pg-zzh 的基础设施）
dev-fqx:
	@echo "=================================================="
	@echo "  启动调试版前后端（共用基础设施）"
	@echo "  后端: http://localhost:8083"
	@echo "  前端: http://localhost:5174"
	@echo "=================================================="
	@echo ""
	@echo "正在启动后端..."
	./scripts/dev-app-nohup.sh start
	@echo ""
	@echo "正在启动前端..."
	./scripts/dev-frontend-nohup.sh start
	@echo ""
	@echo "=================================================="
	@echo "  调试版已启动"
	@echo "  后端: http://localhost:8083"
	@echo "  前端: http://localhost:5174"
	@echo "  后端日志: tail -f log.log"
	@echo "  前端日志: tail -f log_frontend.log"
	@echo "  停止: make dev-fqx-stop"
	@echo "=================================================="

dev-fqx-stop:
	@echo "正在停止前端..."
	./scripts/dev-frontend-nohup.sh stop
	@echo ""
	@echo "正在停止后端..."
	./scripts/dev-app-nohup.sh stop
	@echo ""
	@echo "调试版已停止（基础设施未停止，仍在运行）"

dev-fqx-restart:
	@echo "正在重启调试版前后端..."
	./scripts/dev-frontend-nohup.sh restart
	./scripts/dev-app-nohup.sh restart
	@echo "调试版前后端已重启"

dev-fqx-status:
	@echo "=== 调试版状态 ==="
	./scripts/dev-app-nohup.sh status
	./scripts/dev-frontend-nohup.sh status

dev-fqx-logs:
	@echo "=============================="
	@echo "  调试版日志"
	@echo "=============================="
	@echo ""
	@echo "--- 后端日志 (最后 20 行) ---"
	@if [ -f log.log ]; then tail -n 20 log.log; else echo "  (日志文件不存在，后端可能未启动)"; fi
	@echo ""
	@echo "--- 前端日志 (最后 20 行) ---"
	@if [ -f log_frontend.log ]; then tail -n 20 log_frontend.log; else echo "  (日志文件不存在，前端可能未启动)"; fi
	@echo ""
	@echo "实时查看日志:"
	@echo "  后端: tail -f log.log"
	@echo "  前端: tail -f log_frontend.log"

dev-fqx-logs-app:
	@echo "--- 后端日志 (实时) ---"
	tail -f log.log

dev-fqx-logs-frontend:
	@echo "--- 前端日志 (实时) ---"
	tail -f log_frontend.log



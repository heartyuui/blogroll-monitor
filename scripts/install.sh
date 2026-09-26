#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd -P)"
ENV_FILE="${PROJECT_ROOT}/.env"
BACKEND_ENV_FILE="${PROJECT_ROOT}/.backend-env"
COMPOSE_FILE="${PROJECT_ROOT}/docker-compose.yml"

RECONFIGURE=0
PREPARE_ONLY=0
NON_INTERACTIVE=0
HEALTH_TIMEOUT_SECONDS="${INSTALL_HEALTH_TIMEOUT_SECONDS:-120}"
TEMP_FILES=()

usage() {
  cat <<'EOF'
用法：./scripts/install.sh [选项]

为已克隆的 friend-link-monitor 仓库生成配置，并使用 Docker Compose 启动服务。
脚本不会安装 Docker，也不会修改博客后端。

选项：
  --prepare-only     只生成并校验配置，不构建或启动容器
  --reconfigure      备份现有配置后重新生成；默认不会覆盖现有 .env
  --non-interactive  不进行交互；BLOG_BASE_URL 必须通过环境变量提供
  -h, --help         显示帮助

可传入的环境变量：
  BLOG_BASE_URL
  MONITOR_NODE_ID
  MONITOR_HMAC_KEY_ID
  MONITOR_HMAC_SECRET
  MONITOR_HOST_PORT
  INSTALL_HEALTH_TIMEOUT_SECONDS

示例：
  ./scripts/install.sh
  ./scripts/install.sh --prepare-only
  BLOG_BASE_URL=https://example.com ./scripts/install.sh --non-interactive
EOF
}

log() {
  printf '[install] %s\n' "$*"
}

warn() {
  printf '[install] 警告：%s\n' "$*" >&2
}

die() {
  printf '[install] 错误：%s\n' "$*" >&2
  exit 1
}

trap 'if ((${#TEMP_FILES[@]})); then rm -f -- "${TEMP_FILES[@]}"; fi' EXIT

while (($# > 0)); do
  case "$1" in
    --prepare-only)
      PREPARE_ONLY=1
      ;;
    --reconfigure)
      RECONFIGURE=1
      ;;
    --non-interactive)
      NON_INTERACTIVE=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "未知选项：$1（使用 --help 查看帮助）"
      ;;
  esac
  shift
done

command -v docker >/dev/null 2>&1 || die "未找到 Docker CLI。请先安装 Docker Engine 与 Compose 插件。"
docker compose version >/dev/null 2>&1 || die "未找到 docker compose 插件。"
[[ -f "${COMPOSE_FILE}" ]] || die "缺少 ${COMPOSE_FILE}"
[[ "${HEALTH_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || die "INSTALL_HEALTH_TIMEOUT_SECONDS 必须是整数"
((HEALTH_TIMEOUT_SECONDS >= 10 && HEALTH_TIMEOUT_SECONDS <= 3600)) ||
  die "INSTALL_HEALTH_TIMEOUT_SECONDS 必须在 10 到 3600 之间"

env_value() {
  local key="$1"
  local file="$2"
  local value

  value="$(
    awk -v wanted="${key}" '
      $0 ~ "^[[:space:]]*" wanted "[[:space:]]*=" {
        sub("^[[:space:]]*" wanted "[[:space:]]*=[[:space:]]*", "")
        sub("\r$", "")
        print
        exit
      }
    ' "${file}"
  )"

  if [[ "${value}" == \"*\" && "${value}" == *\" ]]; then
    value="${value:1:${#value}-2}"
  elif [[ "${value}" == \'*\' && "${value}" == *\' ]]; then
    value="${value:1:${#value}-2}"
  fi
  printf '%s' "${value}"
}

prompt_required() {
  local variable_name="$1"
  local prompt_text="$2"
  local default_value="${3:-}"
  local value="${!variable_name:-}"

  if [[ -n "${value}" ]]; then
    return
  fi

  if ((NON_INTERACTIVE)); then
    [[ -n "${default_value}" ]] || die "${variable_name} 未设置，非交互模式无法继续"
    printf -v "${variable_name}" '%s' "${default_value}"
    return
  fi

  [[ -t 0 ]] || die "当前终端不可交互；请传入配置并使用 --non-interactive"
  while [[ -z "${value}" ]]; do
    if [[ -n "${default_value}" ]]; then
      read -r -p "${prompt_text} [${default_value}]: " value
      value="${value:-${default_value}}"
    else
      read -r -p "${prompt_text}: " value
    fi
  done
  printf -v "${variable_name}" '%s' "${value}"
}

validate_configuration() {
  local environment="$1"
  local allow_insecure_local="$2"

  [[ "${BLOG_BASE_URL}" =~ ^https://[^[:space:]#]+$ ]] ||
    die "BLOG_BASE_URL 必须是完整的 HTTPS 地址，且不能包含空格或 #"
  [[ "${MONITOR_NODE_ID}" =~ ^[A-Za-z0-9._-]+$ ]] ||
    die "MONITOR_NODE_ID 只能包含字母、数字、点、下划线和连字符"
  [[ "${MONITOR_HMAC_KEY_ID}" =~ ^[A-Za-z0-9._-]+$ ]] ||
    die "MONITOR_HMAC_KEY_ID 只能包含字母、数字、点、下划线和连字符"
  [[ "${MONITOR_HMAC_SECRET}" =~ ^[A-Za-z0-9_-]+$ ]] ||
    die "MONITOR_HMAC_SECRET 必须是无填充的 base64url"
  ((${#MONITOR_HMAC_SECRET} >= 43)) ||
    die "MONITOR_HMAC_SECRET 必须至少编码 32 个随机字节"
  [[ "${MONITOR_HOST_PORT}" =~ ^[0-9]+$ ]] ||
    die "MONITOR_HOST_PORT 必须是 1 到 65535 的整数"
  ((MONITOR_HOST_PORT >= 1 && MONITOR_HOST_PORT <= 65535)) ||
    die "MONITOR_HOST_PORT 必须是 1 到 65535 的整数"

  if [[ "${environment}" == "production" && "${allow_insecure_local}" == "true" ]]; then
    die "production 环境不能启用 BLOG_ALLOW_INSECURE_LOCAL"
  fi
}

generate_secret() {
  local secret

  if command -v openssl >/dev/null 2>&1; then
    secret="$(openssl rand -base64 32 | tr -d '\r\n=' | tr '+/' '-_')"
  elif command -v dd >/dev/null 2>&1 && command -v base64 >/dev/null 2>&1; then
    secret="$(dd if=/dev/urandom bs=32 count=1 status=none | base64 | tr -d '\r\n=' | tr '+/' '-_')"
  else
    die "无法生成安全密钥；请安装 openssl，或设置 MONITOR_HMAC_SECRET"
  fi

  printf '%s' "${secret}"
}

backup_file() {
  local file="$1"
  local timestamp="$2"
  local backup

  [[ -f "${file}" ]] || return
  backup="${file}.backup.${timestamp}"
  cp -p -- "${file}" "${backup}"
  chmod 600 "${backup}"
  log "已备份 $(basename -- "${file}") 到 $(basename -- "${backup}")"
}

write_monitor_env() {
  local temp_file
  temp_file="$(mktemp "${PROJECT_ROOT}/.env.tmp.XXXXXX")"
  TEMP_FILES+=("${temp_file}")

  cat >"${temp_file}" <<EOF
MONITOR_ENV=production
MONITOR_LISTEN_ADDRESS=0.0.0.0:8080
MONITOR_HOST_PORT=${MONITOR_HOST_PORT}
MONITOR_DATABASE_PATH=/data/friend-link-monitor.db

BLOG_BASE_URL=${BLOG_BASE_URL%/}
BLOG_ALLOW_INSECURE_LOCAL=false
MONITOR_NODE_ID=${MONITOR_NODE_ID}
MONITOR_HMAC_KEY_ID=${MONITOR_HMAC_KEY_ID}
MONITOR_HMAC_SECRET=${MONITOR_HMAC_SECRET}

MONITOR_SYNC_INTERVAL=1m
MONITOR_CHECK_INTERVAL=5m
MONITOR_SCHEDULER_POLL_INTERVAL=1s
MONITOR_GLOBAL_CONCURRENCY=32
MONITOR_PER_DOMAIN_CONCURRENCY=1
MONITOR_SUCCESS_THRESHOLD=2
MONITOR_FAILURE_THRESHOLD=3
MONITOR_MAX_REDIRECTS=5
MONITOR_TARGET_RETRIES=1

MONITOR_DNS_TIMEOUT=2s
MONITOR_CONNECT_TIMEOUT=3s
MONITOR_TLS_TIMEOUT=3s
MONITOR_RESPONSE_HEADER_TIMEOUT=5s
MONITOR_READ_TIMEOUT=3s
MONITOR_TOTAL_TIMEOUT=10s
MONITOR_MAX_RESPONSE_BYTES=65536
MONITOR_ALLOW_HTTPS_DOWNGRADE=false

MONITOR_RAW_RETENTION=168h
MONITOR_HOURLY_RETENTION=4320h
MONITOR_DAILY_RETENTION=17520h

MONITOR_TEST_ALLOW_LOOPBACK=false
EOF

  chmod 600 "${temp_file}"
  mv -f -- "${temp_file}" "${ENV_FILE}"
}

write_backend_env() {
  local temp_file
  temp_file="$(mktemp "${PROJECT_ROOT}/.backend-env.tmp.XXXXXX")"
  TEMP_FILES+=("${temp_file}")

  cat >"${temp_file}" <<EOF
FRIEND_LINK_MONITOR_HMAC_KEYS=${MONITOR_HMAC_KEY_ID}:${MONITOR_HMAC_SECRET}
FRIEND_LINK_MONITOR_CLOCK_SKEW_SECONDS=300
FRIEND_LINK_STATUS_STALE_SECONDS=1200
FRIEND_LINK_PUBLIC_CACHE_SECONDS=15
EOF

  chmod 600 "${temp_file}"
  mv -f -- "${temp_file}" "${BACKEND_ENV_FILE}"
}

compose() {
  docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_FILE}" "$@"
}

new_configuration=0
backend_configuration_written=0

if [[ -f "${ENV_FILE}" && ${RECONFIGURE} -eq 0 ]]; then
  log "检测到现有 .env，将直接复用；如需重建请使用 --reconfigure"
  BLOG_BASE_URL="$(env_value BLOG_BASE_URL "${ENV_FILE}")"
  MONITOR_NODE_ID="$(env_value MONITOR_NODE_ID "${ENV_FILE}")"
  MONITOR_HMAC_KEY_ID="$(env_value MONITOR_HMAC_KEY_ID "${ENV_FILE}")"
  MONITOR_HMAC_SECRET="$(env_value MONITOR_HMAC_SECRET "${ENV_FILE}")"
  MONITOR_HOST_PORT="$(env_value MONITOR_HOST_PORT "${ENV_FILE}")"
  MONITOR_HOST_PORT="${MONITOR_HOST_PORT:-8080}"
  monitor_environment="$(env_value MONITOR_ENV "${ENV_FILE}")"
  monitor_environment="${monitor_environment:-production}"
  allow_insecure_local="$(env_value BLOG_ALLOW_INSECURE_LOCAL "${ENV_FILE}")"
  allow_insecure_local="${allow_insecure_local:-false}"

  if [[ "${monitor_environment}" == "production" ]]; then
    validate_configuration "${monitor_environment}" "${allow_insecure_local}"
  else
    [[ -n "${BLOG_BASE_URL}" && -n "${MONITOR_NODE_ID}" && -n "${MONITOR_HMAC_KEY_ID}" &&
      -n "${MONITOR_HMAC_SECRET}" ]] || die "现有 .env 缺少必要配置"
    if [[ ! "${MONITOR_HOST_PORT}" =~ ^[0-9]+$ ]] ||
      ((MONITOR_HOST_PORT < 1 || MONITOR_HOST_PORT > 65535)); then
      die "MONITOR_HOST_PORT 必须是 1 到 65535 的整数"
    fi
  fi

  if [[ ! -f "${BACKEND_ENV_FILE}" ]]; then
    write_backend_env
    backend_configuration_written=1
  fi
else
  timestamp="$(date -u +%Y%m%dT%H%M%SZ)"

  if ((RECONFIGURE)) && [[ -f "${ENV_FILE}" ]]; then
    current_blog_base_url="$(env_value BLOG_BASE_URL "${ENV_FILE}")"
    current_node_id="$(env_value MONITOR_NODE_ID "${ENV_FILE}")"
    current_key_id="$(env_value MONITOR_HMAC_KEY_ID "${ENV_FILE}")"
    current_secret="$(env_value MONITOR_HMAC_SECRET "${ENV_FILE}")"
    current_host_port="$(env_value MONITOR_HOST_PORT "${ENV_FILE}")"

    BLOG_BASE_URL="${BLOG_BASE_URL:-${current_blog_base_url}}"
    MONITOR_NODE_ID="${MONITOR_NODE_ID:-${current_node_id}}"
    MONITOR_HMAC_KEY_ID="${MONITOR_HMAC_KEY_ID:-${current_key_id}}"
    MONITOR_HMAC_SECRET="${MONITOR_HMAC_SECRET:-${current_secret}}"
    MONITOR_HOST_PORT="${MONITOR_HOST_PORT:-${current_host_port}}"

    backup_file "${ENV_FILE}" "${timestamp}"
    backup_file "${BACKEND_ENV_FILE}" "${timestamp}"
  fi

  host_name="$(hostname 2>/dev/null || printf 'server')"
  host_name="$(printf '%s' "${host_name}" | tr -c 'A-Za-z0-9._-' '-')"
  host_name="${host_name#-}"
  host_name="${host_name%-}"
  host_name="${host_name:-server}"

  prompt_required BLOG_BASE_URL "博客后端 HTTPS 根地址（不含 /api 等路径）"
  prompt_required MONITOR_NODE_ID "监控节点 ID" "monitor-${host_name}"
  prompt_required MONITOR_HMAC_KEY_ID "HMAC key ID" "monitor-$(date -u +%Y-%m)"
  prompt_required MONITOR_HOST_PORT "仅本机监听的健康检查端口" "8080"

  if [[ -z "${MONITOR_HMAC_SECRET:-}" ]]; then
    MONITOR_HMAC_SECRET="$(generate_secret)"
    log "已生成新的 HMAC 密钥（不会显示在终端）"
  fi

  validate_configuration "production" "false"
  write_monitor_env
  write_backend_env
  new_configuration=1
  backend_configuration_written=1
  log "已写入 .env，并设置为仅当前用户可读写"
fi

chmod 600 "${ENV_FILE}"
if [[ -f "${BACKEND_ENV_FILE}" ]]; then
  chmod 600 "${BACKEND_ENV_FILE}"
fi

compose config --quiet
log "Docker Compose 配置校验通过"

if ((backend_configuration_written)); then
  log "匹配的博客后端变量已写入 .backend-env（包含密钥，不在终端显示）"
fi

if ((PREPARE_ONLY)); then
  log "准备阶段完成。将 .backend-env 中的变量安全地配置到博客后端并重启后端，然后重新运行本脚本。"
  exit 0
fi

if ((new_configuration && !NON_INTERACTIVE)); then
  printf '\n'
  printf '请先把 %s 中的变量安全地配置到博客后端并重启后端。\n' "${BACKEND_ENV_FILE}"
  printf '该文件包含密钥，不能提交到 Git、粘贴到聊天或公开日志。\n'
  read -r -p "后端配置完成后按 Enter 继续启动；按 Ctrl+C 暂停安装。"
  printf '\n'
fi

docker info >/dev/null 2>&1 ||
  die "Docker daemon 不可用；请启动 Docker，或确认当前用户有权访问 Docker socket"

log "正在构建并启动 friend-link-monitor"
compose up -d --build

container_id="$(compose ps -q friend-link-monitor)"
[[ -n "${container_id}" ]] || die "Compose 未返回 friend-link-monitor 容器 ID"

deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))
health_status="starting"
container_status="unknown"

while ((SECONDS < deadline)); do
  container_status="$(docker inspect --format '{{.State.Status}}' "${container_id}" 2>/dev/null || printf 'missing')"
  health_status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "${container_id}" 2>/dev/null || printf 'missing')"

  if [[ "${health_status}" == "healthy" ]]; then
    log "安装完成：容器健康，探针已完成至少一次 catalog 同步"
    log "本机健康地址：http://127.0.0.1:${MONITOR_HOST_PORT}/health/ready"
    exit 0
  fi

  if [[ "${container_status}" == "exited" || "${container_status}" == "dead" || "${container_status}" == "missing" ]]; then
    break
  fi
  sleep 2
done

warn "容器未在 ${HEALTH_TIMEOUT_SECONDS} 秒内进入 healthy（容器=${container_status}，健康=${health_status}）"
compose ps
printf '\n排查命令：\n'
printf '  cd %q\n' "${PROJECT_ROOT}"
printf '  docker compose --env-file .env logs --tail 100\n'
printf '\n请确认博客后端已加载 .backend-env 中的变量，且以下接口可通过 BLOG_BASE_URL 访问：\n'
printf '  GET  /internal/blogroll-monitor/catalog\n'
printf '  POST /internal/blogroll-monitor/status-batch\n'
exit 1

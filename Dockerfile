# 多阶段构建：前端（Node 只在构建阶段使用）→ Go 单文件 → 带 ffmpeg 的精简运行镜像
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npx vite build

FROM golang:1.26-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /veilfin ./cmd/veilfin

FROM alpine:3.22
RUN apk add --no-cache ffmpeg tzdata ca-certificates \
 && adduser -D -u 1000 veilfin \
 && mkdir -p /data && chown veilfin /data
COPY --from=go /veilfin /usr/local/bin/veilfin
USER veilfin
ENV VF_DATA=/data VF_LISTEN=:8097 TZ=Asia/Shanghai
EXPOSE 8097
VOLUME ["/data"]
ENTRYPOINT ["veilfin"]
CMD ["server"]

#!/bin/bash

echo "Backing up previous latest image (if exists)..."
docker image inspect new-api:latest >/dev/null 2>&1 && docker tag new-api:latest new-api:backup

echo "Building new-api Docker image..."
# 不限制资源的build
# docker build -t new-api:latest .

# 限制使用 2 个 CPU 核心，CPU 时间片分配比例为 512
docker build --cpus=2 --cpu-shares=512 -t new-api:latest .

echo "Build complete!"
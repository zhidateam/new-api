#!/bin/bash

# 获取系统 CPU 核心总数
total_cpus=$(nproc)
# 计算默认使用的 CPU 数（系统总核心数的一半，向上取整）
default_cpus=$(( (total_cpus + 1) / 2 ))

# 如果传入参数，使用参数值；否则使用默认值
if [ $# -eq 0 ]; then
    use_cpus=$default_cpus
else
    use_cpus=$1
fi

# 确保 CPU 数不超过系统总核心数
if [ $use_cpus -gt $total_cpus ]; then
    use_cpus=$total_cpus
fi

echo "系统总 CPU 核心数: $total_cpus"
echo "将使用 CPU 核心数: $use_cpus"

echo "Backing up previous latest image (if exists)..."
docker image inspect new-api:latest >/dev/null 2>&1 && docker tag new-api:latest new-api:backup

echo "Building new-api Docker image..."
# 使用指定的 CPU 核心数构建，保持原有的 CPU 时间片分配比例
# 使用 cpu-period 和 cpu-quota 替代 cpus 参数
# cpu-quota = cpu-period * cpus
docker build --cpu-period=100000 --cpu-quota=$(( 100000 * $use_cpus )) --cpu-shares=512 -t new-api:latest .

echo "Build complete!"
#!/bin/bash

echo "Backing up previous latest image (if exists)..."
docker image inspect new-api:latest >/dev/null 2>&1 && docker tag new-api:latest new-api:backup

echo "Building new-api Docker image..."
docker build -t new-api:latest .

echo "Build complete!"
#!/bin/bash

echo "Building new-api Docker image..."
docker build -t new-api:latest .

echo "Build complete!"
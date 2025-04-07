#!/bin/bash

# Get the current version from VERSION file
VERSION=$(cat VERSION)

# Build the Docker image
echo "Building new-api Docker image with version: $VERSION"
docker build -t new-api:$VERSION -t new-api:latest .

echo "Build complete!"
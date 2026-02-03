#!/bin/bash

# VRSky Test Scripts - Common Utility Functions
# Shared functions used across multiple test scripts

# Change to project src directory with error handling
change_to_src_dir() {
    cd "${PROJECT_ROOT}/src" || { echo "Failed to change to src directory"; exit 1; }
}

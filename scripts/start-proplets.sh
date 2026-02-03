#!/bin/bash

# Default to all available proplets if no argument provided
NUM_PROPLETS=${1:-1}

echo "Starting $NUM_PROPLETS proplet(s)..."

for ((i=1; i<=NUM_PROPLETS; i++)); do
    x-terminal-emulator -e propeller-proplet &
    sleep 1
done

wait
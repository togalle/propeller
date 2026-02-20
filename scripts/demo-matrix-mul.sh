#!/bin/bash

TASK_RESPONSE=$(curl -s -X POST "http://localhost:7070/tasks" \
    -H "Content-Type: application/json" \
    -d '{"name": "matrix_mul", "inputs": [15]}')

TASK_ID=$(echo "$TASK_RESPONSE" | jq -r '.id')

curl -s -X PUT "http://localhost:7070/tasks/$TASK_ID/upload" \
    -F 'file=@/home/tomasgalle/UGent/thesis/propeller/build/matrix-mul.wasm'

curl -s -X GET "http://localhost:7070/tasks/$TASK_ID"

curl -s -X POST "http://localhost:7070/tasks/$TASK_ID/start"

sleep 0.5

curl -s -X GET "http://localhost:7070/tasks/$TASK_ID"

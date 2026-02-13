#!/bin/bash

TASK_RESPONSE=$(curl -s -X POST "http://localhost:7070/tasks" \
  -H "Content-Type: application/json" \
  -d '{"name": "naive_fib", "inputs": [40]}')

TASK_ID=$(echo "$TASK_RESPONSE" | jq -r '.id')

curl -s -X PUT "http://localhost:7070/tasks/$TASK_ID/upload" \
  -F 'file=@/home/tomasgalle/UGent/thesis/propeller/build/naive-fib.wasm'

curl -s -X GET "http://localhost:7070/tasks/$TASK_ID"

curl -s -X POST "http://localhost:7070/tasks/$TASK_ID/start"

sleep 1.5

curl -s -X GET "http://localhost:7070/tasks/$TASK_ID"

#!/bin/bash

TASK_RESPONSE=$(curl -s -X POST "http://localhost:7070/tasks" \
    -H "Content-Type: application/json" \
    -d '{"name": "matrix_mul", "inputs": [200], "scheduler": "dynamic", "weights": {"cpu_percent": 1.0, "distance": 0.1}}')

TASK_ID=$(echo "$TASK_RESPONSE" | jq -r '.id')

curl -s -X PUT "http://localhost:7070/tasks/$TASK_ID/upload" \
    -F 'file=@/home/tomasgalle/UGent/thesis/propeller/build/matrix-mul.wasm' | jq

curl -s -X GET "http://localhost:7070/tasks/$TASK_ID" | jq 'del(.file)'

curl -s -X POST "http://localhost:7070/tasks/$TASK_ID/start" | jq

sleep 5

curl -s -X GET "http://localhost:7070/tasks/$TASK_ID" | jq 'del(.file)'

# TASK_RESPONSE=$(curl -s -X POST "http://localhost:7070/tasks" \
#     -H "Content-Type: application/json" \
#     -d '{"name": "naive_fib", "inputs": [44], "scheduler": "dynamic", "weights": {"cpu_percent": 1.0, "distance": 0.1}}')
#
# TASK_ID=$(echo "$TASK_RESPONSE" | jq -r '.id')
#
# curl -s -X PUT "http://localhost:7070/tasks/$TASK_ID/upload" \
#     -F 'file=@/home/tomasgalle/UGent/thesis/propeller/build/naive-fib.wasm' | jq
#
# curl -s -X GET "http://localhost:7070/tasks/$TASK_ID" | jq 'del(.file)'
#
# curl -s -X POST "http://localhost:7070/tasks/$TASK_ID/start" | jq
#
# sleep 5
#
# curl -s -X GET "http://localhost:7070/tasks/$TASK_ID" | jq 'del(.file)'

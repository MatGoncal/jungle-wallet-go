#!/bin/bash
set -euo pipefail

export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-us-east-1}"
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"

awslocal() {
  awslocal "$@" 2>/dev/null || aws --endpoint-url=http://localhost:4566 "$@"
}

echo "Provisioning SQS queues and IAM policies..."

# DLQ for wager transactions
awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes '{
    "FifoQueue":"true",
    "ContentBasedDeduplication":"false",
    "MessageRetentionPeriod":"1209600"
  }'

DLQ_URL=$(awslocal sqs get-queue-url --queue-name wager-transactions-dlq.fifo --query QueueUrl --output text)
DLQ_ARN=$(awslocal sqs get-queue-attributes --queue-url "$DLQ_URL" --attribute-names QueueArn --query Attributes.QueueArn --output text)

# Main wager FIFO with redrive
awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes "{
    \"FifoQueue\":\"true\",
    \"ContentBasedDeduplication\":\"false\",
    \"VisibilityTimeout\":\"30\",
    \"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"${DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"
  }"

# Outbound events FIFO
awslocal sqs create-queue \
  --queue-name wallet-events.fifo \
  --attributes '{
    "FifoQueue":"true",
    "ContentBasedDeduplication":"false",
    "VisibilityTimeout":"30"
  }'

# IAM: publisher may only SendMessage to events queue
awslocal iam create-user --user-name wallet-publisher || true
awslocal iam put-user-policy --user-name wallet-publisher --policy-name PublishEvents --policy-document '{
  "Version":"2012-10-17",
  "Statement":[{
    "Effect":"Allow",
    "Action":["sqs:SendMessage","sqs:GetQueueUrl","sqs:GetQueueAttributes"],
    "Resource":"arn:aws:sqs:us-east-1:000000000000:wallet-events.fifo"
  }]
}' || true

# IAM: consumer may only Receive/Delete on wager queue
awslocal iam create-user --user-name wallet-consumer || true
awslocal iam put-user-policy --user-name wallet-consumer --policy-name ConsumeWagers --policy-document '{
  "Version":"2012-10-17",
  "Statement":[{
    "Effect":"Allow",
    "Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:GetQueueUrl","sqs:GetQueueAttributes","sqs:ChangeMessageVisibility"],
    "Resource":"arn:aws:sqs:us-east-1:000000000000:wager-transactions.fifo"
  }]
}' || true

echo "SQS provisioning complete."
awslocal sqs list-queues

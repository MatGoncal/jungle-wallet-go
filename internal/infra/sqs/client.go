package sqs

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"go.uber.org/fx"
)

var Module = fx.Module("sqs",
	fx.Provide(NewClient),
	fx.Provide(NewReadyChecker),
)

// Client wraps the AWS SQS client pointed at LocalStack (or real AWS).
type Client struct {
	cfg  config.Config
	sqs  *awssqs.Client
	http *http.Client
}

func NewClient(cfg config.Config) (*Client, error) {
	httpClient := &http.Client{Timeout: 5 * time.Second}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.SQSRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.SQSAccessKeyID,
			cfg.SQSSecretAccessKey,
			"",
		)),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	sqsClient := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(cfg.SQSEndpoint)
	})

	return &Client{cfg: cfg, sqs: sqsClient, http: httpClient}, nil
}

func (c *Client) SQS() *awssqs.Client { return c.sqs }

// ReadyChecker verifies LocalStack/SQS reachability via GetQueueUrl.
type ReadyChecker struct {
	client *Client
}

func NewReadyChecker(client *Client) *ReadyChecker {
	return &ReadyChecker{client: client}
}

func (c *ReadyChecker) Name() string { return "sqs" }

func (c *ReadyChecker) Check(ctx context.Context) error {
	_, err := c.client.sqs.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{
		QueueName: aws.String("wager-transactions.fifo"),
	})
	if err != nil {
		return fmt.Errorf("sqs ready: %w", err)
	}
	return nil
}

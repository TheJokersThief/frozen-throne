package frozen_throne_server

type Config struct {
	WebhookSecret       string `envconfig:"WEBHOOK_SECRET" required:"true"`
	GithubAppID         int64  `envconfig:"GITHUB_APP_ID" required:"true"`
	GithubAppPrivateKey string `envconfig:"GITHUB_APP_PRIVATE_KEY" required:"true"`
}

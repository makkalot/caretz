package main

import "github.com/makkalot/caretz"

func main() {
	var _ = caretz.ClientOptions{
		Model: caretz.ModelOptions{
			BaseURL:       "http://127.0.0.1:8001/v1",
			APIKey:        "noset",
			Model:         "qwen-code",
			ContextWindow: 60000,
		},
	}
}

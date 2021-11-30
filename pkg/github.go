package main

import (
	"context"

	"github.com/google/go-github/v40/github"
	"golang.org/x/oauth2"
)

func githubClient(tok string) github.Client {
	switch {
	case len(tok) == 0:
		return *github.NewClient(nil)
	default:
		ctx := context.Background()
		tc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: tok},
		))
		return *github.NewClient(tc)
	}
}

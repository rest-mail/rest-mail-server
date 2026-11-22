package session

import "context"

type serverBinding struct{}

func BindServer(ctx context.Context, server string) context.Context {
	return context.WithValue(ctx, serverBinding{}, server)
}
